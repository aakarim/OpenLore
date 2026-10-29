package cmds_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestReplaceLiteral(t *testing.T) {
	for _, tt := range []struct {
		name, before, command, after string
	}{
		{"single", "one\r\ntwo", "replace note.md one three", "three\r\ntwo"},
		{"literal", `a.*[x]\n / & $1`, `replace note.md 'a.*[x]\n' '\t&$1/'`, `\t&$1/ / & $1`},
		{"all", "one one\none", "replace --all note.md one two", "two two\ntwo"},
		{"delete", "one\ntwo\n", "replace note.md 'one\ntwo\n' ''", ""},
		{"multiline", "one\ntwo\nend", "replace note.md 'one\ntwo' 'three\nfour'", "three\nfour\nend"},
		{"flag values", "--all", "replace -- note.md --all -n", "-n"},
		{"same value", "one", "replace note.md one one", "one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fs := newMapFS()
			fs.AddFile("/docs/note.md", tt.before)
			sh := shell.NewShell(fs)
			sh.SetCwd("/docs")
			var out, errOut bytes.Buffer
			code := sh.ExecPipeline(tt.command, &out, &errOut, nil)
			if code != 0 || errOut.Len() != 0 || out.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if got := string(fs.Files["/docs/note.md"].Content); got != tt.after {
				t.Fatalf("content=%q, want %q", got, tt.after)
			}
		})
	}
}

func TestReplaceRejectsWithoutWriting(t *testing.T) {
	for _, command := range []string{
		"replace", "replace -z /note.md one two", "replace /note.md one",
		"replace /note.md one two extra", "replace /note.md '' two",
		"replace /missing one two", "replace /note.md missing two",
		"replace --all /note.md missing two", "replace /note.md one two",
		"replace -n /note.md one two", "replace -n --all /note.md missing two",
	} {
		t.Run(command, func(t *testing.T) {
			fs := newMapFS()
			fs.AddFile("/note.md", "one one")
			out, errOut, code := execCmd(t, fs, command)
			if code != 1 || errOut == "" || out != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errOut)
			}
			if got := string(fs.Files["/note.md"].Content); got != "one one" {
				t.Fatalf("failed command changed content: %q", got)
			}
		})
	}
}

func TestReplaceDryRunDiff(t *testing.T) {
	for _, tt := range []struct {
		before, command, diff string
	}{
		{"head\none\ntail\n", "replace -n /note.md one two", "--- /note.md\n+++ /note.md\n@@ -2,1 +2,1 @@\n-one\n+two\n"},
		{"one", "replace -n /note.md one two", "--- /note.md\n+++ /note.md\n@@ -1,1 +1,1 @@\n-one\n\\ No newline at end of file\n+two\n\\ No newline at end of file\n"},
		{"one\n", "replace -n /note.md 'one\n' ''", "--- /note.md\n+++ /note.md\n@@ -1,1 +0,0 @@\n-one\n"},
		{"one\none\n", "replace --all -n /note.md one two", "--- /note.md\n+++ /note.md\n@@ -1,2 +1,2 @@\n-one\n-one\n+two\n+two\n"},
		{"one", "replace -n /note.md one one", ""},
	} {
		fs := &pendingFS{mapFS: newMapFS(), ref: "must-not-write"}
		fs.AddFile("/note.md", tt.before)
		out, errOut, code := execPending(fs, tt.command)
		if code != 0 || errOut != "" || out != tt.diff {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q; want diff=%q", tt.command, code, out, errOut, tt.diff)
		}
		if fs.lastChange.Write != nil || string(fs.Files["/note.md"].Content) != tt.before {
			t.Fatal("dry run attempted a write")
		}
	}
}

type editRejectFS struct {
	*mapFS
	err error
}

func (f *editRejectFS) WriteFileAtomic(string, []byte, vfs.WriteOpts) (string, error) {
	return "", f.err
}

func TestReplaceWriteGovernance(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/note.md", "one")
	for _, rejection := range []error{vfs.ErrReadOnly, errors.New("admission denied"), &rules.Rejection{Path: "/note.md", Rule: "test-rule"}} {
		_, errOut, code := execPending(&editRejectFS{mapFS: fs, err: rejection}, "replace /note.md one two")
		if code != 1 || !strings.Contains(errOut, rejection.Error()) {
			t.Fatalf("rejection %v: exit=%d stderr=%q", rejection, code, errOut)
		}
	}
	pending := &pendingFS{mapFS: fs, ref: "replace-pending"}
	_, errOut, code := execPending(pending, "replace /note.md one two")
	if code != 0 || !strings.Contains(errOut, "replace-pending") || pending.lastChange.Write == nil || pending.lastChange.Write.Opts.IfMatch == nil || string(pending.lastChange.Write.Bytes) != "two" {
		t.Fatalf("pending: exit=%d stderr=%q change=%+v", code, errOut, pending.lastChange)
	}
	race := &writeRaceFS{mapFS: fs, target: "/note.md"}
	_, errOut, code = execPending(race, "replace /note.md one two")
	if code != 1 || !strings.Contains(errOut, "file changed concurrently") || string(fs.Files["/note.md"].Content) != "concurrent" {
		t.Fatalf("race: exit=%d stderr=%q", code, errOut)
	}
}

func TestReplaceHonorsActionGate(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/note.md", "one")
	sh := shell.NewShell(fs)
	sh.SetAllowedActions(nil)
	for _, command := range []string{"replace /note.md one two", "replace -n /note.md one two", "which replace", "type replace", "command -v replace", "help replace"} {
		var out, errOut bytes.Buffer
		code := sh.ExecPipeline(command, &out, &errOut, nil)
		if code == 0 || string(fs.Files["/note.md"].Content) != "one" || out.Len() != 0 {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q", command, code, out.String(), errOut.String())
		}
	}
}

func TestReplaceDryRunReportsOutputError(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/note.md", "one")
	var errOut bytes.Buffer
	code := shell.NewShell(fs).ExecPipeline("replace -n /note.md one two", errorWriter{err: errors.New("output unavailable")}, &errOut, nil)
	if code != 1 || !strings.Contains(errOut.String(), "output unavailable") || string(fs.Files["/note.md"].Content) != "one" {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}

func TestReplaceHonorsLastWriteWins(t *testing.T) {
	fs := &writeRaceFS{mapFS: newMapFS(), target: "/note.md"}
	fs.AddFile("/note.md", "one")
	sh := shell.NewShell(fs)
	sh.SetConflictPolicyFn(func(string) vfs.WriteConflictPolicy { return vfs.PolicyLastWriteWins })
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("replace /note.md one two", &out, &errOut, nil)
	if code != 0 || string(fs.Files["/note.md"].Content) != "two" {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}
