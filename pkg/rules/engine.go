package rules

import (
	"context"
	"errors"
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

// BundleRootError reports that validation was requested above independently
// governed docsets. Running bundle rules there would silently choose the wrong
// configuration boundary.
type BundleRootError struct {
	Root    string
	Docsets []string
}

func (e *BundleRootError) Error() string {
	return fmt.Sprintf("%s is above docsets %s with bundle rules; run lore validate per docset", e.Root, strings.Join(e.Docsets, ", "))
}

func New(options Options) *Engine { return &Engine{options: options} }

// CheckConfigFile validates a proposed .lore/config.yaml against every layer
// above its containing directory.
func (e *Engine) CheckConfigFile(ctx context.Context, dir string, content []byte) error {
	configPath := path.Join(vfs.CleanPath(dir), ".lore/config.yaml")
	config, err := DecodeFile(content)
	if err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}
	target := path.Join(vfs.CleanPath(dir), "__rules_config_check__.md")
	var layers []Layer
	if e.options.Config != nil {
		got, err := e.options.Config.LayersFor(ctx, target)
		if err != nil {
			return fmt.Errorf("%s: %w", configPath, err)
		}
		layers = append(layers, got...)
	}
	if e.options.Folders != nil {
		folders, ok := e.options.Folders.(FolderLayerSource)
		if !ok {
			return fmt.Errorf("%s: folder layer source cannot validate config", configPath)
		}
		got, err := folders.LayersAbove(ctx, dir)
		if err != nil {
			return fmt.Errorf("%s: %w", configPath, err)
		}
		layers = append(layers, got...)
	}
	layers = append(layers, Layer{Origin: configPath, Scope: vfs.CleanPath(dir), Rules: config.Rules})
	unified, err := Unify(layers)
	if err != nil {
		var conflict *UnificationError
		if errors.As(err, &conflict) {
			return fmt.Errorf("%s: rules.%s: conflicts with %s @ %s (%s); use a new rule name to tighten", configPath, conflict.Rule, conflict.Rule, conflict.OuterOrigin, conflictDetails(conflict, layers))
		}
		return fmt.Errorf("%s: %w", configPath, err)
	}
	env := e.options.Env
	if env == nil {
		env = func(string) Env { return Env{} }
	}
	if _, err := Compile(e.options.Registry, env, unified); err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}
	return nil
}

func conflictDetails(conflict *UnificationError, layers []Layer) string {
	var outer, inner RuleSpec
	for _, layer := range layers {
		spec, ok := layer.Rules[conflict.Rule]
		if !ok {
			continue
		}
		if layer.Origin == conflict.OuterOrigin {
			outer = spec
		}
		if layer.Origin == conflict.InnerOrigin {
			inner = spec
		}
	}
	var details []string
	for _, key := range conflict.DifferingKeys {
		if key != "with" {
			details = append(details, key+" differs")
			continue
		}
		keys := make(map[string]bool, len(outer.With)+len(inner.With))
		for name := range outer.With {
			keys[name] = true
		}
		for name := range inner.With {
			keys[name] = true
		}
		names := make([]string, 0, len(keys))
		for name := range keys {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if fmt.Sprint(outer.With[name]) != fmt.Sprint(inner.With[name]) {
				details = append(details, fmt.Sprintf("%s: %v vs %v", name, outer.With[name], inner.With[name]))
			}
		}
	}
	return strings.Join(details, ", ")
}

// Invalidate drops cached folder layers at and below dir.
func (e *Engine) Invalidate(dir string) {
	if folders, ok := e.options.Folders.(FolderLayerSource); ok {
		folders.Invalidate(dir)
	}
}

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
	unified, err := Unify(layers)
	if err != nil {
		return nil, err
	}
	env := e.options.Env
	if env == nil {
		env = func(string) Env { return Env{} }
	}
	return Compile(e.options.Registry, env, unified)
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
		var violations []Finding
		for _, finding := range findings {
			if finding.Warning {
				e.warn(rule, leaf.Target, finding.Measured)
			} else {
				violations = append(violations, finding)
			}
		}
		if len(violations) == 0 {
			continue
		}
		if rule.Spec.IsEnforcing() {
			return &Rejection{Path: leaf.Target, Rule: rule.Name, Member: rule.Spec.Use, Origin: last(rule.Origins), Findings: violations}
		}
		for _, finding := range violations {
			e.warn(rule, leaf.Target, findingText(finding, rule, leaf.Target))
		}
	}
	return nil
}

func IsDirConfigPath(target string) bool {
	clean := vfs.CleanPath(target)
	return path.Base(clean) == "config.yaml" && path.Base(path.Dir(clean)) == ".lore"
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
		if rule.Member.Manifest().Scope != ScopeFile || !matchesAtScope(rule, target) {
			continue
		}
		findings, checkErr := rule.Check.Evaluate(ctx, Subject{Mode: ModeValidate, Path: target, Dir: path.Dir(target), Content: content, FS: fsys, BundleRoot: bundleRoot, Bundle: bundle})
		severity := validation.SeverityWarning
		if rule.Spec.IsEnforcing() {
			severity = validation.SeverityError
		}
		if checkErr != nil {
			diagnostics = append(diagnostics, validation.Diagnostic{Path: relative(bundleRoot, target), Line: 1, Column: 1, Severity: severity, Rule: rule.Name, Member: rule.Spec.Use, Message: checkErr.Error()})
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
			diagnostics = append(diagnostics, validation.Diagnostic{Path: p, Line: line, Column: column, Severity: findingSeverity, Rule: finding.Code, Member: rule.Spec.Use, Message: finding.Measured})
		}
	}
	return diagnostics
}

func (e *Engine) Validator() validation.Validator {
	return func(bundle validation.Bundle) []validation.Diagnostic {
		var out []validation.Diagnostic
		invalidDirs := map[string]bool{}
		for _, file := range bundle.Files {
			if !IsDirConfigPath(file.AbsolutePath) || beneathInvalidConfig(file.AbsolutePath, invalidDirs) {
				continue
			}
			dir := path.Dir(path.Dir(file.AbsolutePath))
			if err := e.CheckConfigFile(context.Background(), dir, file.Content); err != nil {
				invalidDirs[dir] = true
				out = append(out, validation.Diagnostic{Path: relative(bundle.Root, file.AbsolutePath), Line: 1, Column: 1, Severity: validation.SeverityError, Rule: "rules/config", Message: err.Error()})
			}
		}
		var bundleRules []CompiledRule
		if !invalidDirs[vfs.CleanPath(bundle.Root)] {
			var err error
			bundleRules, err = e.Effective(context.Background(), bundle.Root)
			if err != nil {
				rule := "rules/config"
				var rootErr *BundleRootError
				if errors.As(err, &rootErr) {
					rule = "rules/bundle-root"
				}
				return []validation.Diagnostic{{Path: bundle.Root, Line: 1, Column: 1, Severity: validation.SeverityError, Rule: rule, Message: err.Error()}}
			}
		}
		for _, file := range bundle.Files {
			if IsDirConfigPath(file.AbsolutePath) || beneathInvalidConfig(file.AbsolutePath, invalidDirs) {
				continue
			}
			out = append(out, e.validateFile(context.Background(), bundle.FS, bundle.Root, file.AbsolutePath, file.Content, &bundle)...)
		}
		for _, rule := range bundleRules {
			if rule.Member.Manifest().Scope != ScopeBundle || !bundleMatches(rule, &bundle) {
				continue
			}
			findings, checkErr := rule.Check.Evaluate(context.Background(), Subject{Mode: ModeValidate, Path: bundle.Root, Dir: bundle.Root, FS: bundle.FS, BundleRoot: bundle.Root, Bundle: &bundle})
			severity := validation.SeverityWarning
			if rule.Spec.IsEnforcing() {
				severity = validation.SeverityError
			}
			if checkErr != nil {
				out = append(out, validation.Diagnostic{Path: ".", Line: 1, Column: 1, Severity: severity, Rule: rule.Name, Member: rule.Spec.Use, Message: checkErr.Error()})
				continue
			}
			for _, finding := range findings {
				findingSeverity := severity
				if finding.Warning {
					findingSeverity = validation.SeverityWarning
				}
				p := finding.Path
				if p == "" {
					p = "."
				}
				if strings.HasPrefix(p, "/") {
					p = relative(bundle.Root, p)
				}
				line, column := finding.Line, finding.Column
				if line == 0 {
					line = 1
				}
				if column == 0 {
					column = 1
				}
				out = append(out, validation.Diagnostic{Path: p, Line: line, Column: column, Severity: findingSeverity, Rule: finding.Code, Member: rule.Spec.Use, Message: finding.Measured})
			}
		}
		SortDiagnostics(out)
		return out
	}
}

func beneathInvalidConfig(target string, invalid map[string]bool) bool {
	for dir := range invalid {
		if target == dir || strings.HasPrefix(target, strings.TrimSuffix(dir, "/")+"/") {
			return true
		}
	}
	return false
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
	if r.Member == "okf" && len(r.Findings) != 0 {
		return fmt.Sprintf("okf: %s: %s\n  see: lore package doc okf", r.Path, r.Findings[0].Measured)
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
	lines := []string{f.Measured}
	if f.Limit != "" {
		lines = append(lines, f.Limit)
	}
	if f.Remedy != "" {
		lines = append(lines, "suggested: "+f.Remedy)
	}
	override := f.Override
	if override == "" {
		if strings.HasPrefix(rule.Spec.Use, "size/") {
			override = fmt.Sprintf("to raise the limit, edit %s in %s", rule.Name, last(rule.Origins))
		} else {
			override = fmt.Sprintf("edit %s in %s", rule.Name, last(rule.Origins))
		}
	}
	if override != "" {
		lines = append(lines, "override: "+override)
	}
	lines = append(lines, "see: lore package doc "+rule.Spec.Use)
	return strings.Join(lines, "\n")
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
