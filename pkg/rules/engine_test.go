package rules

import (
	"context"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type testSource []Layer

func (s testSource) LayersFor(context.Context, string) ([]Layer, error) { return s, nil }

type testMember struct {
	manifest Manifest
	calls    *int
}

func (m testMember) Manifest() Manifest { return m.manifest }
func (m testMember) Compile(map[string]any, Env) (Check, error) {
	return testCheck{calls: m.calls}, nil
}

type testCheck struct{ calls *int }

func (c testCheck) Evaluate(context.Context, Subject) ([]Finding, error) {
	if c.calls != nil {
		(*c.calls)++
	}
	return nil, nil
}
func (testCheck) OnRemove(context.Context, string) error       { return nil }
func (testCheck) OnMove(context.Context, string, string) error { return nil }

func TestAdmitLeafNeverCallsBundleRule(t *testing.T) {
	calls := 0
	registry := NewRegistry()
	registry.Register(testMember{manifest: Manifest{Path: "test/bundle", Kind: KindRule, Scope: ScopeBundle}, calls: &calls})
	engine := New(Options{Registry: registry, Config: testSource{{Origin: "test", Scope: "/", Rules: map[string]RuleSpec{"bundle": {Match: []string{"**"}, Use: "test/bundle"}}}}})
	err := engine.AdmitLeaf(context.Background(), vfs.Change{Target: "/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("x")}}, "test", nil)
	if err != nil || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestCompileReportsSuggestionsAndTypes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(testMember{manifest: Manifest{Path: "size/lines", Kind: KindRule, Scope: ScopeFile, Params: []Param{{Name: "max", Type: ParamInteger, Required: true}}}})
	compile := func(spec RuleSpec) error {
		_, err := Compile(registry, func(string) Env { return Env{} }, map[string]RuleSpec{"limit": spec}, map[string][]string{"limit": {"test"}}, "/")
		return err
	}
	if err := compile(RuleSpec{Use: "size/line", With: map[string]any{"max": 3}}); err == nil || !strings.Contains(err.Error(), `did you mean "size/lines"`) {
		t.Fatalf("unknown member error=%v", err)
	}
	if err := compile(RuleSpec{Use: "size/lines", With: map[string]any{"maxx": 3}}); err == nil || !strings.Contains(err.Error(), `did you mean "max"`) {
		t.Fatalf("unknown param error=%v", err)
	}
	if err := compile(RuleSpec{Use: "size/lines", With: map[string]any{"max": "three"}}); err == nil || !strings.Contains(err.Error(), "expected integer") {
		t.Fatalf("type error=%v", err)
	}
	if err := compile(RuleSpec{Use: "size/lines"}); err == nil || !strings.Contains(err.Error(), "required parameter") {
		t.Fatalf("missing error=%v", err)
	}
}
