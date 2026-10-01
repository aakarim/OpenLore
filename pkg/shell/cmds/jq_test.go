package cmds_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
)

func TestJqField(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "jq -r .name /docs/data.json")
	if strings.TrimSpace(out) != "alice" {
		t.Errorf("jq: got %q", strings.TrimSpace(out))
	}
}

func TestJqPipe(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out, errOut bytes.Buffer
	sh.Exec("jq '.items | length' /docs/data.json", &out, &errOut, nil)
	if strings.TrimSpace(out.String()) != "3" {
		t.Errorf("jq pipe: got %q", out.String())
	}
}

func TestJqSelect(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out, errOut bytes.Buffer
	sh.Exec("jq '.[] | select(.active)' /docs/users.json", &out, &errOut, nil)
	if !strings.Contains(out.String(), "alice") {
		t.Errorf("jq select: got %q", out.String())
	}
	if strings.Contains(out.String(), "bob") {
		t.Error("jq select should filter bob")
	}
}

func TestJqMap(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "jq 'map(.name)' /docs/users.json")
	if !strings.Contains(out, "alice") {
		t.Errorf("jq map: got %q", out)
	}
}

func TestJqObjectConstruct(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out, errOut bytes.Buffer
	sh.Exec("jq '{n: .name, a: .age}' /docs/data.json", &out, &errOut, nil)
	if !strings.Contains(out.String(), "alice") || !strings.Contains(out.String(), "30") {
		t.Errorf("jq object: got %q", out.String())
	}
}

func TestJqSortBy(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out bytes.Buffer
	sh.Exec("jq 'sort_by(.age)' /docs/users.json", &out, &bytes.Buffer{}, nil)
	bobIdx := strings.Index(out.String(), "bob")
	aliceIdx := strings.Index(out.String(), "alice")
	if bobIdx < 0 || aliceIdx < 0 || bobIdx > aliceIdx {
		t.Errorf("jq sort_by: bob should come before alice, got %q", out.String())
	}
}

func TestJqAdd(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out bytes.Buffer
	sh.Exec("jq '.items | add' /docs/data.json", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "6" {
		t.Errorf("jq add: got %q", strings.TrimSpace(out.String()))
	}
}

func TestJqAlternativeOperator(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/present.json", `{"a":1}`)
	fs.AddFile("/docs/missing.json", `{}`)
	fs.AddFile("/docs/false.json", `{"a":false}`)
	fs.AddFile("/docs/zero.json", `{"a":0}`)

	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{name: "present value", cmd: "jq -c '.a // 5' /docs/present.json", want: "1"},
		{name: "missing value", cmd: "jq -c '.a // 5' /docs/missing.json", want: "5"},
		{name: "false value", cmd: "jq -c '.a // 5' /docs/false.json", want: "5"},
		{name: "zero is truthy", cmd: "jq -c '.a // 5' /docs/zero.json", want: "0"},
		{name: "empty output", cmd: "jq -c 'empty // 5' /docs/present.json", want: "5"},
		{name: "mixed outputs", cmd: "jq -c '(null, false, 1) // 5' /docs/present.json", want: "1"},
		{name: "all false outputs", cmd: "jq -c '(null, false) // 5' /docs/present.json", want: "5"},
		{name: "object value", cmd: "jq -c '{t: (.a // null)}' /docs/present.json", want: `{"t":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := execCmd(t, fs, tt.cmd)
			if code != 0 || strings.TrimSpace(out) != tt.want {
				t.Fatalf("code=%d stdout=%q stderr=%q, want stdout %q", code, strings.TrimSpace(out), errOut, tt.want)
			}
		})
	}
}
