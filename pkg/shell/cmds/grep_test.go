package cmds_test

import (
	"strings"
	"testing"
)

func TestPipe(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "cat /docs/notes.txt | grep apple")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("pipe: got %d lines, want 2", len(lines))
	}
}

func TestGrepLineNumbers(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "grep -n apple /docs/notes.txt")
	if !strings.Contains(out, "2:") {
		t.Errorf("grep -n: got %q", out)
	}
}

func TestGrepRejectsUnsupportedOptions(t *testing.T) {
	tests := []struct {
		name    string
		command string
		option  string
	}{
		{name: "short", command: "grep -Q apple /docs/notes.txt", option: "-Q"},
		{name: "grouped short", command: "grep -iQ apple /docs/notes.txt", option: "-Q"},
		{name: "long", command: "grep --help", option: "--help"},
		{name: "multiple pattern flag", command: "printf 'apple\\nbanana\\n' | grep -e apple -e banana", option: "-e"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, stderr, code := execCmd(t, testFS(), tt.command)
			if code != 2 {
				t.Errorf("grep exit code = %d, want 2 for an unsupported option", code)
			}
			if out != "" {
				t.Errorf("grep wrote stdout for an unsupported option: %q", out)
			}
			if !strings.Contains(stderr, "unsupported option \""+tt.option+"\"") {
				t.Errorf("grep error does not name %q: %q", tt.option, stderr)
			}
			if !strings.Contains(stderr, "use -E 'a|b' for multiple patterns") {
				t.Errorf("grep error does not explain the supported alternative: %q", stderr)
			}
		})
	}
}

func TestGrepOnlyMatching(t *testing.T) {
	fs := testFS()

	t.Run("grep -o", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "grep -o 'apple' /docs/notes.txt")
		if code != 0 {
			t.Fatalf("grep -o failed: code=%d", code)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		for _, line := range lines {
			if line != "apple" {
				t.Errorf("grep -o: expected each line to be 'apple', got %q", line)
			}
		}
		if len(lines) != 2 {
			t.Errorf("grep -o: expected 2 matches, got %d", len(lines))
		}
	})

	t.Run("grep -oh recursive", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "grep -roh 'apple' /docs")
		if code != 0 {
			t.Fatalf("grep -roh failed: code=%d", code)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		for _, line := range lines {
			if line != "apple" {
				t.Errorf("grep -roh: expected 'apple', got %q", line)
			}
		}
	})

	t.Run("grep -c count", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "grep -c apple /docs/notes.txt")
		if code != 0 {
			t.Fatalf("grep -c failed: code=%d", code)
		}
		if strings.TrimSpace(out) != "2" {
			t.Errorf("grep -c: expected '2', got %q", strings.TrimSpace(out))
		}
	})

	t.Run("grep -v invert", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "grep -v apple /docs/notes.txt")
		if code != 0 {
			t.Fatalf("grep -v failed: code=%d", code)
		}
		if strings.Contains(out, "apple") {
			t.Errorf("grep -v: should not contain 'apple', got:\n%s", out)
		}
	})

	t.Run("grep -l files with matches", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "grep -rl apple /docs")
		if code != 0 {
			t.Fatalf("grep -rl failed: code=%d", code)
		}
		if !strings.Contains(out, "notes.txt") {
			t.Errorf("grep -rl: should list notes.txt, got:\n%s", out)
		}
	})
}

func TestGrepRegex(t *testing.T) {
	fs := testFS()
	out, _, code := execCmd(t, fs, "grep '^#' /docs/readme.md")
	if code != 0 {
		t.Fatalf("grep regex failed: code=%d", code)
	}
	if !strings.Contains(out, "# Hello World") {
		t.Errorf("grep '^#': should match header, got:\n%s", out)
	}
}

func TestGrepPatternModes(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/patterns.txt", "a\nb\na|b\nFinal (v2)\nFinal v2\n[x](v)\n[x]v\n](v)\n)(v)\nx(v)\nx*y\nxy\n")

	t.Run("basic regexp", func(t *testing.T) {
		out, _, code := execCmd(t, fs, `grep '^a$\|^b$' /docs/patterns.txt`)
		if code != 0 {
			t.Fatalf("grep BRE alternation failed: code=%d", code)
		}
		if out != "a\nb\n" {
			t.Errorf("grep BRE alternation: got %q, want %q", out, "a\nb\n")
		}

		out, _, code = execCmd(t, fs, `grep '^Final (v2)$' /docs/patterns.txt`)
		if code != 0 || out != "Final (v2)\n" {
			t.Errorf("grep BRE literal parentheses: code=%d, got %q", code, out)
		}

		out, _, code = execCmd(t, fs, `grep '^\[x](v)$' /docs/patterns.txt`)
		if code != 0 || out != "[x](v)\n" {
			t.Errorf("grep BRE escaped bracket: code=%d, got %q", code, out)
		}

		out, _, code = execCmd(t, fs, `grep '^[])](v)$' /docs/patterns.txt`)
		if code != 0 || out != "](v)\n)(v)\n" {
			t.Errorf("grep BRE class with literal closing bracket: code=%d, got %q", code, out)
		}

		out, _, code = execCmd(t, fs, `grep '^[^]](v)$' /docs/patterns.txt`)
		if code != 0 || out != ")(v)\nx(v)\n" {
			t.Errorf("grep BRE negated class with literal closing bracket: code=%d, got %q", code, out)
		}
	})

	t.Run("extended regexp", func(t *testing.T) {
		out, _, code := execCmd(t, fs, `grep -E '^Final (v2)$' /docs/patterns.txt`)
		if code != 0 || out != "Final v2\n" {
			t.Errorf("grep -E: code=%d, got %q", code, out)
		}
	})

	t.Run("fixed string", func(t *testing.T) {
		out, _, code := execCmd(t, fs, `grep -F 'x*y' /docs/patterns.txt`)
		if code != 0 || out != "x*y\n" {
			t.Errorf("grep -F: code=%d, got %q", code, out)
		}
	})
}

func TestGrepOnlyMatchingPipe(t *testing.T) {
	fs := testFS()
	out, _, _ := execCmd(t, fs, "grep -roh 'apple' /docs | sort | uniq -c | sort -rn")
	out = strings.TrimSpace(out)
	if out == "" {
		t.Error("grep -roh pipeline returned empty output")
	}
	if !strings.Contains(out, "apple") {
		t.Errorf("grep -roh pipeline should contain 'apple', got: %q", out)
	}
}
