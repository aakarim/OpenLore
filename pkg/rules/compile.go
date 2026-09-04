package rules

import (
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
)

type UnificationError struct {
	Rule          string
	OuterOrigin   string
	InnerOrigin   string
	DifferingKeys []string
}

func (e *UnificationError) Error() string {
	return fmt.Sprintf("rule %q conflicts between %s and %s (different: %s)", e.Rule, e.OuterOrigin, e.InnerOrigin, strings.Join(e.DifferingKeys, ", "))
}

func Unify(layers []Layer) (map[string]UnifiedRule, error) {
	unified := map[string]UnifiedRule{}
	for _, layer := range layers {
		for name, incoming := range layer.Rules {
			outer, exists := unified[name]
			if !exists {
				unified[name] = UnifiedRule{Spec: incoming, Origins: []string{layer.Origin}, Scope: layer.Scope}
				continue
			}
			if outer.Spec.Default {
				unified[name] = UnifiedRule{Spec: incoming, Origins: append(outer.Origins, layer.Origin), Scope: layer.Scope}
				continue
			}
			if keys := DifferingKeys(outer.Spec, incoming); len(keys) != 0 {
				return nil, &UnificationError{Rule: name, OuterOrigin: outer.Origins[len(outer.Origins)-1], InnerOrigin: layer.Origin, DifferingKeys: keys}
			}
			outer.Origins = append(outer.Origins, layer.Origin)
			unified[name] = outer
		}
	}
	return unified, nil
}

// DifferingKeys returns normalized semantic fields that differ between specs.
func DifferingKeys(a, b RuleSpec) []string {
	var keys []string
	if !reflect.DeepEqual(normalizeStrings(a.Match), normalizeStrings(b.Match)) {
		keys = append(keys, "match")
	}
	if !reflect.DeepEqual(normalizeStrings(a.Exclude), normalizeStrings(b.Exclude)) {
		keys = append(keys, "exclude")
	}
	if a.Use != b.Use {
		keys = append(keys, "use")
	}
	if !reflect.DeepEqual(a.With, b.With) {
		keys = append(keys, "with")
	}
	if a.IsEnforcing() != b.IsEnforcing() {
		keys = append(keys, "enforce")
	}
	if a.Default != b.Default {
		keys = append(keys, "default")
	}
	return keys
}

func normalizeStrings(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return v
}

func Compile(reg *Registry, env func(string) Env, unified map[string]UnifiedRule) ([]CompiledRule, error) {
	if reg == nil {
		return nil, fmt.Errorf("rules: nil registry")
	}
	names := make([]string, 0, len(unified))
	for name := range unified {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]CompiledRule, 0, len(names))
	for _, name := range names {
		rule := unified[name]
		spec := rule.Spec
		if err := validateGlobs(name, "match", spec.Match); err != nil {
			return nil, err
		}
		if err := validateGlobs(name, "exclude", spec.Exclude); err != nil {
			return nil, err
		}
		member, ok := reg.Lookup(spec.Use)
		if !ok {
			suffix := ""
			if suggestion := reg.Suggest(spec.Use); suggestion != "" {
				suffix = fmt.Sprintf(" (did you mean %q?)", suggestion)
			}
			return nil, fmt.Errorf("rules.%s.use: unknown member %q%s", name, spec.Use, suffix)
		}
		with := make(map[string]any, len(spec.With))
		for key, value := range spec.With {
			with[key] = value
		}
		for _, param := range member.Manifest().Params {
			if _, ok := with[param.Name]; !ok && param.Default != nil {
				with[param.Name] = param.Default
			}
		}
		if err := validateParams(name, with, member.Manifest()); err != nil {
			return nil, err
		}
		check, err := member.Compile(with, env(spec.Use))
		if err != nil {
			return nil, fmt.Errorf("rules.%s.with: %w", name, err)
		}
		out = append(out, CompiledRule{Name: name, Spec: spec, Origins: rule.Origins, Scope: rule.Scope, Member: member, Check: check})
	}
	return out, nil
}

func validateGlobs(rule, field string, patterns []string) error {
	for i, pattern := range patterns {
		for _, segment := range split(pattern) {
			if segment == "**" {
				continue
			}
			if _, err := path.Match(segment, ""); err != nil {
				return fmt.Errorf("rules.%s.%s[%d]: invalid glob %q: %w", rule, field, i, pattern, err)
			}
		}
	}
	return nil
}

func validateParams(rule string, with map[string]any, manifest Manifest) error {
	params := map[string]Param{}
	for _, p := range manifest.Params {
		params[p.Name] = p
	}
	for name, value := range with {
		p, ok := params[name]
		if !ok {
			alternatives := make([]string, 0, len(params))
			for n := range params {
				alternatives = append(alternatives, n)
			}
			suffix := ""
			if near := nearest(name, alternatives); near != "" {
				suffix = fmt.Sprintf(" (did you mean %q?)", near)
			}
			return fmt.Errorf("rules.%s.with.%s: unknown parameter for %s%s", rule, name, manifest.Path, suffix)
		}
		if !valueHasType(value, p.Type) {
			return fmt.Errorf("rules.%s.with.%s: expected %s", rule, name, p.Type)
		}
	}
	for _, p := range manifest.Params {
		if p.Required {
			if _, ok := with[p.Name]; !ok {
				return fmt.Errorf("rules.%s.with.%s: required parameter for %s", rule, p.Name, manifest.Path)
			}
		}
	}
	return nil
}

func valueHasType(v any, typ ParamType) bool {
	switch typ {
	case ParamInteger:
		_, ok := integer(v)
		return ok
	case ParamNumber:
		_, ok := number(v)
		return ok
	case ParamString:
		_, ok := v.(string)
		return ok
	case ParamBool:
		_, ok := v.(bool)
		return ok
	case ParamIntegerOrInitial:
		if s, ok := v.(string); ok {
			return s == "initial"
		}
		_, ok := integer(v)
		return ok
	default:
		return false
	}
}

func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == float64(int64(n))
	case float32:
		return int64(n), n == float32(int64(n))
	default:
		return 0, false
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	default:
		return 0, false
	}
}

func nearest(got string, values []string) string {
	best, score := "", 1<<30
	for _, v := range values {
		if d := distance(got, v); d < score {
			best, score = v, d
		}
	}
	if score <= 3 {
		return best
	}
	return ""
}
