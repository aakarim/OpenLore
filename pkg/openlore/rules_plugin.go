package openlore

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/rules"
	_ "github.com/aakarim/go-openlore/pkg/rules/link"
	_ "github.com/aakarim/go-openlore/pkg/rules/okfrule"
	_ "github.com/aakarim/go-openlore/pkg/rules/size"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type configRuleLayers struct {
	global  map[string]rules.RuleSpec
	docsets map[string]config.DocsetSpec
}

func (s configRuleLayers) LayersFor(_ context.Context, target string) ([]rules.Layer, error) {
	root, name, docset, ok := owningDocset(s.docsets, target)
	if !ok {
		var governed []string
		for docsetName, candidate := range s.docsets {
			for _, root := range ruleRoots(candidate) {
				if pathWithinRoot(vfs.CleanPath(target), root) && (hasBundleRule(s.global) || hasBundleRule(candidate.Rules)) {
					governed = append(governed, docsetName)
					break
				}
			}
		}
		if len(governed) != 0 {
			sort.Strings(governed)
			return nil, &rules.BundleRootError{Root: vfs.CleanPath(target), Docsets: governed}
		}
		return nil, nil
	}
	layers := []rules.Layer{{Origin: "lore.json", Scope: root, Rules: s.global}}
	if len(docset.Rules) != 0 {
		layers = append(layers, rules.Layer{Origin: "lore.json#docsets." + name, Scope: root, Rules: docset.Rules})
	}
	return layers, nil
}

func hasBundleRule(specs map[string]rules.RuleSpec) bool {
	for _, spec := range specs {
		switch spec.Use {
		case "okf/bundle", "link/resolves", "link/alias":
			return true
		}
	}
	return false
}

func owningDocset(docsets map[string]config.DocsetSpec, target string) (string, string, config.DocsetSpec, bool) {
	bestRoot, bestName, bestLen := "", "", -1
	var best config.DocsetSpec
	for name, docset := range docsets {
		for _, root := range ruleRoots(docset) {
			if pathWithinRoot(root, vfs.CleanPath(target)) && len(root) > bestLen {
				bestRoot, bestName, best, bestLen = root, name, docset, len(root)
			}
		}
	}
	return bestRoot, bestName, best, bestLen >= 0
}

func ruleRoots(docset config.DocsetSpec) []string {
	roots := append([]string(nil), docset.Aliases...)
	for _, mapping := range docset.Paths {
		roots = append(roots, displayPath(mapping))
	}
	return roots
}

type rulesPlugin struct{ engine *rules.Engine }

func newRulesPlugin(auth *config.AuthConfig, defaults rules.Defaults, logger *slog.Logger) (*rulesPlugin, error) {
	var aliases []string
	for _, docset := range auth.Docsets {
		aliases = append(aliases, docset.Aliases...)
	}
	sort.Slice(aliases, func(i, j int) bool { return len(aliases[i]) > len(aliases[j]) })
	engine := rules.New(rules.Options{Registry: rules.DefaultRegistry(), Config: configRuleLayers{global: auth.Rules, docsets: auth.Docsets}, Env: func(string) rules.Env { return rules.Env{Defaults: defaults, Logger: logger, AliasRoots: aliases} }, Logger: logger})
	// Compile every configured docset at boot so invalid members, parameters,
	// and unification conflicts fail before the server begins accepting writes.
	for _, docset := range auth.Docsets {
		for _, mapping := range docset.Paths {
			root := displayPath(mapping)
			if _, err := engine.Effective(context.Background(), path.Join(root, "__rules_boot_check__.md")); err != nil {
				return nil, fmt.Errorf("configuring rules: %w", err)
			}
		}
	}
	return &rulesPlugin{engine: engine}, nil
}

func (p *rulesPlugin) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			for _, leaf := range op.Leaves() {
				if err := p.engine.AdmitLeaf(ctx, leaf, op.Attribution.String(), nil); err != nil {
					return WriteResult{}, err
				}
			}
			return next(ctx, op)
		}
	}}
}
func (p *rulesPlugin) Validators() []validation.Validator {
	return []validation.Validator{p.engine.Validator()}
}
func (p *rulesPlugin) Info() PluginInfo { return PluginInfo{Name: "rules", Version: "0.1.0"} }

func anyConfiguredRules(auth *config.AuthConfig) bool {
	if len(auth.Rules) != 0 {
		return true
	}
	for _, docset := range auth.Docsets {
		if len(docset.Rules) != 0 {
			return true
		}
	}
	return false
}

var (
	_ WriteMiddlewareProvider = (*rulesPlugin)(nil)
	_ ValidatorProvider       = (*rulesPlugin)(nil)
	_ PluginInfoProvider      = (*rulesPlugin)(nil)
)
