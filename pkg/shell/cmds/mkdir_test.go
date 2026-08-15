package cmds_test

import (
	"strings"
	"testing"
)

func TestMkdirCreatesDir(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "mkdir /docs/newdir")
	if code != 0 {
		t.Fatalf("mkdir: code=%d err=%s", code, errOut)
	}
	if fi, ok := fs.Files["/docs/newdir"]; !ok || !fi.Dir {
		t.Fatalf("expected /docs/newdir to be a directory")
	}
}

func TestMkdirMissingParentFails(t *testing.T) {
	fs := testFS()
	_, _, code := execCmd(t, fs, "mkdir /docs/a/b/c")
	if code == 0 {
		t.Fatalf("expected failure creating nested dir without -p")
	}
	if _, ok := fs.Files["/docs/a/b/c"]; ok {
		t.Fatalf("should not have created nested dir")
	}
}

func TestMkdirParents(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "mkdir -p /docs/a/b/c")
	if code != 0 {
		t.Fatalf("mkdir -p: code=%d err=%s", code, errOut)
	}
	for _, p := range []string{"/docs/a", "/docs/a/b", "/docs/a/b/c"} {
		if fi, ok := fs.Files[p]; !ok || !fi.Dir {
			t.Fatalf("expected %s to be a directory", p)
		}
	}
}

func TestMkdirParentsExistingOK(t *testing.T) {
	fs := testFS()
	// /docs/sub already exists — mkdir -p must not error.
	assertExitCode(t, fs, "mkdir -p /docs/sub", 0)
}

func TestMkdirMultiple(t *testing.T) {
	fs := testFS()
	assertExitCode(t, fs, "mkdir /docs/x /docs/y", 0)
	if _, ok := fs.Files["/docs/x"]; !ok {
		t.Fatal("expected /docs/x")
	}
	if _, ok := fs.Files["/docs/y"]; !ok {
		t.Fatal("expected /docs/y")
	}
}

func TestMkdirUnknownFlag(t *testing.T) {
	assertExitCode(t, testFS(), "mkdir -z /docs/z", 1)
}

func TestMkdirMissingOperand(t *testing.T) {
	assertExitCode(t, testFS(), "mkdir", 1)
}

func TestMkdirReadOnly(t *testing.T) {
	fs := testFS()
	fs.SetReadonly()
	_, errOut, code := execCmd(t, fs, "mkdir /docs/nope")
	if code != 1 || !strings.Contains(errOut, "read-only") {
		t.Fatalf("expected read-only error, got code=%d err=%q", code, errOut)
	}
}
