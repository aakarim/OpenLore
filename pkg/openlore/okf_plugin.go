package openlore

import (
	"log/slog"
	"path"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

const defaultOKFPattern = "*.md"

// okfPlugin retains OKF's read-side metadata integration and a compatibility
// validator shim. Write admission and all validation logic live in rulesPlugin.
type okfPlugin struct {
	docsets map[string]config.DocsetSpec
	engine  *rules.Engine
}

func newOKF(docsets map[string]config.DocsetSpec, logger *slog.Logger) *okfPlugin {
	auth := &config.AuthConfig{Docsets: make(map[string]config.DocsetSpec, len(docsets))}
	for name, docset := range docsets {
		copy := docset
		if docset.Rules != nil {
			copy.Rules = make(map[string]rules.RuleSpec, len(docset.Rules))
			for ruleName, spec := range docset.Rules {
				copy.Rules[ruleName] = spec
			}
		}
		auth.Docsets[name] = copy
	}
	_ = config.ValidateAuthConfig(auth)
	plugin, _ := newRulesPlugin(auth, rules.Defaults{Growth: 1.25}, nil, nil, logger)
	return &okfPlugin{docsets: docsets, engine: plugin.engine}
}

func anyDocsetHasOKF(docsets map[string]config.DocsetSpec) bool {
	for _, docset := range docsets {
		if docset.OKF != nil {
			return true
		}
	}
	return false
}

func (p *okfPlugin) resolve(target string) *config.OKFDocsetConfig {
	clean := vfs.CleanPath(target)
	bestLen := -1
	var best *config.OKFDocsetConfig
	for _, docset := range p.docsets {
		for _, mapping := range docset.Paths {
			root := displayPath(mapping)
			if pathWithinRoot(root, clean) && len(root) > bestLen {
				bestLen, best = len(root), docset.OKF
			}
		}
	}
	return best
}

func matchesOKFPatterns(target string, patterns []string) bool {
	if len(patterns) == 0 {
		patterns = []string{defaultOKFPattern}
	}
	base := path.Base(vfs.CleanPath(target))
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

func (p *okfPlugin) MetaExtenders() []meta.Extender {
	return []meta.Extender{func(absPath string, content []byte, _ map[string]any) map[string]any {
		cfg := p.resolve(absPath)
		if cfg == nil || !matchesOKFPatterns(absPath, cfg.Patterns) {
			return nil
		}
		status := map[string]any{"valid": true}
		if err := okf.Validate(absPath, content); err != nil {
			status["valid"], status["error"] = false, err.Error()
		}
		return map[string]any{"okf": status}
	}}
}

// Validators delegates to the same engine used by lore.json admission. It is
// retained for callers that constructed the historical OKF plugin directly.
func (p *okfPlugin) Validators() []validation.Validator {
	if p.engine == nil {
		return nil
	}
	return []validation.Validator{p.engine.Validator()}
}

var (
	_ MetaExtenderProvider = (*okfPlugin)(nil)
	_ ValidatorProvider    = (*okfPlugin)(nil)
)
