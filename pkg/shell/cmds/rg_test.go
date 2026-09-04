package cmds_test

import (
	"strings"
	"testing"
)

func TestRgRecursiveOrderingAndLineNumbers(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddDir("/docs")
	fs.AddDir("/docs/deep")
	fs.AddFile("/docs/z.md", "zero\nNeedle last\n")
	fs.AddFile("/docs/deep/a.md", "needle first\nother\n")

	out, errOut, code := execCmd(t, fs, "rg -in needle /docs")
	if code != 0 || errOut != "" {
		t.Fatalf("rg failed: code=%d stderr=%q", code, errOut)
	}
	want := "/docs/deep/a.md:1:needle first\n/docs/z.md:2:Needle last\n"
	if out != want {
		t.Fatalf("output mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestRgFilesWithMatchesAndDuplicateRoots(t *testing.T) {
	fs := testFS()
	out, errOut, code := execCmd(t, fs, "rg -l apple /docs /docs")
	if code != 0 || errOut != "" {
		t.Fatalf("rg failed: code=%d stderr=%q", code, errOut)
	}
	if out != "/docs/notes.txt\n/docs/sorted1.txt\n" {
		t.Fatalf("got %q", out)
	}
}

func TestRgNoMatchAndErrors(t *testing.T) {
	fs := testFS()

	t.Run("no match", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "rg absent /docs")
		if code != 1 || out != "" || errOut != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	})

	t.Run("invalid regex", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "rg '[' /docs")
		if code != 2 || out != "" || !strings.Contains(errOut, "rg: invalid pattern:") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "rg -x apple /docs")
		if code != 2 || out != "" || errOut != "rg: unknown option: -x\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	})

	t.Run("missing path is atomic", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "rg apple /docs/notes.txt /missing")
		if code != 2 || out != "" {
			t.Fatalf("code=%d stdout=%q", code, out)
		}
	})
}

func TestRgLineEndingsAndLongLine(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddFile("/lines.md", "windows needle\r\n"+strings.Repeat("x", 128<<10)+" needle")

	out, errOut, code := execCmd(t, fs, "rg -n needle /lines.md")
	if code != 0 || errOut != "" {
		t.Fatalf("rg failed: code=%d stderr=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "\r") || !strings.HasPrefix(lines[1], "/lines.md:2:") {
		t.Fatalf("unexpected output shape: first=%q lines=%d", lines[0], len(lines))
	}
}
