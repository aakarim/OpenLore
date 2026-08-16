package cmds_test

import (
	"strings"
	"testing"
)

func TestSed(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "sed 's/apple/orange/' /docs/notes.txt")
	if !strings.Contains(out, "orange") {
		t.Error("sed should replace apple with orange")
	}
}

func TestSedPipe(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "cat /docs/notes.txt | sed 's/apple/APPLE/g'")
	if strings.Count(out, "APPLE") != 2 {
		t.Errorf("sed pipe: expected 2 APPLEs, got %q", out)
	}
}

func TestSedSubstitutionWithSpaces(t *testing.T) {
	fs := testFS()
	out, errOut, code := execCmd(t, fs, "cat /docs/readme.md | sed 's/Hello/Goodbye World/g'")
	if code != 0 {
		t.Fatalf("sed substitution with spaces failed: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "Goodbye World") {
		t.Errorf("sed: should contain 'Goodbye World', got:\n%s", out)
	}
}

func TestSedSubstitutionGlobal(t *testing.T) {
	fs := testFS()
	out, _, code := execCmd(t, fs, "cat /docs/notes.txt | sed 's/apple/APPLE/g'")
	if code != 0 {
		t.Fatalf("sed s///g failed: code=%d", code)
	}
	if strings.Contains(out, "apple") {
		t.Errorf("sed s///g: should have replaced all 'apple', got:\n%s", out)
	}
	if !strings.Contains(out, "APPLE") {
		t.Errorf("sed s///g: should contain 'APPLE', got:\n%s", out)
	}
}

func TestSedAppendMultilineInPlace(t *testing.T) {
	fs := testFS()
	command := "sed -i '/This is/a\\\n* idea, with context (important); keep it\n* another idea' /docs/readme.md"
	_, errOut, code := execCmd(t, fs, command)
	if code != 0 {
		t.Fatalf("sed multiline append failed: code=%d stderr=%s", code, errOut)
	}
	if strings.Contains(errOut, "command not found") {
		t.Fatalf("append text leaked as shell commands:\n%s", errOut)
	}

	content, err := fs.ReadFile("/docs/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Hello World\nThis is a test file.\n* idea, with context (important); keep it\n* another idea\nLine 3\nLine 4\nLine 5\n"
	if string(content) != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}
