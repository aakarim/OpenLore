package cmds_test

import (
	"strings"
	"testing"
)

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
