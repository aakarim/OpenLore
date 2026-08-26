package cmds_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// pendingFS wraps mapFS so every mutation is parked as a pending change (as an
// admission middleware would), letting us assert the write verbs treat
// *vfs.PendingChangeError as exit-0 + a Ref line.
type pendingFS struct {
	*mapFS
	ref        string
	lastChange vfs.ChangeSet
}

func (p *pendingFS) WriteFileAtomic(path string, data []byte, opts vfs.WriteOpts) (string, error) {
	p.lastChange = vfs.ChangeSet{
		Target: path,
		Action: vfs.ChangeActionWrite,
		Write:  &vfs.WriteChange{Bytes: append([]byte(nil), data...), Opts: opts},
	}
	return "", &vfs.PendingChangeError{Ref: p.ref, ChangeSet: p.lastChange}
}
func (p *pendingFS) Remove(path string) error {
	p.lastChange = vfs.ChangeSet{Target: path, Action: vfs.ChangeActionRemove}
	return &vfs.PendingChangeError{Ref: p.ref, ChangeSet: p.lastChange}
}
func (p *pendingFS) RemoveAll(path string, opts vfs.RemoveOpts) error {
	p.lastChange = vfs.ChangeSet{
		Target:    path,
		Action:    vfs.ChangeActionRemoveAll,
		RemoveAll: &vfs.RemoveAllChange{Opts: opts},
	}
	return &vfs.PendingChangeError{Ref: p.ref, ChangeSet: p.lastChange}
}

func execPending(fs vfs.WritableFS, cmd string) (string, string, int) {
	sh := shell.NewShell(fs)
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline(cmd, &out, &errOut, nil)
	return out.String(), errOut.String(), code
}

func TestWritePendingChangeIsExitZeroWithRef(t *testing.T) {
	fs := &pendingFS{mapFS: testFS(), ref: "chg-42"}

	// A redirect write parked as pending → exit 0, Ref surfaced on stderr.
	out, errOut, code := execPending(fs, "echo hi > /docs/new.txt")
	if code != 0 {
		t.Fatalf("write: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "chg-42") || !strings.Contains(errOut, "pending") {
		t.Fatalf("write pending line missing Ref: %q", errOut)
	}
	if fs.lastChange.Write == nil || !fs.lastChange.Write.Opts.IfNoneMatch {
		t.Fatalf("pending write lost create-only precondition: %+v", fs.lastChange)
	}
}

func TestPendingFSRetainsIfMatch(t *testing.T) {
	fs := &pendingFS{mapFS: testFS(), ref: "chg-43"}
	base := fs.Files["/docs/notes.txt"].Content
	sum := sha256.Sum256(base)
	want := hex.EncodeToString(sum[:])

	_, errOut, code := execPending(fs, "echo replacement > /docs/notes.txt")
	if code != 0 {
		t.Fatalf("write: code=%d err=%q", code, errOut)
	}
	if fs.lastChange.Write == nil || fs.lastChange.Write.Opts.IfMatch == nil || *fs.lastChange.Write.Opts.IfMatch != want {
		t.Fatalf("pending write lost IfMatch=%s: %+v", want, fs.lastChange)
	}
}

func TestPendingFSRetainsRemoveSnapshot(t *testing.T) {
	fs := &pendingFS{mapFS: testFS(), ref: "chg-44"}
	expected := fs.snapshot("/docs/sub")

	_ = fs.RemoveAll("/docs/sub", vfs.RemoveOpts{Expected: expected})
	if fs.lastChange.RemoveAll == nil || fs.lastChange.RemoveAll.Opts.Expected != expected {
		t.Fatalf("pending remove lost expected snapshot: %+v", fs.lastChange)
	}
}

func TestRmPendingChangeIsExitZeroWithRef(t *testing.T) {
	fs := &pendingFS{mapFS: testFS(), ref: "chg-99"}

	// rm of an existing file parked as pending → exit 0, Ref surfaced on stdout.
	out, errOut, code := execPending(fs, "rm /docs/notes.txt")
	if code != 0 {
		t.Fatalf("rm: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "chg-99") || !strings.Contains(out, "pending") {
		t.Fatalf("rm pending line missing Ref: %q", out)
	}
}
