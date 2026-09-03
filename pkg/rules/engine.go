package rules

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type Options struct {
	Registry *Registry
	Config   LayerSource
	Folders  LayerSource
	Env      func(string) Env
	Logger   *slog.Logger
}

type Engine struct{ options Options }

func New(options Options) *Engine { return &Engine{options: options} }

func (e *Engine) Effective(ctx context.Context, target string) ([]CompiledRule, error) {
	var layers []Layer
	for _, source := range []LayerSource{e.options.Config, e.options.Folders} {
		if source == nil {
			continue
		}
		got, err := source.LayersFor(ctx, target)
		if err != nil {
			return nil, err
		}
		layers = append(layers, got...)
	}
	if len(layers) == 0 {
		return nil, nil
	}
	specs, origins, err := Unify(layers)
	if err != nil {
		return nil, err
	}
	scope := layers[len(layers)-1].Scope
	env := e.options.Env
	if env == nil {
		env = func(string) Env { return Env{} }
	}
	return Compile(e.options.Registry, env, specs, origins, scope)
}

func (e *Engine) AdmitLeaf(ctx context.Context, leaf vfs.Change, actor string, existing func() ([]byte, bool, error)) error {
	if leaf.Action != vfs.ChangeActionWrite || leaf.Write == nil {
		return nil
	}
	rules, err := e.Effective(ctx, leaf.Target)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.Member.Manifest().Scope != ScopeFile || !matchesAtScope(rule, leaf.Target) {
			continue
		}
		findings, checkErr := rule.Check.Evaluate(ctx, Subject{Mode: ModeAdmit, Path: leaf.Target, Dir: path.Dir(leaf.Target), Content: leaf.Write.Bytes, Existing: existing, Actor: actor})
		if checkErr != nil {
			if rule.Spec.IsEnforcing() {
				return &Rejection{Path: leaf.Target, Rule: rule.Name, Member: rule.Spec.Use, Origin: last(rule.Origins), Err: checkErr}
			}
			e.warn(rule, leaf.Target, checkErr.Error())
			continue
		}
		if len(findings) == 0 {
			continue
		}
		if rule.Spec.IsEnforcing() {
			return &Rejection{Path: leaf.Target, Rule: rule.Name, Member: rule.Spec.Use, Origin: last(rule.Origins), Findings: findings}
		}
		for _, finding := range findings {
			e.warn(rule, leaf.Target, findingText(finding, rule, leaf.Target))
		}
	}
	return nil
}

func (e *Engine) ValidateFile(ctx context.Context, fsys vfs.FileSystem, bundleRoot, target string, content []byte) []validation.Diagnostic {
	return e.validateFile(ctx, fsys, bundleRoot, target, content, nil)
}

func (e *Engine) validateFile(ctx context.Context, fsys vfs.FileSystem, bundleRoot, target string, content []byte, bundle *validation.Bundle) []validation.Diagnostic {
	rules, err := e.Effective(ctx, target)
	if err != nil {
		return []validation.Diagnostic{{Path: relative(bundleRoot, target), Line: 1, Column: 1, Severity: validation.SeverityError, Rule: "rules/config", Message: err.Error()}}
	}
	var diagnostics []validation.Diagnostic
	for _, rule := range rules {
		if rule.Member.Manifest().Scope == ScopeBundle {
			if bundle == nil || len(bundle.Files) == 0 || target != bundle.Files[0].AbsolutePath || !bundleMatches(rule, bundle) {
				continue
			}
		} else if !matchesAtScope(rule, target) {
			continue
		}
		findings, checkErr := rule.Check.Evaluate(ctx, Subject{Mode: ModeValidate, Path: target, Dir: path.Dir(target), Content: content, FS: fsys, BundleRoot: bundleRoot, Bundle: bundle})
		severity := validation.SeverityWarning
		if rule.Spec.IsEnforcing() {
			severity = validation.SeverityError
		}
		if checkErr != nil {
			diagnostics = append(diagnostics, validation.Diagnostic{Path: relative(bundleRoot, target), Line: 1, Column: 1, Severity: severity, Rule: rule.Name, Message: checkErr.Error()})
			continue
		}
		for _, finding := range findings {
			findingSeverity := severity
			if finding.Warning {
				findingSeverity = validation.SeverityWarning
			}
			p := finding.Path
			if p == "" {
				p = target
			}
			if strings.HasPrefix(p, "/") {
				p = relative(bundleRoot, p)
			}
			line, column := finding.Line, finding.Column
			if line == 0 {
				line = 1
			}
			if column == 0 {
				column = 1
			}
			diagnostics = append(diagnostics, validation.Diagnostic{Path: p, Line: line, Column: column, Severity: findingSeverity, Rule: finding.Code, Message: findingText(finding, rule, target)})
		}
	}
	return diagnostics
}

func (e *Engine) Validator() validation.Validator {
	return func(bundle validation.Bundle) []validation.Diagnostic {
		var out []validation.Diagnostic
		for _, file := range bundle.Files {
			out = append(out, e.validateFile(context.Background(), bundle.FS, bundle.Root, file.AbsolutePath, file.Content, &bundle)...)
		}
		return out
	}
}

func (e *Engine) ValidateBundle(ctx context.Context, fsys vfs.FileSystem, root string) ([]validation.Diagnostic, error) {
	_ = ctx
	return validation.Scan(fsys, root, e.Validator())
}

func matchesAtScope(rule CompiledRule, target string) bool {
	rel, ok := relativeTo(rule.Scope, target)
	return ok && Matches(rule.Spec, rel)
}

func bundleMatches(rule CompiledRule, bundle *validation.Bundle) bool {
	for _, file := range bundle.Files {
		if matchesAtScope(rule, file.AbsolutePath) {
			return true
		}
	}
	return false
}

func relativeTo(root, target string) (string, bool) {
	root, target = vfs.CleanPath(root), vfs.CleanPath(target)
	if root == "/" {
		return strings.TrimPrefix(target, "/"), true
	}
	if target == root {
		return path.Base(target), true
	}
	if !strings.HasPrefix(target, root+"/") {
		return "", false
	}
	return strings.TrimPrefix(target, root+"/"), true
}

func relative(root, target string) string {
	rel, ok := relativeTo(root, target)
	if ok {
		return rel
	}
	return target
}
func last(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func (e *Engine) warn(rule CompiledRule, target, message string) {
	if e.options.Logger != nil {
		e.options.Logger.Warn("rule violation (non-enforcing)", "path", target, "rule", rule.Name, "member", rule.Spec.Use, "message", message)
	}
}

type Rejection struct {
	Path     string
	Rule     string
	Member   string
	Origin   string
	Findings []Finding
	Err      error
}

func (r *Rejection) Error() string {
	if r.Member == "okf" && r.Err != nil {
		return fmt.Sprintf("okf: %s: %v", r.Path, r.Err)
	}
	if r.Member == "okf" && len(r.Findings) != 0 {
		return fmt.Sprintf("okf: %s: %s", r.Path, r.Findings[0].Measured)
	}
	header := fmt.Sprintf("rules: %s: %s (%s @ %s)", r.Path, r.Member, r.Rule, r.Origin)
	if r.Err != nil {
		return header + "\n  " + r.Err.Error()
	}
	parts := []string{header}
	for _, finding := range r.Findings {
		parts = append(parts, indent(findingText(finding, CompiledRule{Name: r.Rule, Spec: RuleSpec{Use: r.Member}, Origins: []string{r.Origin}}, r.Path)))
	}
	return strings.Join(parts, "\n")
}

func (r *Rejection) Unwrap() error { return r.Err }

func findingText(f Finding, rule CompiledRule, target string) string {
	limit := f.Limit
	if limit == "" {
		limit = "this write cannot proceed under this rule"
	}
	remedy := f.Remedy
	if remedy == "" {
		remedy = "change the file so it satisfies the rule"
	}
	override := f.Override
	if override == "" {
		if strings.HasPrefix(rule.Spec.Use, "size/") {
			override = fmt.Sprintf("to raise the limit, edit %s in %s", rule.Name, last(rule.Origins))
		} else {
			override = fmt.Sprintf("edit %s in %s", rule.Name, last(rule.Origins))
		}
	}
	return strings.Join([]string{f.Measured, limit, "suggested: " + remedy, "override: " + override, "see: lore package doc " + rule.Spec.Use}, "\n")
}

func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }

func SortDiagnostics(d []validation.Diagnostic) {
	sort.SliceStable(d, func(i, j int) bool {
		if d[i].Path != d[j].Path {
			return d[i].Path < d[j].Path
		}
		if d[i].Line != d[j].Line {
			return d[i].Line < d[j].Line
		}
		return d[i].Rule < d[j].Rule
	})
}
