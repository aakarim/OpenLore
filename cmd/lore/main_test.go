package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/lore"
	"github.com/aakarim/go-openlore/internal/lore/api"
	"github.com/aakarim/go-openlore/pkg/openlore"
	"github.com/aakarim/go-openlore/pkg/shell"
)

func TestInitRepository(t *testing.T) {
	for _, dir := range []string{"", "team/knowledge"} {
		t.Run("directory="+dir, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			// Even when a default repository exists, init must target cwd or
			// its explicit argument, not the last login's repository.
			previous := t.TempDir()
			original := []byte("version: '1'\nport: 2233\n")
			if err := os.WriteFile(filepath.Join(previous, "openlore.yml"), original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := lore.SaveDefaultRoot(previous); err != nil {
				t.Fatal(err)
			}
			args := []string{"init"}
			if dir != "" {
				args = append(args, dir)
			}
			var out bytes.Buffer
			if err := run(args, &out); err != nil {
				t.Fatalf("init: %v; %s", err, out.String())
			}
			root := filepath.Join(cwd, dir)
			root, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			found, err := lore.FindRoot(root)
			if err != nil || found != root {
				t.Fatalf("repository root = %q, %v; want %q", found, err, root)
			}
			cfg, err := config.New(config.WithConfigFile(filepath.Join(root, "openlore.yml")))
			if err != nil {
				t.Fatal("generated config:", err)
			}
			if cfg.ConfigVersion != "1" || cfg.Port != 2222 || cfg.HTTPPort != 8080 || cfg.MetricsPort != 3000 || !cfg.Readonly || !cfg.MCPEnabled || cfg.MCPPath != "/mcp" || !cfg.APIEnabled || cfg.APIPath != "/api" {
				t.Fatal("generated config does not use default server settings")
			}
			docs := t.TempDir()
			if err := os.WriteFile(filepath.Join(docs, "README.md"), []byte("Team knowledge\n"), 0600); err != nil {
				t.Fatal(err)
			}
			sh := shell.NewShell(openlore.NewDirFS(docs, cfg.Files))
			sh.SetCwd(cfg.DefaultCwd)
			var stdout, stderr bytes.Buffer
			if code := sh.Exec("cat README.md", &stdout, &stderr, nil); code != 0 || stdout.String() != "Team knowledge\n" {
				t.Fatalf("read from initial shell directory: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			// A rerun must preserve even a hand-edited configuration.
			file := filepath.Join(root, "openlore.yml")
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := run(args, &out); err == nil {
				t.Fatal("init accepted an existing config")
			}
			for _, path := range []string{file, filepath.Join(previous, "openlore.yml")} {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatalf("existing config changed: %s: %q, %v", path, got, err)
				}
			}
		})
	}
}

func TestInitRejectsInvalidArguments(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"init", "first", "second"}, {"init", "--unknown"}} {
		if err := run(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
	if _, err := os.Stat("first"); !os.IsNotExist(err) {
		t.Fatalf("invalid init created a directory: %v", err)
	}
}

func TestFolderSyncAgainstServer(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	port := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port())
	yml := fmt.Sprintf("version: '1'\nport: %d\nhttp_port: %s\nmetrics_port: 0\nexternal_url: %s\nauth_file: %s/lore.json\ndata_dir: %s/data\nhost_key_path: %s/hostkey\nanalytics:\n  enabled: false\ntokens:\n  issuer: %s\n  audience: %s\n", port(), strings.TrimPrefix(base, "http://127.0.0.1:"), base, root, root, root, base, base)
	if err := os.WriteFile("openlore.yml", []byte(yml), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("lore.json", []byte(`{"roles":{"alice":{}},"identities":[{"name":"alice","roles":["alice"]}],"docsets":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(config.WithConfigFile("openlore.yml"))
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := openlore.NewIssuerFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := issuer.Mint(openlore.Attribution{Principal: "alice"}, openlore.ScopeFull, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openlore.NewServer(t.TempDir(), config.WithConfigFile(filepath.Join(root, "openlore.yml")))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/api/docstore/identity")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server not ready:", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The identity acknowledgement on first sync is answered by a scripted prompt.
	old := newPrompter
	newPrompter = func(io.Writer) prompter { return &scriptedPrompter{answers: []bool{true}} }
	t.Cleanup(func() { newPrompter = old })
	var out bytes.Buffer
	call := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := run(args, &out); err != nil {
			t.Fatalf("CLI failed: %v; %s", err, out.String())
		}
		return out.String()
	}
	call("login", base, "--token", token)
	if err := os.Mkdir("notes", 0700); err != nil {
		t.Fatal(err)
	}
	put := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join("notes", name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("a.md", "old")
	put("gone.md", "reuse")
	call("source", "add", "notes", "--name", "notes")
	if _, err := os.Stat(filepath.Join(root, "home", "alice", "sources.yaml")); err != nil {
		t.Fatal("definitions not stored under home/alice:", err)
	}
	if got := call("sync"); !strings.Contains(got, "2 files · 2 new blobs · +2 ~0 -0") || !strings.Contains(got, "folder/alice/notes") {
		t.Fatal(got)
	}
	put("a.md", "new")
	put("b.md", "reuse")
	if err := os.Remove("notes/gone.md"); err != nil {
		t.Fatal(err)
	}
	if got := call("status"); !strings.Contains(got, "+1 ~1 -1 · 1 blobs to upload") {
		t.Fatal(got)
	}
	if got := call("sync", "notes"); !strings.Contains(got, "2 files · 1 new blobs · +1 ~1 -1") {
		t.Fatal(got)
	}
	if got := call("sync"); !strings.Contains(got, "0 new blobs") || !strings.Contains(got, "(unchanged)") {
		t.Fatal(got)
	}
	// The local cache is keyed by store, so another identity's "notes" would
	// never share or delete it.
	cache := filepath.Join(root, ".local", "sources", "folder", "alice", "notes", "files", "a.md")
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("staged copy missing from the store cache:", err)
	}
	cred, err := lore.LoadCredentials(root)
	if err != nil {
		t.Fatal(err)
	}
	history, err := (&api.Client{APIPath: cfg.APIPath, Credentials: cred}).Snapshots("folder/alice/notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].Deleted != 1 || history[0].Files["gone.md"].SHA256 == "" || history[1].Files["gone.md"].SHA256 != "" {
		t.Fatalf("history: %+v", history)
	}
	if err := run([]string{"sync", "unknown"}, &out); err == nil {
		t.Fatal("unknown source accepted")
	}
	readToken, _, err := issuer.Mint(openlore.Attribution{Principal: "alice"}, openlore.ScopeRead, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	call("login", base, "--token", readToken)
	put("a.md", "must not upload")
	out.Reset()
	if err := run([]string{"sync", "notes"}, &out); err == nil || !strings.Contains(out.String(), "full scope required") {
		t.Fatalf("server rejection was not propagated: %v; %s", err, out.String())
	}
}

func TestClaudeConnectionPreviewAndSafety(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := filepath.Join(home, "knowledge")
	project := filepath.Join(home, "src", "pay.ments")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"CLAUDE.md", "AGENTS.md", ".claude/rules/style.md", ".claude/rules/settings.local.json", ".claude/rules/credentials.md", ".claude/rules/api_key.md", ".claude/agent-memory-test/MEMORY.md"} {
		write(filepath.Join(project, p))
	}
	// An external file linked from an otherwise eligible rule must not upload.
	write(filepath.Join(home, "private.md"))
	if err := os.Symlink(filepath.Join(home, "private.md"), filepath.Join(project, ".claude", "rules", "link.md")); err != nil {
		t.Fatal(err)
	}
	key, _ := lore.ClaudeProjectKey(project)
	write(filepath.Join(home, ".claude", "projects", key, "memory", "MEMORY.md"))
	write(filepath.Join(home, ".claude", "projects", key, "session.jsonl"))
	write(filepath.Join(home, ".claude", "CLAUDE.md"))
	var out bytes.Buffer
	prompt := &scriptedPrompter{}
	c := newClient(lore.Repository{Root: root}, &out)
	c.api.Credentials = lore.Credentials{Identity: "alice"}
	c.prompt = prompt
	if err := c.addClaude(project, "payments", nil, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	file := config.HomeSourcesFile(root, "alice")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("blank selection wrote sources.yaml")
	}
	if !strings.HasPrefix(out.String(), "Connecting a synced Claude source, not a one-off import.") || !strings.Contains(out.String(), "No sources connected.") {
		t.Fatalf("preview: %s", out.String())
	}
	if len(prompt.options) != 1 || len(prompt.options[0]) != 4 || len(prompt.preselected[0]) != 0 {
		t.Fatalf("groups offered without a scope flag: %v ticked %v", prompt.options, prompt.preselected)
	}
	for i, want := range []string{"Project instructions · ~/src/pay.ments · 3 files", "Project auto memory · ~/.claude/projects/" + key + "/memory · 1 file", "Session transcripts · ~/.claude/projects/" + key + " · 1 file", "Personal instructions · ~/.claude · 1 file"} {
		if option := prompt.options[0][i]; option != want {
			t.Fatalf("option %d: %q, want %q", i, option, want)
		}
	}
	// --project offers only project-scope groups, with opt-in transcripts unticked.
	prompt.selected = [][]int{{0, 1}}
	prompt.answers = []bool{true}
	out.Reset()
	if err := c.addClaude(project, "payments", []string{"project"}, []string{"**"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(prompt.options) != 2 || len(prompt.options[1]) != 3 || !strings.HasPrefix(prompt.options[1][2], "Session transcripts · ") || !slices.Equal(prompt.preselected[1], []int{0, 1}) {
		t.Fatalf("groups offered with --project: %v ticked %v", prompt.options[1], prompt.preselected[1])
	}
	sources, err := config.LoadSources(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources.Sources) != 2 || sources.Sources[0].Project != "~/src/pay.ments" {
		t.Fatalf("definitions: %+v", sources)
	}
	store, err := c.repo.StorePath(sources.Sources[0], "alice")
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := c.repo.ScanSource(sources.Sources[0], store, false)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for p := range files {
		names = append(names, p)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{".claude/rules/style.md", "AGENTS.md", "CLAUDE.md"}) {
		t.Fatalf("unexpected upload candidates: %v", names)
	}
	if strings.Contains(out.String(), "credentials.md") || strings.Contains(out.String(), "session.jsonl") {
		t.Fatal("excluded file appeared in exact preview")
	}
	listing := "\npayments-instructions → claude/alice/" + key + "/instructions (3 files)\n    .claude/rules/style.md\n    AGENTS.md\n    CLAUDE.md\n\npayments-memory → claude/alice/" + key + "/memory (1 file)\n    MEMORY.md\n\n"
	if !strings.HasSuffix(out.String(), listing) {
		t.Fatalf("listing: %q", out.String())
	}
	// Session transcripts sync only when chosen explicitly.
	prompt.selected = [][]int{{2}}
	prompt.answers = []bool{true}
	out.Reset()
	if err := c.addClaude(project, "history", []string{"project"}, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\nhistory-transcripts → claude/alice/"+key+"/transcripts (1 file)\n    session.jsonl\n") {
		t.Fatalf("transcript listing: %q", out.String())
	}
	sources, err = config.LoadSources(file)
	if err != nil || len(sources.Sources) != 3 || sources.Sources[2].Group != "transcripts" {
		t.Fatalf("definitions: %+v %v", sources, err)
	}
	// --personal offers only the personal group, ticked.
	out.Reset()
	if err := c.addClaude(project, "me", []string{"personal"}, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if last := len(prompt.options) - 1; len(prompt.options[last]) != 1 || !strings.HasPrefix(prompt.options[last][0], "Personal instructions · ") || !slices.Equal(prompt.preselected[last], []int{0}) {
		t.Fatalf("groups offered with --personal: %v ticked %v", prompt.options[last], prompt.preselected[last])
	}
}

func TestDefaultRepositoryOutsideTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := filepath.Join(home, "knowledge")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "openlore.yml"), []byte("version: '1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// lore login records the resolved root; credentials are keyed by it.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(home, "src", "payments")
	if err := os.MkdirAll(elsewhere, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(elsewhere)
	var out bytes.Buffer
	if err := run([]string{"source", "list"}, &out); err == nil || !strings.Contains(err.Error(), "run lore login from one to make it the default") {
		t.Fatalf("without a default: %v", err)
	}
	if err := lore.SaveDefaultRoot(root); err != nil {
		t.Fatal(err)
	}
	file, err := lore.DefaultFile()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 || !strings.HasPrefix(file, filepath.Join(home, ".config", "lore")) {
		t.Fatalf("default file %s: %v %v", file, info, err)
	}
	// Credentials for the default repository, as lore login would have saved them.
	if err := lore.SaveCredentials(root, lore.Credentials{Identity: "alice"}); err != nil {
		t.Fatal(err)
	}
	sources := config.HomeSourcesFile(root, "alice")
	if err := os.MkdirAll(filepath.Dir(sources), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sources, []byte("version: '1'\nsources:\n  - name: notes\n    type: folder\n    path: notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"source", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "notes · folder · notes · folder/alice/notes") {
		t.Fatalf("default repository's sources not used: %q", out.String())
	}
	// A stale default no longer holding openlore.yml is ignored.
	if err := os.Remove(filepath.Join(root, "openlore.yml")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"source", "list"}, &out); err == nil || !strings.Contains(err.Error(), "not inside a knowledge repository") {
		t.Fatalf("stale default: %v", err)
	}
}

func TestFolderSourceEditsPreserveComments(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.Mkdir("notes", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("notes/guide.md", []byte("guide"), 0600); err != nil {
		t.Fatal(err)
	}
	file := config.HomeSourcesFile(root, "alice")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("# shared definitions\nversion: '1' # keep this\nsources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := newClient(lore.Repository{Root: root}, &out)
	if err := c.addSource([]string{"notes", "--name", "notes"}); err == nil || !strings.Contains(err.Error(), "run lore login first") {
		t.Fatalf("source add without login: %v", err)
	}
	c.api.Credentials = lore.Credentials{Identity: "alice"}
	if err := c.addSource([]string{"notes", "--name", "notes", "--include", "**/*.md", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	sources, err := config.LoadSources(file)
	if err != nil || len(sources.Sources) != 0 {
		t.Fatal("dry-run persisted source")
	}
	if err := c.addSource([]string{"notes", "--name", "notes", "--include", "**/*.md"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !bytes.Contains(b, []byte("# shared definitions")) || !bytes.Contains(b, []byte("# keep this")) {
		t.Fatalf("lost comments: %s", b)
	}
	if err := c.removeSource([]string{"notes"}); err != nil {
		t.Fatal(err)
	}
	sources, err = config.LoadSources(file)
	if err != nil || len(sources.Sources) != 0 {
		t.Fatal("source not removed")
	}
}

// scriptedPrompter records the options offered (and which started ticked) and
// replays scripted answers; an exhausted script chooses nothing and declines.
type scriptedPrompter struct {
	options     [][]string
	preselected [][]int
	selected    [][]int
	answers     []bool
}

func (s *scriptedPrompter) multiSelect(_ string, options []string, preselected []int) ([]int, error) {
	s.options = append(s.options, options)
	s.preselected = append(s.preselected, preselected)
	if len(s.selected) == 0 {
		return nil, nil
	}
	next := s.selected[0]
	s.selected = s.selected[1:]
	return next, nil
}
func (s *scriptedPrompter) confirm(string) (bool, error) {
	if len(s.answers) == 0 {
		return false, nil
	}
	next := s.answers[0]
	s.answers = s.answers[1:]
	return next, nil
}
