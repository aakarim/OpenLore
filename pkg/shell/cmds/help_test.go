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
	for _, command := range []string{"mkdir", "mv", "rm"} {
		if !strings.Contains(out, command) {
			t.Errorf("help should list %s", command)
		}
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
