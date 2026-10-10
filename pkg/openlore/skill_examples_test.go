package openlore

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
)

// The instruction documents a server hands to agents (`agents`,
// `openlore-skill`, `agents-shellm`, `agents-shellm-housekeeping`, `teach`) are copied
// into persistent agent context, so every example must run as written against
// the server that produced it. These tests boot a server whose docsets are
// mounted at /backend and /notes (not /docs), render each document for a
// publishing, a directly-writing, and a read-only identity, then execute every
// bash example through that identity's session shell.

// bootSkillServer serves /backend (with an inbox) and /notes from a temp root,
// plus a docset per extra mount, each seeded with one file. alice may publish
// to backend and read everything else; bob may write notes and the extra
// mounts directly; anyone else is a read-only guest.
func bootSkillServer(t *testing.T, extraMounts ...string) *Server {
	t.Helper()
	root := t.TempDir()
	// The grep examples search for the literal "search term"; grep exits 1 on
	// no match, so the fixture must contain it under every mount.
	for p, body := range map[string]string{
		"backend/README.md":         "# Backend\n\nUse the search term here. See [notes](research/notes.md).\n",
		"backend/research/notes.md": "---\nupdated: 2026-01-01\n---\n# Notes\n",
		"notes/api.md":              "# API\n\nAnother search term.\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mount := func(p string) []config.PathMapping { return []config.PathMapping{{Source: p, Display: p}} }
	keyless := true
	auth := &config.AuthConfig{
		AllowKeyless:    &keyless,
		UnknownIdentity: "allow",
		Roles:           map[string]config.RoleSpec{"publisher": {}, "editor": {}},
		Docsets: map[string]config.DocsetSpec{
			"backend": {Paths: mount("/backend"), Inbox: "inbox", Access: config.DocsetAccess{Allow: map[string]string{"guest": "ro", "publisher": "publish"}}},
			"notes":   {Paths: mount("/notes"), Access: config.DocsetAccess{Allow: map[string]string{"guest": "ro", "publisher": "ro", "editor": "rw"}}},
		},
		Identities: []config.AuthIdentity{
			{Name: "alice", Roles: []string{"publisher"}},
			{Name: "bob", Roles: []string{"editor"}},
		},
	}
	for _, m := range extraMounts {
		seed := filepath.Join(root, strings.TrimPrefix(m, "/"), "example", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(seed), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(seed, []byte("---\nname: example\ndescription: x\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		auth.Docsets[strings.TrimPrefix(m, "/")] = config.DocsetSpec{Paths: mount(m), Access: config.DocsetAccess{Allow: map[string]string{"guest": "ro", "publisher": "ro", "editor": "rw"}}}
	}
	authFile := filepath.Join(t.TempDir(), "lore.json")
	writeAuthFixture(t, authFile, auth)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := NewServer(root, WithReadonly(false), WithPort(2299), config.WithDataDir(t.TempDir()), config.WithAuthFile(authFile), WithLogger(logger))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	if err := s.RegisterPlugin(NewInboxPlugin()); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	return s
}

func skillSession(t *testing.T, s *Server, name string) *shell.Shell {
	t.Helper()
	if name == "guest" {
		return s.buildSessionShell(s.anonymousIdentity())
	}
	id, ok := s.identityForName(name)
	if !ok {
		t.Fatalf("identity %q not found", name)
	}
	return s.buildSessionShell(id)
}

func renderSkillFor(t *testing.T, sh *shell.Shell, command string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline(command, &out, &errOut, nil); code != 0 {
		t.Fatalf("%s exit=%d stderr=%q", command, code, errOut.String())
	}
	return out.String()
}

var (
	// sshWrapped matches a remote command quoted after an ssh invocation, with
	// an optional local `cat <file> |` feeding its stdin and a trailing comment.
	sshWrapped = regexp.MustCompile(`^(cat \S+ \| )?ssh (?:-n )?(?:-p \S+ \S+|\$OPENLORE_SSH) "(.*)"\s*(?:#.*)?$`)
	// placeholder marks text the agent must substitute before running.
	placeholder = regexp.MustCompile(`<[a-z][a-z-]*>`)
)

// exampleCommands extracts the shell lines an agent would run from the bash
// code blocks of a rendered document. ssh-wrapped lines yield the remote
// command; a local `cat file |` prefix becomes `echo x |`. Lines that cannot
// run inside the server shell are dropped: local-only commands (ssh, sshfs,
// export, mkdir of local skill dirs), local function definitions and their
// calls, heredoc bodies, and anything still carrying a <placeholder>.
func exampleCommands(doc string) []string {
	var out []string
	inBash, inFunc, heredocEnd := false, false, ""
	localFuncs := map[string]bool{}
	for _, raw := range strings.Split(doc, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "```bash"):
			inBash = true
			continue
		case strings.HasPrefix(line, "```"):
			inBash = false
			continue
		case !inBash || line == "" || strings.HasPrefix(line, "#"):
			continue
		}
		if heredocEnd != "" {
			if line == heredocEnd {
				heredocEnd = ""
			}
			continue
		}
		if inFunc {
			inFunc = line != "}"
			continue
		}
		if strings.HasSuffix(line, "{") {
			inFunc = true
			localFuncs[strings.TrimSuffix(strings.Fields(line)[0], "()")] = true
			continue
		}
		if i := strings.Index(line, "<<'"); i >= 0 {
			heredocEnd = strings.SplitN(line[i+3:], "'", 2)[0]
			continue
		}
		if m := sshWrapped.FindStringSubmatch(line); m != nil {
			line = strings.NewReplacer(`\"`, `"`, `\$`, `$`).Replace(m[2])
			if m[1] != "" {
				line = "echo x | " + line
			}
		} else if strings.HasPrefix(line, "ssh") || strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "mkdir -p .") || localFuncs[strings.Fields(line)[0]] {
			continue
		}
		if placeholder.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func TestExampleCommandsUnwrapsSSHAndSkipsLocalOnlyLines(t *testing.T) {
	doc := "text\n```bash\n# comment\nssh -p 2299 <host> \"tree -L 2 /\"   # discover\n" +
		"cat report.md | ssh $OPENLORE_SSH \"publish /backend/r.md\"\n" +
		"ssh -p 2299 <host>\nsshfs -p 2299 <host>:/ /mnt -o ro\nexport OPENLORE_SSH=\"-p 1 h\"\n" +
		"cat <<'EOF' | ssh -p 2299 <host> \"cat > /x\"\nbody: 1\nEOF\n" +
		"f() {\n  inner\n}\nf ~/x\nssh $OPENLORE_SSH \"cat /t/<run>/x\"\ngrep -r \"q\" /backend\n```\n" +
		"```text\nnot a command\n```\n"
	got := exampleCommands(doc)
	want := []string{"tree -L 2 /", "echo x | publish /backend/r.md", `grep -r "q" /backend`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

// skillCommands lists every embedded document rendered per session. teach is
// an onboarding script with no access branches, so only the generic
// assertions apply to it.
var skillCommands = []string{"agents", "openlore-skill", "agents-shellm", "agents-shellm-housekeeping", "teach"}

func TestSkillDocumentsRenderAgainstRealMounts(t *testing.T) {
	s := bootSkillServer(t)
	for _, command := range skillCommands {
		for _, who := range []string{"alice", "bob", "guest"} {
			t.Run(command+"/"+who, func(t *testing.T) {
				sh := skillSession(t, s, who)
				doc := renderSkillFor(t, sh, command)
				for _, bad := range []string{"/docs", "{{", "}}", "publish <docset>", "-p 2222"} {
					if strings.Contains(doc, bad) {
						t.Errorf("%s for %s still contains %q:\n%s", command, who, bad, doc)
					}
				}
				if strings.Contains(doc, "ssh -p") && !strings.Contains(doc, "-p 2299") {
					t.Errorf("%s for %s does not advertise the configured SSH port", command, who)
				}
				if strings.Contains(doc, "/trajectories") || strings.Contains(doc, "/skills") {
					t.Errorf("%s for %s mentions a folder this server does not have:\n%s", command, who, doc)
				}
				if command == "teach" {
					if !strings.Contains(doc, "ssh -p 2299 <address>") {
						t.Errorf("teach does not render the port into its ssh examples:\n%s", doc)
					}
					return
				}
				// Each identity sees the publishing branch that matches its access.
				want := map[string]string{
					"alice": "publish /backend/",
					"bob":   "can write directly",
					"guest": "read-only",
				}[who]
				if command == "agents-shellm-housekeeping" && who != "alice" {
					want = "no publish access"
				}
				if !strings.Contains(doc, want) {
					t.Errorf("%s for %s lacks %q:\n%s", command, who, want, doc)
				}
				if who != "alice" && strings.Contains(doc, "publish /backend/") {
					t.Errorf("%s for %s shows alice's publish example:\n%s", command, who, doc)
				}
			})
		}
	}
}

func TestSkillDocumentExamplesRunAgainstTheServer(t *testing.T) {
	s := bootSkillServer(t)
	for _, command := range skillCommands {
		for _, who := range []string{"alice", "bob", "guest"} {
			t.Run(command+"/"+who, func(t *testing.T) {
				sh := skillSession(t, s, who)
				examples := exampleCommands(renderSkillFor(t, sh, command))
				if len(examples) == 0 {
					t.Fatalf("%s rendered no runnable examples", command)
				}
				t.Logf("running %d examples: %q", len(examples), examples)
				for _, example := range examples {
					var out, errOut bytes.Buffer
					if code := sh.ExecPipeline(example, &out, &errOut, strings.NewReader("")); code != 0 {
						t.Errorf("%s example %q exit=%d stderr=%q stdout=%q", command, example, code, errOut.String(), out.String())
					}
				}
			})
		}
	}
}

// The trajectory and skills-collection sections exist only on servers that
// have those folders, and their examples must run there. The trajectory sync
// procedure writes directly, so only an identity that can write
// /trajectories sees it; everyone else is told how to get access.
func TestSkillDocumentsIncludeOptionalSectionsWhenFoldersExist(t *testing.T) {
	s := bootSkillServer(t, "/trajectories", "/skills")
	for _, who := range []string{"alice", "bob"} {
		sh := skillSession(t, s, who)
		for command, section := range map[string]string{
			"agents-shellm":              "## Sharing run trajectories",
			"agents-shellm-housekeeping": "## Check skill coverage",
		} {
			doc := renderSkillFor(t, sh, command)
			if !strings.Contains(doc, section) {
				t.Errorf("%s for %s omits %q on a server that has the folder:\n%s", command, who, section, doc)
			}
			examples := exampleCommands(doc)
			t.Logf("%s/%s: running %d examples: %q", command, who, len(examples), examples)
			for _, example := range examples {
				var out, errOut bytes.Buffer
				if code := sh.ExecPipeline(example, &out, &errOut, strings.NewReader("")); code != 0 {
					t.Errorf("%s example %q exit=%d stderr=%q stdout=%q", command, example, code, errOut.String(), out.String())
				}
			}
		}
		doc := renderSkillFor(t, sh, "agents-shellm")
		canSync := strings.Contains(doc, "sync_traj()")
		if who == "bob" && !canSync {
			t.Errorf("agents-shellm hides the sync procedure from bob, who can write /trajectories:\n%s", doc)
		}
		if who == "alice" && (canSync || !strings.Contains(doc, "cannot write to `/trajectories`")) {
			t.Errorf("agents-shellm shows alice a direct-write sync procedure for a read-only mount:\n%s", doc)
		}
		// bob's sync procedure must actually work against the server.
		if who == "bob" {
			var out, errOut bytes.Buffer
			if code := sh.ExecPipeline("mkdir -p /trajectories/run-1/blobs", &out, &errOut, nil); code != 0 {
				t.Errorf("mkdir exit=%d stderr=%q", code, errOut.String())
			}
			if code := sh.ExecPipeline("tee /trajectories/run-1/trajectory.jsonl >/dev/null", &out, &errOut, strings.NewReader("{}\n")); code != 0 {
				t.Errorf("tee exit=%d stderr=%q", code, errOut.String())
			}
		}
	}
	doc := renderSkillFor(t, skillSession(t, s, "alice"), "agents-shellm-housekeeping")
	if !strings.Contains(doc, "## Check trajectory freshness") {
		t.Errorf("housekeeping omits the trajectory section:\n%s", doc)
	}
}
