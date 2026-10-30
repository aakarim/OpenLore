package cmds_test

import (
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "help")
	if !strings.Contains(out, "FILESYSTEM") {
		t.Error("help should show categories")
	}
	if !strings.Contains(out, "jq") {
		t.Error("help should list jq")
	}
	if !strings.Contains(out, "skills") {
		t.Error("help should list skills")
	}
	if !strings.Contains(out, "globs expand; bare patterns use the current directory") {
		t.Error("help should document glob expansion")
	}
	if !strings.Contains(out, "cat <<'EOF' > file") || !strings.Contains(out, "heredoc") {
		t.Error("help should document multi-line heredoc writes")
	}
	for _, command := range []string{"mkdir", "mv", "rm", "replace"} {
		if !strings.Contains(out, command) {
			t.Errorf("help should list %s", command)
		}
	}
}

func TestHelpCommand(t *testing.T) {
	for _, tt := range []struct {
		command string
		want    string
	}{
		{"tee", "  tee <file>                               Pass stdin to stdout and a file\n"},
		{"grep", "  grep [-EFinrRohcvl] <pattern> [path]    Search for pattern in files\n    Basic regex by default; -E extended regex; -F fixed strings; use -E 'a|b' for multiple patterns\n"},
		{"sha256sum", "  md5sum/sha1sum/sha256sum [-c] [file]      Compute checksums\n"},
		{"false", "  true / false                              Exit with 0 / 1\n"},
		{"eval", "  source <file> / eval <cmd>               Execute commands\n"},
		{"pwd", "  pwd                                      Print working directory\n"},
		{"exit", "  exit / quit                              Close session\n"},
		{"quit", "  exit / quit                              Close session\n"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			out, errs, code := execCmd(t, testFS(), "help "+tt.command)
			if code != 0 || errs != "" || out != tt.want {
				t.Fatalf("help %s: code=%d stderr=%q stdout=%q", tt.command, code, errs, out)
			}
		})
	}

	out, errs, code := execCmd(t, testFS(), "help sed")
	if code != 0 || errs != "" || !strings.Contains(out, "Replacement escapes:") || !strings.Contains(out, "sed -i") {
		t.Fatalf("sed help should include continuations and write forms: code=%d stderr=%q stdout=%q", code, errs, out)
	}
	if strings.Contains(out, "awk") || strings.Contains(out, "FILESYSTEM") || strings.Contains(out, "writes are atomic") {
		t.Fatalf("sed help includes unrelated entries: %q", out)
	}

	for _, command := range []string{"nonexistent", "te", "write", "Unquoted", "cmd", "since"} {
		out, errs, code := execCmd(t, testFS(), "help "+command)
		if code != 1 || out != "" || errs != "help: no entry for "+command+"\n" {
			t.Fatalf("unknown entry %s: code=%d stderr=%q stdout=%q", command, code, errs, out)
		}
	}
	out, errs, code = execCmd(t, testFS(), "help tee sed")
	if code != 2 || out != "" || errs != "usage: help [command]\n" {
		t.Fatalf("extra arguments: code=%d stderr=%q stdout=%q", code, errs, out)
	}
}

func TestCommandNotFound(t *testing.T) {
	_, errOut, code := execCmd(t, testFS(), "nonexistent")
	if code != 127 {
		t.Errorf("exit code %d, want 127", code)
	}
	if !strings.Contains(errOut, "command not found") {
		t.Error("should say not found")
	}
}
