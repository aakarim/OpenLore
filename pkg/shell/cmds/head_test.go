package cmds_test

import (
	"strings"
	"testing"
)

func TestHead(t *testing.T) {
	for _, tt := range []struct {
		cmd  string
		want string
	}{
		{cmd: "head -n 2 /docs/readme.md", want: "# Hello World\nThis is a test file.\n"},
		{cmd: "seq 1 15 | head -n 3", want: "1\n2\n3\n"},
		{cmd: "seq 1 15 | head -c 2 -n +3", want: "1\n2\n3\n"},
	} {
		t.Run(tt.cmd, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.cmd)
			if code != 0 || errOut != "" || out != tt.want {
				t.Errorf("code=%d stdout=%q stderr=%q, want code=0 stdout=%q stderr empty", code, out, errOut, tt.want)
			}
		})
	}
}

func TestHeadTailInvalidLineCount(t *testing.T) {
	for _, command := range []string{"head", "tail"} {
		for _, tt := range []struct {
			args string
			want string
		}{
			{args: "-n", want: "option requires an argument -- 'n'"},
			{args: "-n ''", want: "option requires an argument -- 'n'"},
			{args: "-n $X", want: "option requires an argument -- 'n'"},
			{args: "-n abc", want: "invalid number of lines: 'abc'"},
			{args: "-n 3abc", want: "invalid number of lines: '3abc'"},
			{args: "-n -1", want: "invalid number of lines: '-1'"},
			{args: "-n +", want: "invalid number of lines: '+'"},
			{args: "-n +3abc", want: "invalid number of lines: '+3abc'"},
			{args: "-n 999999999999999999999999", want: "invalid number of lines: '999999999999999999999999'"},
		} {
			t.Run(command+" "+tt.args, func(t *testing.T) {
				out, errOut, code := execCmd(t, testFS(), "X=; seq 1 15 | "+command+" "+tt.args)
				wantErr := command + ": " + tt.want + "\n"
				if code != 1 || out != "" || errOut != wantErr {
					t.Errorf("code=%d stdout=%q stderr=%q, want code=1 stdout empty stderr=%q", code, out, errOut, wantErr)
				}
			})
		}
		out, errOut, code := execCmd(t, testFS(), command+" /docs/readme.md -n abc")
		if code != 1 || out != "" || !strings.Contains(errOut, "invalid number of lines") {
			t.Errorf("%s file: code=%d stdout=%q stderr=%q", command, code, out, errOut)
		}
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
