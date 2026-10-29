package cmds_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestLoreMetaSetStructuralAndPreservesBody(t *testing.T) {
	const body = "\r\n# title\r\nstatus: old\n---\n\x00raw bytes\xff"
	fs := newMapFS()
	fs.AddFile("/docs/note.md", "---\r\n# retain this comment\r\nstatus: old\r\nnested:\r\n  status: old\r\ntags: [one, two]\r\n---\r\n"+body)
	sh := shell.NewShell(fs)
	sh.SetCwd("/docs")
	for _, command := range []string{
		"lore meta set note.md status=new",
		"lore meta set note.md 'tags=[three, four]'",
		"lore meta set note.md 'details={enabled: true, count: 3}'",
		"lore meta set note.md 'url=https://example.test/?a=b=c'",
		"lore meta set note.md empty=",
		"lore meta set note.md 'literal.key=value'",
	} {
		_, errOut, code := runMeta(t, sh, "", command)
		if code != 0 || errOut != "" {
			t.Fatalf("%s: exit=%d stderr=%q", command, code, errOut)
		}
	}
	content := fs.Files["/docs/note.md"].Content
	fields, gotBody, found, err := okf.ParseFrontmatter(content)
	if err != nil || !found || !bytes.Equal(gotBody, []byte(body)) {
		t.Fatalf("frontmatter=%v body=%q found=%v err=%v", fields, gotBody, found, err)
	}
	if fields["status"] != "new" || fields["empty"] != "" || fields["literal.key"] != "value" || fields["url"] != "https://example.test/?a=b=c" {
		t.Fatalf("fields=%v", fields)
	}
	if fields["nested"].(map[string]any)["status"] != "old" || fields["details"].(map[string]any)["enabled"] != true || fields["details"].(map[string]any)["count"] != 3 {
		t.Fatalf("nested fields=%v", fields)
	}
	if tags := fields["tags"].([]any); len(tags) != 2 || tags[0] != "three" || tags[1] != "four" {
		t.Fatalf("tags=%v", tags)
	}
	if !bytes.Contains(content, []byte("# retain this comment")) {
		t.Fatal("unrelated YAML comment was lost")
	}
}

func TestLoreMetaSetCreatesFrontmatter(t *testing.T) {
	for _, before := range []string{"", "# body\r\nno final newline", "---\n---\nbody", "---\nnull\n---\nbody"} {
		fs := newMapFS()
		fs.AddFile("/note.md", before)
		_, errOut, code := execCmd(t, fs, "lore meta set /note.md 'title=hello world'")
		if code != 0 {
			t.Fatalf("before=%q: exit=%d stderr=%q", before, code, errOut)
		}
		fields, body, found, err := okf.ParseFrontmatter(fs.Files["/note.md"].Content)
		wantBody := before
		if _, originalBody, originalFM := okf.SplitFrontmatter([]byte(before)); originalFM {
			wantBody = string(originalBody)
		}
		if err != nil || !found || fields["title"] != "hello world" || string(body) != wantBody {
			t.Fatalf("fields=%v body=%q want=%q err=%v", fields, body, wantBody, err)
		}
	}
}

func TestLoreMetaUnsetStructural(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/note.md", "---\nstatus: old\nnested: {status: keep}\n---\nstatus: body\r\n")
	_, errOut, code := execCmd(t, fs, "lore meta unset /note.md status")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	fields, body, _, err := okf.ParseFrontmatter(fs.Files["/note.md"].Content)
	if _, present := fields["status"]; present || err != nil || string(body) != "status: body\r\n" || fields["nested"].(map[string]any)["status"] != "keep" {
		t.Fatalf("fields=%v body=%q err=%v", fields, body, err)
	}
	for _, before := range []string{"body\r\n", "---\nother: keep\n---\nbody"} {
		pending := &pendingFS{mapFS: newMapFS(), ref: "must-not-write"}
		pending.AddFile("/note.md", before)
		_, errOut, code := execPending(pending, "lore meta unset /note.md absent")
		if code != 0 || errOut != "" || pending.lastChange.Write != nil || string(pending.Files["/note.md"].Content) != before {
			t.Fatalf("absent key: exit=%d stderr=%q change=%+v", code, errOut, pending.lastChange)
		}
	}
}

func TestLoreMetaEditRejectsWithoutWriting(t *testing.T) {
	for _, tt := range []struct{ before, command string }{
		{"body", "lore meta set /note.md"},
		{"body", "lore meta unset /note.md"},
		{"body", "lore meta set /note.md key"},
		{"body", "lore meta set /note.md '=value'"},
		{"body", "lore meta unset /note.md ''"},
		{"body", "lore meta set /note.md 'key=[broken'"},
		{"body", "lore meta set /note.md 'key=one\n---\ntwo'"},
		{"body", "lore meta set /missing key=value"},
		{"---\nkey: [broken\n---\nbody", "lore meta set /note.md key=value"},
		{"---\nkey: one\nkey: two\n---\nbody", "lore meta unset /note.md key"},
		{"---\n- list\n---\nbody", "lore meta set /note.md key=value"},
		{"---\nunclosed", "lore meta set /note.md key=value"},
		{"---\nkey: &value old\nother: *value\n---\nbody", "lore meta unset /note.md key"},
	} {
		fs := newMapFS()
		fs.AddFile("/note.md", tt.before)
		out, errOut, code := execCmd(t, fs, tt.command)
		if code != 1 || out != "" || errOut == "" || string(fs.Files["/note.md"].Content) != tt.before {
			t.Fatalf("%s (before=%q): exit=%d stdout=%q stderr=%q content=%q", tt.command, tt.before, code, out, errOut, fs.Files["/note.md"].Content)
		}
	}
}

func TestLoreMetaEditWriteGovernance(t *testing.T) {
	for _, command := range []string{"lore meta set /note.md status=new", "lore meta unset /note.md status"} {
		fs := newMapFS()
		fs.AddFile("/note.md", "---\nstatus: old\n---\nbody")
		_, errOut, code := execPending(&editRejectFS{mapFS: fs, err: vfs.ErrReadOnly}, command)
		if code != 1 || !strings.Contains(errOut, "read-only") {
			t.Fatalf("read-only: exit=%d stderr=%q", code, errOut)
		}
		pending := &pendingFS{mapFS: fs, ref: "meta-pending"}
		_, errOut, code = execPending(pending, command)
		if code != 0 || !strings.Contains(errOut, "meta-pending") || pending.lastChange.Write == nil || pending.lastChange.Write.Opts.IfMatch == nil {
			t.Fatalf("pending: exit=%d stderr=%q change=%+v", code, errOut, pending.lastChange)
		}
		race := &writeRaceFS{mapFS: fs, target: "/note.md"}
		_, errOut, code = execPending(race, command)
		if code != 1 || !strings.Contains(errOut, "file changed concurrently") || string(fs.Files["/note.md"].Content) != "concurrent" {
			t.Fatalf("race: exit=%d stderr=%q", code, errOut)
		}
	}
}

func TestLoreMetaEditHonorsActionGate(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/note.md", "body")
	sh := shell.NewShell(fs)
	sh.SetAllowedActions(nil)
	for _, command := range []string{"lore meta set /note.md key=value", "lore meta unset /note.md key", "lore meta set", "lore meta unset"} {
		_, errOut, code := runMeta(t, sh, "", command)
		if code != 127 || !strings.Contains(errOut, "command not found") || string(fs.Files["/note.md"].Content) != "body" {
			t.Fatalf("%s: exit=%d stderr=%q", command, code, errOut)
		}
	}
	_, errOut, code := runMeta(t, sh, "", "lore meta /note.md")
	if code != 0 {
		t.Fatalf("metadata read denied: exit=%d stderr=%q", code, errOut)
	}
}

func TestLoreMetaEditPreservesAnchors(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/note.md", "---\nstatus: &status old\nother: *status\n---\nbody")
	_, errOut, code := execCmd(t, fs, "lore meta set /note.md status=new")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	fields, _, _, err := okf.ParseFrontmatter(fs.Files["/note.md"].Content)
	if err != nil || fields["status"] != "new" || fields["other"] != "new" {
		t.Fatalf("fields=%v err=%v", fields, err)
	}
}

func TestLoreMetaEditHonorsLastWriteWins(t *testing.T) {
	fs := &writeRaceFS{mapFS: newMapFS(), target: "/note.md"}
	fs.AddFile("/note.md", "body")
	sh := shell.NewShell(fs)
	sh.SetConflictPolicyFn(func(string) vfs.WriteConflictPolicy { return vfs.PolicyLastWriteWins })
	_, errOut, code := runMeta(t, sh, "", "lore meta set /note.md key=value")
	fields, body, _, err := okf.ParseFrontmatter(fs.Files["/note.md"].Content)
	if code != 0 || err != nil || fields["key"] != "value" || string(body) != "body" {
		t.Fatalf("exit=%d stderr=%q fields=%v body=%q err=%v", code, errOut, fields, body, err)
	}
}
