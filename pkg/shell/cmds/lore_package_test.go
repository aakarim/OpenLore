package cmds_test

import (
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/shell"
)

func TestLorePackageList(t *testing.T) {
	out, errOut, code := runMeta(t, shell.NewShell(testFS()), "/", "lore package list")
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	members := rules.DefaultRegistry().All()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(members)+1 {
		t.Fatalf("got %d lines, want header plus %d members:\n%s", len(lines), len(members), out)
	}
	for i, member := range members {
		manifest := member.Manifest()
		fields := strings.Fields(lines[i+1])
		if len(fields) < 4 || fields[0] != manifest.Path || fields[1] != string(manifest.Kind) || fields[2] != string(manifest.Scope) || !strings.Contains(lines[i+1], manifest.Summary) {
			t.Errorf("member row %d = %q", i, lines[i+1])
		}
	}
	if !containsMemberRow(lines[1:], "size/lines", "rule", "file") {
		t.Errorf("missing size/lines rule/file spot check:\n%s", out)
	}
}

func containsMemberRow(lines []string, name, kind, scope string) bool {
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == name && fields[1] == kind && fields[2] == scope {
			return true
		}
	}
	return false
}

func TestLorePackageDoc(t *testing.T) {
	sh := shell.NewShell(testFS())
	for _, test := range []struct {
		command string
		want    []string
	}{
		{"lore package doc size/lines", []string{"scope: file", "evaluated:", "every write", "lore validate", "PARAMETERS", "max", "EXAMPLE"}},
		{"lore package doc link/resolves", []string{"scope: bundle", "lore validate only"}},
		{"lore package doc size", []string{"size/kilobytes", "size/lines", "size/tokens"}},
		{"lore package doc size/tokens", []string{"estimate/v1", "bytes / 4"}},
		{"lore package doc okf", []string{"MEMBERS", "okf/bundle"}},
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
