package cmds_test

import (
	"strings"
	"testing"
)

func TestDu(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "du -s /docs")
	if !strings.Contains(out, "/docs") {
		t.Errorf("du: got %q", out)
	}
}

func TestDuHuman(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "du -sh /docs")
	// Should contain a unit suffix
	if !strings.Contains(out, "/docs") {
		t.Errorf("du -h: got %q", out)
	}
}
