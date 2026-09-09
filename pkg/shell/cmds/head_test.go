package cmds_test

import (
	"strings"
	"testing"
)

func TestHead(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "head -n 2 /docs/readme.md")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("head -n 2: got %d lines, want 2", len(lines))
	}
}

func TestHeadBytes(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{name: "stdin", cmd: "echo hello | head -c 3", want: "hel"},
		{name: "file and attached count", cmd: "head -c7 /docs/readme.md", want: "# Hello"},
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

func TestHeadInvalidByteCount(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantErr string
	}{
		{name: "missing", cmd: "head -c", wantErr: "head: option requires an argument -- 'c'\n"},
		{name: "non-numeric", cmd: "head -c nope", wantErr: "head: invalid number of bytes: nope\n"},
		{name: "negative separated", cmd: "head -c -1", wantErr: "head: invalid number of bytes: -1\n"},
		{name: "negative attached", cmd: "head -c-1", wantErr: "head: invalid number of bytes: -1\n"},
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
