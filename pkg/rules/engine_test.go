package rules

import (
	"context"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type testSource []Layer

func (s testSource) LayersFor(context.Context, string) ([]Layer, error) { return s, nil }

type testFolderSource []Layer

func (s testFolderSource) LayersForDir(context.Context, string) ([]Layer, error) { return s, nil }
func (s testFolderSource) LayersAbove(context.Context, string) ([]Layer, error)  { return s, nil }
func (testFolderSource) Invalidate(string)                                       {}

type testMember struct {
	manifest Manifest
	calls    *int
	subjects *[]Subject
}

func (m testMember) Manifest() Manifest { return m.manifest }
func (m testMember) Compile(map[string]any, Env) (Check, error) {
	return testCheck{calls: m.calls, subjects: m.subjects}, nil
}

type testCheck struct {
	calls    *int
	subjects *[]Subject
}

func (c testCheck) Evaluate(_ context.Context, subject Subject) ([]Finding, error) {
	if c.calls != nil {
		(*c.calls)++
	}
	if c.subjects != nil {
		*c.subjects = append(*c.subjects, subject)
	}
	return nil, nil
}
func (testCheck) OnRemove(context.Context, string, string) error { return nil }
func (testCheck) OnMove(context.Context, string, string) error   { return nil }

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

func TestValidatorCallsBundleRuleOnceAtBundleRoot(t *testing.T) {
	calls := 0
	var subjects []Subject
	registry := NewRegistry()
	registry.Register(testMember{manifest: Manifest{Path: "test/bundle", Kind: KindRule, Scope: ScopeBundle}, calls: &calls, subjects: &subjects})
	engine := New(Options{Registry: registry, Config: testSource{{Origin: "test", Scope: "/docs", Rules: map[string]RuleSpec{"bundle": {Match: []string{"**"}, Use: "test/bundle"}}}}})
	bundle := validation.Bundle{Root: "/docs", Files: []validation.File{{AbsolutePath: "/docs/a.md"}, {AbsolutePath: "/docs/b.md"}}}
	engine.Validator()(bundle)
	if calls != 1 || len(subjects) != 1 || subjects[0].Path != bundle.Root {
		t.Fatalf("calls=%d subjects=%#v", calls, subjects)
	}
}

func TestValidatorRunsBundleRuleWhenFolderIsNamedBundleRoot(t *testing.T) {
	calls := 0
	registry := NewRegistry()
	registry.Register(testMember{manifest: Manifest{Path: "test/bundle", Kind: KindRule, Scope: ScopeBundle}, calls: &calls})
	engine := New(Options{Registry: registry, Folders: testFolderSource{{Origin: "/docs/sub/.lore/config.yaml", Scope: "/docs/sub", Rules: map[string]RuleSpec{"bundle": {Match: []string{"**"}, Use: "test/bundle"}}}}})
	bundle := validation.Bundle{Root: "/docs/sub", Files: []validation.File{{AbsolutePath: "/docs/sub/a.md"}}}
	engine.Validator()(bundle)
	if calls != 1 {
		t.Fatalf("bundle rule calls=%d, want 1", calls)
	}
}

func TestCompileReportsSuggestionsAndTypes(t *testing.T) {
	registry := NewRegistry()
	registry.Register(testMember{manifest: Manifest{Path: "size/lines", Kind: KindRule, Scope: ScopeFile, Params: []Param{{Name: "max", Type: ParamInteger, Required: true}}}})
	compile := func(spec RuleSpec) error {
		_, err := Compile(registry, func(string) Env { return Env{} }, map[string]UnifiedRule{"limit": {Spec: spec, Origins: []string{"test"}, Scope: "/"}})
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

func TestCompileRejectsInvalidGlobs(t *testing.T) {
	registry := NewRegistry()
	registry.Register(testMember{manifest: Manifest{Path: "test/rule", Kind: KindRule, Scope: ScopeFile}})
	for _, test := range []struct {
		field string
		spec  RuleSpec
	}{
		{field: "match", spec: RuleSpec{Use: "test/rule", Match: []string{"docs/["}}},
		{field: "exclude", spec: RuleSpec{Use: "test/rule", Match: []string{"**"}, Exclude: []string{"docs/["}}},
	} {
		_, err := Compile(registry, func(string) Env { return Env{} }, map[string]UnifiedRule{"broken": {Spec: test.spec, Origins: []string{"test"}, Scope: "/"}})
		if err == nil || !strings.Contains(err.Error(), "rules.broken."+test.field+"[0]: invalid glob") {
			t.Errorf("%s error=%v", test.field, err)
		}
	}
}
