package cmds_test

import (
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "find /docs -name '*.txt'")
	if !strings.Contains(out, "notes.txt") {
		t.Error("find should find notes.txt")
	}
}

func TestFindTypeDir(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "find /docs -type d")
	if !strings.Contains(out, "sub") {
		t.Errorf("find -type d: got %q", out)
	}
}

func TestGlobExpansion(t *testing.T) {
	fs := testFS()

	t.Run("ls with glob", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "ls /docs/*.md")
		if code != 0 {
			t.Fatalf("ls /docs/*.md failed: code=%d", code)
		}
		if !strings.Contains(out, "readme.md") {
			t.Errorf("glob /docs/*.md should match readme.md, got:\n%s", out)
		}
	})

	t.Run("bare relative glob", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "cd /docs && echo *.md")
		if code != 0 {
			t.Fatalf("bare relative glob failed: code=%d stderr=%q", code, errOut)
		}
		if out != "/docs/readme.md\n" {
			t.Errorf("bare relative glob should expand against cwd, got %q", out)
		}
	})

	t.Run("glob does not expand in quotes", func(t *testing.T) {
		// find -name '*.md' - the *.md should NOT be expanded
		out, _, code := execCmd(t, fs, "find /docs -name '*.md'")
		if code != 0 {
			t.Fatalf("find with quoted glob failed: code=%d", code)
		}
		if !strings.Contains(out, "readme.md") {
			t.Errorf("find -name '*.md' should find readme.md, got:\n%s", out)
		}
	})
}
