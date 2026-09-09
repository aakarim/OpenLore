package cmds_test

import (
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "tail -n 3 /docs/numbers.txt")
	if !strings.Contains(out, "20") || !strings.Contains(out, "3") {
		t.Errorf("tail: got %q", out)
	}
}

func TestTailPipe(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "seq 10 | tail -n 3")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Should have the last 3 numbers from 1-10
	if len(lines) < 2 || !strings.Contains(out, "10") {
		t.Errorf("tail pipe: got %q", out)
	}
}

func TestTailBytes(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{name: "stdin", cmd: "echo hello | tail -c 3", want: "lo\n"},
		{name: "file and attached count", cmd: "tail -c6 /docs/readme.md", want: "ine 5\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.cmd)
			if code != 0 || errOut != "" || out != tt.want {
				t.Errorf("code=%d stdout=%q stderr=%q, want code=0 stdout=%q stderr empty", code, out, errOut, tt.want)
			}
		})
	}
}

func TestTailInvalidByteCount(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantErr string
	}{
		{name: "missing", cmd: "tail -c", wantErr: "tail: option requires an argument -- 'c'\n"},
		{name: "non-numeric", cmd: "tail -c nope", wantErr: "tail: invalid number of bytes: nope\n"},
		{name: "negative separated", cmd: "tail -c -1", wantErr: "tail: invalid number of bytes: -1\n"},
		{name: "negative attached", cmd: "tail -c-1", wantErr: "tail: invalid number of bytes: -1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.cmd)
			if code != 1 || out != "" || errOut != tt.wantErr {
				t.Errorf("code=%d stdout=%q stderr=%q, want code=1 stdout empty stderr=%q", code, out, errOut, tt.wantErr)
			}
		})
	}
}
