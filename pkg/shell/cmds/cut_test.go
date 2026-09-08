package cmds_test

import (
	"strings"
	"testing"
)

func TestCut(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "cut -d , -f 1 /docs/data.csv")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[1] != "alice" {
		t.Errorf("cut: got %q, want 'alice'", lines[1])
	}
}

func TestCutMultipleFields(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "cut -d , -f 1,3 /docs/data.csv")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "name,city" {
		t.Errorf("cut -f 1,3: got %q", lines[0])
	}
}

func TestCutAttachedOptionValues(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"echo hello-world | cut -c1-5", "hello\n"},
		{"echo hello-world | cut -d- -f1", "hello\n"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.command)
			if code != 0 || errOut != "" || out != tt.want {
				t.Errorf("got stdout %q, stderr %q, exit %d; want stdout %q, empty stderr, exit 0", out, errOut, code, tt.want)
			}
		})
	}
}

func TestCutDoesNotCombineCharacterAndFieldPositions(t *testing.T) {
	out, errOut, code := execCmd(t, testFS(), "echo a,b,c | cut -c1 -d, -f2")
	if code != 0 || errOut != "" || out != "b\n" {
		t.Errorf("got stdout %q, stderr %q, exit %d; want stdout %q, empty stderr, exit 0", out, errOut, code, "b\n")
	}
}

func TestCutRequiresPositions(t *testing.T) {
	for _, command := range []string{"echo hello | cut", "echo hello | cut -c nope"} {
		t.Run(command, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), command)
			if code != 1 || out != "" || errOut != "cut: you must specify a list of bytes, characters, or fields\n" {
				t.Errorf("got stdout %q, stderr %q, exit %d", out, errOut, code)
			}
		})
	}
}
