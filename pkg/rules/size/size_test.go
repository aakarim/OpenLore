package size

import (
	"context"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/packagestate"
	"github.com/aakarim/go-openlore/pkg/rules"
)

type mapper map[string]string

func (m mapper) HostDir(dir string) (string, bool) { host, ok := m[dir]; return host, ok }

func TestLinesFixedMax(t *testing.T) {
	check, err := (Rule{Metric: Lines}).Compile(map[string]any{"max": 3}, rules.Env{})
	if err != nil {
		t.Fatal(err)
	}
	findings, err := check.Evaluate(context.Background(), rules.Subject{Path: "/docs/a.md", Content: []byte("1\n2\n3\n4\n")})
	if err != nil || len(findings) != 1 || findings[0].Code != "size/lines" {
		t.Fatalf("findings=%#v err=%v", findings, err)
	}
}

func TestInitialBaselineUsesExistingContentAndResetAppends(t *testing.T) {
	state := packagestate.Open(mapper{"/docs": t.TempDir()}, "size")
	compiled, err := (Rule{Metric: Lines}).Compile(map[string]any{"max": "initial", "growth": 1.5}, rules.Env{State: state})
	if err != nil {
		t.Fatal(err)
	}
	existing := func() ([]byte, bool, error) { return []byte("x\n"), true, nil }
	findings, err := compiled.Evaluate(context.Background(), rules.Subject{Mode: rules.ModeAdmit, Path: "/docs/a.md", Content: []byte("1\n2\n"), Existing: existing, Actor: "alice"})
	if err != nil || len(findings) != 1 || !strings.Contains(findings[0].Measured, "baseline 1 lines × growth 1.5") || !strings.Contains(findings[0].Measured, "on rule-added") {
		t.Fatalf("findings=%v err=%v", findings, err)
	}
	store, _, _, _ := Inspect(compiled)
	prev, next, err := Reset(context.Background(), store, "/docs/a.md", []byte("1\n2\n"), nil, "bob", "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	if prev.Lines != 1 || next.Lines != 2 || next.Reason != "reset" || next.Actor != "bob" || next.Note != "reviewed" {
		t.Fatalf("prev=%#v next=%#v", prev, next)
	}
	history, err := store.History(context.Background(), "/docs/a.md")
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%#v err=%v", history, err)
	}
}

func TestManifestAdvertisesInitialMax(t *testing.T) {
	params := (Rule{Metric: Lines}).Manifest().Params
	if len(params) != 2 || params[0].Name != "max" || params[0].Type != rules.ParamIntegerOrInitial || !params[0].Required {
		t.Fatalf("params=%#v", params)
	}
}
