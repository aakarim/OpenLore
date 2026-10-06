package cmds_test

import (
	"strings"
	"testing"
)

func TestLs(t *testing.T) {
	fs := testFS()
	out, _, code := execCmd(t, fs, "ls /docs")
	if code != 0 {
		t.Fatalf("ls failed with code %d", code)
	}
	if !strings.Contains(out, "readme.md") {
		t.Error("ls /docs should list readme.md")
	}
}

func TestLsFlags(t *testing.T) {
	fs := testFS()
	for _, cmd := range []string{"ls -1 /docs", "ls -l1 /docs", "ls -la /docs"} {
		out, _, code := execCmd(t, fs, cmd)
		if code != 0 || !strings.Contains(out, "readme.md") {
			t.Errorf("%s: code %d, out %q", cmd, code, out)
		}
	}
	// Flags advertised nowhere must fail loudly instead of being treated as paths.
	_, errOut, code := execCmd(t, fs, "ls -t /docs")
	if code == 0 || !strings.Contains(errOut, "invalid option -- 't'") {
		t.Errorf("ls -t: code %d, stderr %q", code, errOut)
	}
}
