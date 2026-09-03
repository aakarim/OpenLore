package cmds_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type mvBatchFS struct {
	*mapFS
	admitted []vfs.ChangeSet
	reject   error
}

func (f *mvBatchFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	f.admitted = append(f.admitted, cs)
	if f.reject != nil {
		return f.reject
	}
	if err := vfs.ValidateChangeSet(cs); err != nil {
		return err
	}
	for _, change := range cs.Changes {
		switch change.Action {
		case vfs.ChangeActionWrite:
			if _, err := f.mapFS.WriteFileAtomic(change.Target, change.Write.Bytes, change.Write.Opts); err != nil {
				return err
			}
		case vfs.ChangeActionRemoveAll:
			if err := f.mapFS.RemoveAll(change.Target, change.RemoveAll.Opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func execMvBatch(fs *mvBatchFS, cmd string) (string, int) {
	sh := shell.NewShell(fs)
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline(cmd, &out, &errOut, nil)
	return errOut.String(), code
}

func TestMvSubmitsOneAtomicBatch(t *testing.T) {
	fs := &mvBatchFS{mapFS: testFS()}
	errOut, code := execMvBatch(fs, "mv /docs/notes.txt /docs/moved.txt")
	if code != 0 {
		t.Fatalf("mv: code=%d err=%s", code, errOut)
	}
	if len(fs.admitted) != 1 || len(fs.admitted[0].Changes) != 2 {
		t.Fatalf("admitted changesets = %#v, want one two-leaf batch", fs.admitted)
	}
	changes := fs.admitted[0].Changes
	if changes[0].Action != vfs.ChangeActionWrite || changes[0].Target != "/docs/moved.txt" || changes[0].Write == nil {
		t.Fatalf("first change = %#v, want destination write", changes[0])
	}
	if changes[1].Action != vfs.ChangeActionRemoveAll || changes[1].Target != "/docs/notes.txt" || changes[1].RemoveAll == nil || changes[1].RemoveAll.Opts.Expected == nil {
		t.Fatalf("second change = %#v, want snapshot-guarded source removal", changes[1])
	}
	if _, ok := fs.Files["/docs/notes.txt"]; ok {
		t.Fatal("source should be removed")
	}
	if _, ok := fs.Files["/docs/moved.txt"]; !ok {
		t.Fatal("destination should be written")
	}
}

func TestMvRejectedBatchHasNoPartialOperation(t *testing.T) {
	fs := &mvBatchFS{mapFS: testFS(), reject: errors.New("batch rejected")}
	errOut, code := execMvBatch(fs, "mv /docs/notes.txt /docs/moved.txt")
	if code != 1 || !strings.Contains(errOut, "batch rejected") {
		t.Fatalf("mv: code=%d err=%q", code, errOut)
	}
	if len(fs.admitted) != 1 {
		t.Fatalf("admitted %d changesets, want one", len(fs.admitted))
	}
	if _, ok := fs.Files["/docs/notes.txt"]; !ok {
		t.Fatal("rejected batch removed source")
	}
	if _, ok := fs.Files["/docs/moved.txt"]; ok {
		t.Fatal("rejected batch wrote destination")
	}
}

func TestMvFile(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "mv /docs/notes.txt /docs/moved.txt")
	if code != 0 {
		t.Fatalf("mv: code=%d err=%s", code, errOut)
	}
	if _, ok := fs.Files["/docs/notes.txt"]; ok {
		t.Fatal("source should be removed")
	}
	if got := string(fs.Files["/docs/moved.txt"].Content); !strings.Contains(got, "apple") {
		t.Fatalf("destination content = %q", got)
	}
}

func TestMvFileIntoDirectory(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "mv /docs/notes.txt /docs/sub")
	if code != 0 {
		t.Fatalf("mv into directory: code=%d err=%s", code, errOut)
	}
	if _, ok := fs.Files["/docs/sub/notes.txt"]; !ok {
		t.Fatal("destination file was not created in directory")
	}
}

func TestMvMissingSource(t *testing.T) {
	_, errOut, code := execCmd(t, testFS(), "mv /docs/missing.txt /docs/moved.txt")
	if code != 1 || !strings.Contains(errOut, "No such file or directory") {
		t.Fatalf("missing source: code=%d err=%q", code, errOut)
	}
}

func TestMvDirectoryRejected(t *testing.T) {
	_, errOut, code := execCmd(t, testFS(), "mv /docs/sub /docs/moved")
	if code != 1 || !strings.Contains(errOut, "directory moves are not supported") {
		t.Fatalf("directory move: code=%d err=%q", code, errOut)
	}
}

func TestMvReadOnly(t *testing.T) {
	fs := testFS()
	fs.SetReadonly()
	_, errOut, code := execCmd(t, fs, "mv /docs/notes.txt /docs/moved.txt")
	if code != 1 || !strings.Contains(errOut, "read-only") {
		t.Fatalf("read-only move: code=%d err=%q", code, errOut)
	}
	if _, ok := fs.Files["/docs/notes.txt"]; !ok {
		t.Fatal("source should remain after failed destination write")
	}
}

func TestMvUsageErrors(t *testing.T) {
	assertExitCode(t, testFS(), "mv /docs/notes.txt", 1)
	assertExitCode(t, testFS(), "mv -z /docs/notes.txt /docs/moved.txt", 1)
}
