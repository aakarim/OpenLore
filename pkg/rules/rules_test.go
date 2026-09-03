package rules

import (
	"errors"
	"testing"
)

func TestMatches(t *testing.T) {
	spec := RuleSpec{Match: []string{"**/*.md"}, Exclude: []string{"draft-*.md", "private/**"}}
	for name, want := range map[string]bool{"a.md": true, "docs/a.md": true, "draft-a.md": false, "private/a.md": false, "a.txt": false} {
		if got := Matches(spec, name); got != want {
			t.Errorf("Matches(%q)=%v, want %v", name, got, want)
		}
	}
}

func TestUnify(t *testing.T) {
	a := RuleSpec{Match: []string{"**/*.md"}, Use: "size/lines", With: map[string]any{"max": 3}}
	b := a
	unified, err := Unify([]Layer{{Origin: "outer", Scope: "/outer", Rules: map[string]RuleSpec{"a": a}}, {Origin: "inner", Scope: "/inner", Rules: map[string]RuleSpec{"a": b}}})
	if err != nil || len(unified) != 1 || len(unified["a"].Origins) != 2 || unified["a"].Scope != "/outer" {
		t.Fatalf("identical unification: %v %#v", err, unified)
	}
	b.With = map[string]any{"max": 4}
	_, err = Unify([]Layer{{Origin: "outer", Rules: map[string]RuleSpec{"a": a}}, {Origin: "inner", Rules: map[string]RuleSpec{"a": b}}})
	var conflict *UnificationError
	if !errors.As(err, &conflict) || conflict.OuterOrigin != "outer" || conflict.InnerOrigin != "inner" {
		t.Fatalf("conflict = %#v, %v", conflict, err)
	}
	a.Default = true
	unified, err = Unify([]Layer{{Origin: "outer", Scope: "/outer", Rules: map[string]RuleSpec{"a": a}}, {Origin: "inner", Scope: "/inner", Rules: map[string]RuleSpec{"a": b}}})
	if err != nil || unified["a"].Spec.With["max"] != 4 || unified["a"].Scope != "/inner" {
		t.Fatalf("default replacement: %v %#v", err, unified)
	}
}
