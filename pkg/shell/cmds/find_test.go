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

func TestFindRejectsUnsupportedFlag(t *testing.T) {
	for _, flag := range []string{"-maxdepth", "-mindepth", "-exec"} {
		t.Run(flag, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), "find /docs "+flag+" 1 -type f")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if !strings.Contains(errOut, "unsupported flag '"+flag+"'") {
				t.Errorf("stderr = %q, want unsupported flag name", errOut)
			}
			if !strings.Contains(errOut, "tree -L <n>") {
				t.Errorf("stderr = %q, want supported depth-limiting alternative", errOut)
			}
		})
	}
}

func TestFindValidatesOptionArguments(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command string
		wantErr string
	}{
		{name: "missing name pattern", command: "find /docs -name", wantErr: "option requires an argument -- 'name'"},
		{name: "missing type", command: "find /docs -type", wantErr: "option requires an argument -- 'type'"},
		{name: "unsupported type", command: "find /docs -type -maxdepth", wantErr: "unsupported type '-maxdepth'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.command)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr = %q, want %q", errOut, tt.wantErr)
			}
		})
	}
}

func TestFindAllowsDashPrefixedNamePattern(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/-maxdepth", "not a flag\n")

	out, errOut, code := execCmd(t, fs, "find /docs -name -maxdepth")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, errOut)
	}
	if out != "/docs/-maxdepth\n" {
		t.Errorf("stdout = %q, want dash-prefixed filename", out)
	}
}

func TestGlobExpansion(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/.hidden.md", "hidden\n")

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

	t.Run("unquoted wildcard after quoted prefix", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, `DIR=/docs; echo "$DIR"/*.md`)
		if code != 0 {
			t.Fatalf("mixed quoted glob failed: code=%d stderr=%q", code, errOut)
		}
		if out != "/docs/readme.md\n" {
			t.Errorf("unquoted wildcard should expand after a quoted prefix, got %q", out)
		}
	})

	t.Run("escaped wildcard stays literal", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "cd /docs && echo \\*.md")
		if code != 0 {
			t.Fatalf("escaped wildcard failed: code=%d stderr=%q", code, errOut)
		}
		if out != "*.md\n" {
			t.Errorf("escaped wildcard should remain literal, got %q", out)
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
