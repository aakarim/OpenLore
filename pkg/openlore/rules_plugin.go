package openlore

import (
	"context"
	"fmt"
	"log/slog"
	"path"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/rules/link"
	"github.com/aakarim/go-openlore/pkg/rules/okfrule"
	"github.com/aakarim/go-openlore/pkg/rules/size"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type configRuleLayers struct {
	global  map[string]rules.RuleSpec
	docsets map[string]config.DocsetSpec
}

func (s configRuleLayers) LayersFor(_ context.Context, target string) ([]rules.Layer, error) {
	root, name, docset, ok := owningDocset(s.docsets, target)
	if !ok {
		return nil, nil
	}
	layers := []rules.Layer{{Origin: "lore.json", Scope: root, Rules: s.global}}
	if len(docset.Rules) != 0 {
		layers = append(layers, rules.Layer{Origin: "lore.json#docsets." + name, Scope: root, Rules: docset.Rules})
	}
	return layers, nil
}

func owningDocset(docsets map[string]config.DocsetSpec, target string) (string, string, config.DocsetSpec, bool) {
	bestRoot, bestName, bestLen := "", "", -1
	var best config.DocsetSpec
	for name, docset := range docsets {
		for _, mapping := range docset.Paths {
			root := displayPath(mapping)
			if pathWithinRoot(root, vfs.CleanPath(target)) && len(root) > bestLen {
				bestRoot, bestName, best, bestLen = root, name, docset, len(root)
			}
		}
	}
	return bestRoot, bestName, best, bestLen >= 0
}

type rulesPlugin struct{ engine *rules.Engine }

func newRulesPlugin(auth *config.AuthConfig, defaults rules.Defaults, logger *slog.Logger) (*rulesPlugin, error) {
	registry := rules.NewRegistry()
	size.Register(registry)
	registry.Register(okfrule.Member{})
	registry.Register(okfrule.Member{Bundle: true})
	var aliases []string
	for _, docset := range auth.Docsets {
		aliases = append(aliases, docset.Aliases...)
	}
	registry.Register(link.Member{})
	registry.Register(link.Member{Alias: true, AliasRoots: aliases})
	engine := rules.New(rules.Options{Registry: registry, Config: configRuleLayers{global: auth.Rules, docsets: auth.Docsets}, Env: func(string) rules.Env { return rules.Env{Defaults: defaults, Logger: logger} }, Logger: logger})
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
