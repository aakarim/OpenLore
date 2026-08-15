package cmds_test

import (
	"strings"
	"testing"
)

func TestRmFile(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "rm /docs/notes.txt")
	if code != 0 {
		t.Fatalf("rm: code=%d err=%s", code, errOut)
	}
	if _, ok := fs.Files["/docs/notes.txt"]; ok {
		t.Fatal("expected /docs/notes.txt to be removed")
	}
}

func TestRmDirWithoutRecursiveFails(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "rm /docs/sub")
	if code == 0 {
		t.Fatal("expected error removing a directory without -r")
	}
	if !strings.Contains(errOut, "is a directory") {
		t.Fatalf("expected 'is a directory', got %q", errOut)
	}
	if _, ok := fs.Files["/docs/sub"]; !ok {
		t.Fatal("directory should still exist")
	}
}

func TestRmRecursive(t *testing.T) {
	fs := testFS()
	_, errOut, code := execCmd(t, fs, "rm -r /docs/sub")
	if code != 0 {
		t.Fatalf("rm -r: code=%d err=%s", code, errOut)
	}
	if _, ok := fs.Files["/docs/sub"]; ok {
		t.Fatal("expected /docs/sub removed")
	}
	if _, ok := fs.Files["/docs/sub/file.txt"]; ok {
		t.Fatal("expected /docs/sub/file.txt removed")
	}
}

func TestRmBundledRf(t *testing.T) {
	fs := testFS()
	assertExitCode(t, fs, "rm -rf /docs/sub", 0)
	if _, ok := fs.Files["/docs/sub"]; ok {
		t.Fatal("expected /docs/sub removed")
	}
}

func TestRmMissingNoForce(t *testing.T) {
	assertExitCode(t, testFS(), "rm /docs/nope.txt", 1)
}

func TestRmMissingForce(t *testing.T) {
	assertExitCode(t, testFS(), "rm -f /docs/nope.txt", 0)
}

func TestRmForceMissingOperand(t *testing.T) {
	assertExitCode(t, testFS(), "rm -f", 0)
}

func TestRmMissingOperand(t *testing.T) {
	assertExitCode(t, testFS(), "rm", 1)
}

func TestRmUnknownFlag(t *testing.T) {
	assertExitCode(t, testFS(), "rm -z /docs/notes.txt", 1)
}

func TestRmMultipleIndependent(t *testing.T) {
	fs := testFS()
	// One exists, one missing (no -f): exit 1, but the existing file is still
	// removed because operands are independent.
	_, _, code := execCmd(t, fs, "rm /docs/notes.txt /docs/missing.txt")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if _, ok := fs.Files["/docs/notes.txt"]; ok {
		t.Fatal("existing file should have been removed")
	}
}

func TestRmReadOnly(t *testing.T) {
	fs := testFS()
	fs.SetReadonly()
	_, errOut, code := execCmd(t, fs, "rm /docs/notes.txt")
	if code != 1 || !strings.Contains(errOut, "read-only") {
		t.Fatalf("expected read-only error, got code=%d err=%q", code, errOut)
	}
}
