package cmds_test

import (
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
)

func TestLorePackageList(t *testing.T) {
	out, errOut, code := runMeta(t, shell.NewShell(testFS()), "/", "lore package list")
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 8 {
		t.Fatalf("got %d lines, want header plus 7 members:\n%s", len(lines), out)
	}
	for i, name := range []string{"link/alias", "link/resolves", "okf", "okf/bundle", "size/kilobytes", "size/lines", "size/tokens"} {
		if fields := strings.Fields(lines[i+1]); len(fields) < 4 || fields[0] != name || fields[1] != "rule" || (fields[2] != "file" && fields[2] != "bundle") {
			t.Errorf("member row %d = %q", i, lines[i+1])
		}
	}
}

func TestLorePackageDoc(t *testing.T) {
	sh := shell.NewShell(testFS())
	for _, test := range []struct {
		command string
		want    []string
	}{
		{"lore package doc size/lines", []string{"scope: file", "evaluated:", "every write", "lore validate", "PARAMETERS", "max", "growth", "EXAMPLE"}},
		{"lore package doc link/resolves", []string{"scope: bundle", "lore validate only"}},
		{"lore package doc size", []string{"size/kilobytes", "size/lines", "size/tokens"}},
		{"lore package doc size/tokens", []string{"estimate/v1", "bytes / 4"}},
	} {
		out, errOut, code := runMeta(t, sh, "/", test.command)
		if code != 0 || errOut != "" {
			t.Fatalf("%s: exit=%d stderr=%q", test.command, code, errOut)
		}
		for _, want := range test.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output missing %q:\n%s", test.command, want, out)
			}
		}
	}
}

func TestLorePackageDocUnknown(t *testing.T) {
	_, errOut, code := runMeta(t, shell.NewShell(testFS()), "/", "lore package doc nope")
	if code != 1 || !strings.Contains(errOut, `unknown package member "nope"`) {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
}
