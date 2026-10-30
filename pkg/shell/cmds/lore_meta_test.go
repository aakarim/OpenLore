package cmds_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// The `lore meta` command lives here (shell plumbing); its scanning business
// logic lives in pkg/meta and is tested there. These tests exercise the command
// adapter: arg parsing, cwd scoping, NDJSON emission, and extender wiring.

func metaTreeFS() *mapFS {
	fs := newMapFS()
	fs.AddFile("/docs/orders.md", "---\ntype: Table\ntitle: Orders\ntags:\n  - sales\n  - orders\n---\n# Orders\nbody\n")
	fs.AddFile("/docs/metric.md", "---\ntype: Metric\ntitle: Revenue\n---\nbody\n")
	fs.AddFile("/docs/plain.md", "# No frontmatter here\njust text\n")
	fs.AddFile("/docs/notes.txt", "---\ntype: NotMarkdown\n---\n")
	fs.AddFile("/docs/sub/nested.md", "---\ntype: Note\n---\nnested body\n")
	return fs
}

func runMeta(t *testing.T, sh *shell.Shell, cwd, cmd string) (string, string, int) {
	t.Helper()
	if cwd != "" {
		sh.SetCwd(cwd)
	}
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline(cmd, &out, &errOut, nil)
	return out.String(), errOut.String(), code
}

func parseNDJSON(t *testing.T, s string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid NDJSON %q: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

func TestLoreMetaCommand_NDJSONWithRelativePaths(t *testing.T) {
	out, errOut, code := runMeta(t, shell.NewShell(metaTreeFS()), "/docs", "lore meta")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	recs := parseNDJSON(t, out)
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d:\n%s", len(recs), out)
	}
	if recs[0]["path"] != "metric.md" || recs[2]["path"] != "sub/nested.md" {
		t.Fatalf("paths wrong: %v", recs)
	}
	if strings.Contains(out, "plain.md") || strings.Contains(out, "notes.txt") {
		t.Fatalf("non-qualifying docs leaked:\n%s", out)
	}
}

func TestLoreMetaCommand_PathArgument(t *testing.T) {
	out, _, code := runMeta(t, shell.NewShell(metaTreeFS()), "/", "lore meta docs/sub")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	recs := parseNDJSON(t, out)
	if len(recs) != 1 || recs[0]["path"] != "nested.md" {
		t.Fatalf("path arg scoping wrong: %v", recs)
	}
}

func TestLoreMetaCommand_UnknownFlagErrors(t *testing.T) {
	_, errOut, code := runMeta(t, shell.NewShell(metaTreeFS()), "/docs", "lore meta --json")
	if code != 1 {
		t.Fatalf("unknown flag exit=%d, want 1", code)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Fatalf("stderr missing flag error: %s", errOut)
	}
}

func TestLoreMetaCommand_Help(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		out, errOut, code := runMeta(t, shell.NewShell(metaTreeFS()), "/docs", "lore meta "+flag)
		if code != 0 || errOut != "" {
			t.Fatalf("help: exit=%d stderr=%s", code, errOut)
		}
		for _, term := range []string{"Usage:", "--missing", "--filter", "NDJSON", "set <path>", "unset <path>"} {
			if !strings.Contains(out, term) {
				t.Errorf("help missing %q: %s", term, out)
			}
		}
	}
}

func TestLoreMetaCommand_Missing(t *testing.T) {
	fsys := metaTreeFS()
	fsys.AddFile("/docs/broken.md", "---\ntags: [broken\n---\nbody")
	fsys.AddFile("/docs/empty.md", "---\n---\nbody")
	fsys.AddFile("/docs/null.md", "---\ntype: null\ntags: []\n---\nbody")
	for _, tc := range []struct {
		command string
		paths   string
	}{
		{"lore meta --missing", "broken.md,plain.md"},
		{"lore meta --missing type,tags", "broken.md,empty.md,metric.md,plain.md,sub/nested.md"},
		{"lore meta /docs --missing type,tags", "broken.md,empty.md,metric.md,plain.md,sub/nested.md"},
		{"lore meta --missing=type,tags /docs/sub", "nested.md"},
		{"lore meta /docs/plain.md --missing", "plain.md"},
	} {
		out, errOut, code := runMeta(t, shell.NewShell(fsys), "/docs", tc.command)
		if code != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", tc.command, code, errOut)
		}
		var paths []string
		for _, rec := range parseNDJSON(t, out) {
			paths = append(paths, rec["path"].(string))
			if rec["reason"] == nil {
				t.Errorf("missing reason: %v", rec)
			}
			if rec["path"] == "metric.md" && rec["reason"] == "missing_keys" {
				keys, ok := rec["missing_keys"].([]any)
				if !ok || len(keys) != 1 || keys[0] != "tags" {
					t.Errorf("missing keys: %v", rec)
				}
			}
		}
		if strings.Join(paths, ",") != tc.paths {
			t.Errorf("%s: paths=%v, want %s", tc.command, paths, tc.paths)
		}
		if !strings.Contains(errOut, "with frontmatter") || !strings.Contains(errOut, "without parseable frontmatter") {
			t.Errorf("missing summary: %s", errOut)
		}
	}
}

func TestLoreMetaCommand_SummaryDoesNotChangeNDJSON(t *testing.T) {
	out, errOut, code := runMeta(t, shell.NewShell(metaTreeFS()), "/docs", "lore meta")
	want := "{\"path\":\"metric.md\",\"title\":\"Revenue\",\"type\":\"Metric\"}\n" +
		"{\"path\":\"orders.md\",\"tags\":[\"sales\",\"orders\"],\"title\":\"Orders\",\"type\":\"Table\"}\n" +
		"{\"path\":\"sub/nested.md\",\"type\":\"Note\"}\n"
	if code != 0 || out != want {
		t.Fatalf("default NDJSON changed: code=%d out=%s stderr=%s", code, out, errOut)
	}
	for _, term := range []string{"3 with frontmatter", "1 without parseable frontmatter", "--missing"} {
		if !strings.Contains(errOut, term) {
			t.Errorf("summary missing %q: %s", term, errOut)
		}
	}
}

func TestLoreMetaCommand_RejectsInvalidDiscoveryArguments(t *testing.T) {
	for _, tc := range []struct{ args, message string }{
		{"--missing --filter notes", "cannot be combined"},
		{"--filter notes --missing type", "cannot be combined"},
		{"--missing type,,tags", "non-empty"},
		{"--missing=", "non-empty"},
		{"--missing --missing", "only be specified once"},
		{"/docs /other", "at most one path"},
		{"--filter", "requires a name"},
		{"--filter --missing", "requires a name"},
	} {
		out, errOut, code := runMeta(t, shell.NewShell(metaTreeFS()), "/docs", "lore meta "+tc.args)
		if code != 1 || out != "" || !strings.Contains(errOut, tc.message) {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", tc.args, code, out, errOut)
		}
	}
}

func TestLoreMetaCommand_MissingReportsEncodingFailure(t *testing.T) {
	sh := shell.NewShell(metaTreeFS())
	sh.SetCwd("/docs")
	var errOut bytes.Buffer
	code := sh.ExecPipeline("lore meta --missing", errorWriter{err: errors.New("output unavailable")}, &errOut, nil)
	if code != 1 || !strings.Contains(errOut.String(), "output unavailable") {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
}

func TestLoreMetaCommand_AppliesSessionExtenders(t *testing.T) {
	sh := shell.NewShell(metaTreeFS())
	sh.SetMetaExtenders([]meta.Extender{
		func(absPath string, content []byte, fm map[string]any) map[string]any {
			if fm["type"] == "Metric" {
				return map[string]any{"annotated": true}
			}
			return nil
		},
	})
	out, errOut, code := runMeta(t, sh, "/docs", "lore meta")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	for _, r := range parseNDJSON(t, out) {
		if r["path"] == "metric.md" && r["annotated"] != true {
			t.Fatalf("session extender not applied to metric.md: %v", r)
		}
		if r["path"] != "metric.md" {
			if _, ok := r["annotated"]; ok {
				t.Fatalf("extender leaked onto %v", r["path"])
			}
		}
	}
}

type canonicalMetaFS struct{ *mapFS }

func (f canonicalMetaFS) CanonicalPath(p string) string {
	p = vfs.CleanPath(p)
	if p == "/alias" || strings.HasPrefix(p, "/alias/") {
		return "/docs" + strings.TrimPrefix(p, "/alias")
	}
	return p
}

func TestLoreMetaFilterAliasesNarrowingAndAbsoluteCanonicalPaths(t *testing.T) {
	fs := canonicalMetaFS{metaTreeFS()}
	sh := shell.NewShell(fs)
	sh.SetMetaFilters([]meta.Filter{{
		Name:          "agent_skills",
		Aliases:       []string{"agent_skill", "skills", "skill"},
		Roots:         []string{"/docs"},
		AbsolutePaths: true,
		Selector: func(abs string, r meta.Record) bool {
			return r.Fields["type"] == "Note"
		},
	}})
	for _, alias := range []string{"agent_skills", "agent_skill", "skills", "skill"} {
		out, errOut, code := runMeta(t, sh, "/", "lore meta --filter "+alias+" /alias/sub")
		if code != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", alias, code, errOut)
		}
		recs := parseNDJSON(t, out)
		if len(recs) != 1 || recs[0]["path"] != "/docs/sub/nested.md" {
			t.Fatalf("%s: canonical narrowed results = %v", alias, recs)
		}
	}
}

func TestLoreMetaFilterExactFilePath(t *testing.T) {
	sh := shell.NewShell(metaTreeFS())
	sh.SetMetaFilters([]meta.Filter{{
		Name:          "notes",
		Roots:         []string{"/docs"},
		AbsolutePaths: true,
		Selector:      func(abs string, _ meta.Record) bool { return abs == "/docs/metric.md" },
	}})
	out, errOut, code := runMeta(t, sh, "/", "lore meta --filter notes /docs/metric.md")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	recs := parseNDJSON(t, out)
	if len(recs) != 1 || recs[0]["path"] != "/docs/metric.md" {
		t.Fatalf("exact-file filtered result = %v", recs)
	}
}

type statErrorFS struct {
	*mapFS
	target string
	err    error
}

func (f statErrorFS) Stat(p string) (*vfs.FileInfo, error) {
	if vfs.CleanPath(p) == f.target {
		return nil, f.err
	}
	return f.mapFS.Stat(p)
}

func TestLoreMetaFilterSkipsMissingRoot(t *testing.T) {
	fsys := statErrorFS{mapFS: metaTreeFS(), target: "/missing", err: vfs.ErrNotFound("/missing")}
	sh := shell.NewShell(fsys)
	sh.SetMetaFilters([]meta.Filter{{
		Name:          "all",
		Roots:         []string{"/missing", "/docs"},
		AbsolutePaths: true,
		Selector:      func(string, meta.Record) bool { return true },
	}})

	out, errOut, code := runMeta(t, sh, "/", "lore meta --filter all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if errOut != "" {
		t.Fatalf("missing root should not write stderr, got %q", errOut)
	}
	if recs := parseNDJSON(t, out); len(recs) != 3 {
		t.Fatalf("existing root records = %v, want 3", recs)
	}
}

func TestLoreMetaFilterReportsNonNotFoundRootError(t *testing.T) {
	statErr := errors.New("storage unavailable")
	fsys := statErrorFS{mapFS: metaTreeFS(), target: "/broken", err: statErr}
	sh := shell.NewShell(fsys)
	sh.SetMetaFilters([]meta.Filter{{
		Name:     "all",
		Roots:    []string{"/broken", "/docs"},
		Selector: func(string, meta.Record) bool { return true },
	}})

	_, errOut, code := runMeta(t, sh, "/", "lore meta --filter all")
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if !strings.Contains(errOut, statErr.Error()) {
		t.Fatalf("stderr missing root error: %q", errOut)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLoreMetaFilterReportsEncodingFailure(t *testing.T) {
	sh := shell.NewShell(metaTreeFS())
	sh.SetMetaFilters([]meta.Filter{{
		Name:     "all",
		Roots:    []string{"/docs"},
		Selector: func(string, meta.Record) bool { return true },
	}})
	var errOut bytes.Buffer
	writeErr := errors.New("output unavailable")
	code := sh.ExecPipeline("lore meta --filter all", errorWriter{err: writeErr}, &errOut, nil)
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if !strings.Contains(errOut.String(), writeErr.Error()) {
		t.Fatalf("stderr missing encoding error: %q", errOut.String())
	}
}

func TestLore_UsageListsMeta(t *testing.T) {
	out, _, _ := runMeta(t, shell.NewShell(metaTreeFS()), "/", "lore")
	if !strings.Contains(out, "meta") || !strings.Contains(out, "docsets") {
		t.Fatalf("lore usage should list meta and docsets:\n%s", out)
	}
}

// TestRegisterLoreSub_PluginCanAddSubcommand verifies the generic dispatcher
// lets a plugin register a new subcommand.
func TestRegisterLoreSub_PluginCanAddSubcommand(t *testing.T) {
	cmds.RegisterLoreSub(cmds.LoreSub{
		Name:    "ping",
		Summary: "test subcommand",
		Run: func(ctx cmds.CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
			io.WriteString(w, "pong\n")
			return 0
		},
	})
	t.Cleanup(func() { cmds.DeleteLoreSubForTest("ping") })

	sh := shell.NewShell(testFS())
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("lore ping", &out, &errOut, nil)
	if code != 0 {
		t.Fatalf("registered subcommand exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "pong") {
		t.Fatalf("subcommand output = %q, want pong", out.String())
	}
}
