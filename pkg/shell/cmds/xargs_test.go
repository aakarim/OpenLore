package cmds_test

import (
	"strings"
	"testing"
)

func TestXargs(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "echo readme.md | xargs echo found:")
	if !strings.Contains(out, "found:") || !strings.Contains(out, "readme.md") {
		t.Errorf("xargs: got %q", out)
	}
}

func TestXargsI(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "echo hello | xargs -I {} echo 'got: {}'")
	if !strings.Contains(out, "got: hello") {
		t.Errorf("xargs -I: got %q", out)
	}
}

func TestXargsTreatsInputAsArgumentsNotShellSource(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "parentheses and spaces", command: `echo "Jonas (Hera).md" | xargs echo`, want: "Jonas (Hera).md\n"},
		{name: "semicolon", command: `printf 'a;echo INJECTED\n' | xargs echo`, want: "a;echo INJECTED\n"},
		{name: "command substitution", command: `printf 'b $(echo SUBST)\n' | xargs echo`, want: "b $(echo SUBST)\n"},
		{name: "pipe", command: `printf 'c|echo PIPED\n' | xargs echo`, want: "c|echo PIPED\n"},
		{name: "null delimiter", command: `printf 'a;echo X0\000' | xargs -0 echo`, want: "a;echo X0\n"},
		{name: "replacement", command: `printf 'a;echo XI2\n' | xargs -I {} echo {}`, want: "a;echo XI2\n"},
		{name: "max args", command: `printf 'a;echo XN\n' | xargs -n 1 echo`, want: "a;echo\nXN\n"},
		{name: "custom delimiter", command: `printf 'a;echo XD:' | xargs -d : echo`, want: "a;echo XD\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.command)
			if code != 0 || out != tt.want || errOut != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q, want code=0 stdout=%q stderr empty", code, out, errOut, tt.want)
			}
		})
	}
}
