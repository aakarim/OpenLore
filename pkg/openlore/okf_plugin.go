package openlore

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// The OKF plugin validates Open Knowledge Format documents on write. It is a
// built-in default plugin (a sibling of shellexec/inbox): it contributes a
// single admission (pre-commit) middleware that inspects each write's
// ChangeSet, and for targets matching its patterns (default "*.md") runs
// okf.Validate on the proposed bytes. A non-conformant document is rejected
// before it reaches the write log (enforce=true, the default) or logged and let
// through (enforce=false).
//
// Scope is defined on docsets (config.OKFDocsetConfig on a DocsetSpec), never as
// a global block, so OKF scoping reads the exact same display roots as authz and
// can't drift from it. A write is governed by the OKF config of the docset that
// owns its target path — the longest matching display root across all docsets,
// exactly as grantsForPath resolves grants. A child docset therefore includes a
// subtree (child carries OKF) or exempts it (child carries none, shadowing a
// parent's OKF).
//
// It only inspects ChangeActionWrite mutations (the only action carrying bytes);
// mkdir/remove pass straight through. The same okf package backs downstream
// shell commands (e.g. `kb save`/`kb publish`) so validation is identical on
// every path.
//
// It implements WriteMiddlewareProvider, so registerPlugin wires it into the
// admission chain.

const defaultOKFPattern = "*.md"

type okfPlugin struct {
	docsets map[string]config.DocsetSpec
	logger  *slog.Logger
}

// newOKF builds the OKF plugin over the resolved docset set. It resolves the
// effective OKF config per write from these docsets, so it must be constructed
// after auth config is loaded (or the unenforced-mode public docset is
// synthesized).
func newOKF(docsets map[string]config.DocsetSpec, logger *slog.Logger) *okfPlugin {
	if logger == nil {
		logger = slog.Default()
	}
	return &okfPlugin{docsets: docsets, logger: logger}
}

// anyDocsetHasOKF reports whether at least one docset carries OKF config, so the
// server can skip registering the plugin (and its per-write cost) entirely when
// OKF is unused.
func anyDocsetHasOKF(docsets map[string]config.DocsetSpec) bool {
	for _, ds := range docsets {
		if ds.OKF != nil {
			return true
		}
	}
	return false
}

// resolve returns the OKF config of the docset that owns target — the longest
// matching display root across all docsets, identical to authz's grant
// resolution. It returns nil when no docset owns the path, or when the owning
// docset carries no OKF config (a child docset without OKF thus exempts its
// subtree from a parent docset's OKF). Both cases mean "not validated".
func (p *okfPlugin) resolve(target string) *config.OKFDocsetConfig {
	clean := vfs.CleanPath(target)
	bestLen := -1
	var best *config.OKFDocsetConfig
	for _, ds := range p.docsets {
		okfCfg := ds.OKF
		for _, pm := range ds.Paths {
			root := displayPath(pm)
			if pathWithinRoot(root, clean) && len(root) > bestLen {
				bestLen = len(root)
				best = okfCfg
			}
		}
	}
	return best
}

// matches reports whether target's basename matches any of the patterns (empty
// patterns default to "*.md").
func matchesOKFPatterns(target string, patterns []string) bool {
	if len(patterns) == 0 {
		patterns = []string{defaultOKFPattern}
	}
	base := path.Base(vfs.CleanPath(target))
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
	}
	return false
}

// WriteMiddleware implements WriteMiddlewareProvider.
func (p *okfPlugin) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{
		func(next WriteHandler) WriteHandler {
			return func(ctx context.Context, op WriteOp) (WriteResult, error) {
				for _, leaf := range op.Leaves() {
					if leaf.Action == vfs.ChangeActionWrite && leaf.Write != nil {
						if oc := p.resolve(leaf.Target); oc != nil && matchesOKFPatterns(leaf.Target, oc.Patterns) {
							if err := okf.Validate(leaf.Target, leaf.Write.Bytes); err != nil {
								enforce := oc.Enforce == nil || *oc.Enforce
								if enforce {
									return WriteResult{}, fmt.Errorf("okf: %s: %w", leaf.Target, err)
								}
								p.logger.Warn("okf validation failed (non-enforcing)",
									"path", leaf.Target, "err", err)
							}
						}
					}
				}
				return next(ctx, op)
			}
		},
	}
}

// MetaExtenders implements MetaExtenderProvider. It contributes a single
// extender that annotates a `lore meta` record with OKF conformance — but only
// for documents where OKF actually applies (the owning docset has OKF and the
// path matches its patterns), so read-side discovery agrees exactly with
// write-side enforcement. The added field is:
//
//	"okf": {"valid": true}                       // conformant
//	"okf": {"valid": false, "error": "<reason>"} // non-conformant
func (p *okfPlugin) MetaExtenders() []meta.Extender {
	return []meta.Extender{
		func(absPath string, content []byte, _ map[string]any) map[string]any {
			oc := p.resolve(absPath)
			if oc == nil || !matchesOKFPatterns(absPath, oc.Patterns) {
				return nil // OKF does not govern this path
			}
			status := map[string]any{"valid": true}
			if err := okf.Validate(absPath, content); err != nil {
				status["valid"] = false
				status["error"] = err.Error()
			}
			return map[string]any{"okf": status}
		},
	}
}

// Validators implements ValidatorProvider. The command and filesystem scan are
// owned by core; the plugin contributes OKF conformance plus OpenLore's
// operational link and alias-portability checks.
func (p *okfPlugin) Validators() []validation.Validator {
	aliasRoots := p.aliasRoots()
	return []validation.Validator{func(bundle validation.Bundle) []validation.Diagnostic {
		files := make([]okf.File, 0, len(bundle.Files))
		for _, file := range bundle.Files {
			files = append(files, okf.File{Path: file.Path, Content: file.Content})
		}
		okfDiagnostics := okf.ValidateBundle(files)
		diagnostics := make([]validation.Diagnostic, 0, len(okfDiagnostics))
		for _, diagnostic := range okfDiagnostics {
			diagnostics = append(diagnostics, fromOKFDiagnostic(diagnostic))
		}
		for _, file := range bundle.Files {
			if path.Ext(file.Path) != ".md" {
				continue
			}
			for _, link := range okf.Links(file.Content) {
				local, ok := okf.LocalLinkPath(link.Destination)
				if !ok {
					continue
				}
				target := path.Join(path.Dir(file.AbsolutePath), local)
				if strings.HasPrefix(local, "/") {
					target = path.Join(bundle.Root, strings.TrimPrefix(local, "/"))
				}
				target = vfs.CleanPath(target)
				if !pathWithinRoot(bundle.Root, target) {
					diagnostics = append(diagnostics, openLoreDiagnostic(file.Path, link, validation.SeverityError,
						"openlore/link-outside-bundle", fmt.Sprintf("local link %q resolves outside the bundle", link.Destination)))
				} else if _, err := bundle.FS.Stat(target); err != nil {
					diagnostics = append(diagnostics, openLoreDiagnostic(file.Path, link, validation.SeverityError,
						"openlore/broken-link", fmt.Sprintf("local link %q does not resolve", link.Destination)))
				}
				if aliasRoot(file.AbsolutePath, aliasRoots) != "" {
					diagnostics = append(diagnostics, openLoreDiagnostic(file.Path, link, validation.SeverityWarning,
						"openlore/alias-referrer", "link originates from an aliased docset path; use a stable checkout path"))
				}
				if alias := aliasRoot(target, aliasRoots); alias != "" {
					diagnostics = append(diagnostics, openLoreDiagnostic(file.Path, link, validation.SeverityWarning,
						"openlore/alias-target", fmt.Sprintf("link targets aliased docset path %s; it may resolve differently on another machine", alias)))
				}
			}
		}
		return diagnostics
	}}
}

func (p *okfPlugin) aliasRoots() []string {
	var roots []string
	for _, docset := range p.docsets {
		roots = append(roots, docset.Aliases...)
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	return roots
}

func aliasRoot(filePath string, roots []string) string {
	for _, root := range roots {
		if pathWithinRoot(root, filePath) {
			return root
		}
	}
	return ""
}

func openLoreDiagnostic(filePath string, link okf.Link, severity validation.Severity, rule, message string) validation.Diagnostic {
	return validation.Diagnostic{
		Path: filePath, Line: link.Line, Column: link.Column,
		Severity: severity, Rule: rule, Message: message,
	}
}

func fromOKFDiagnostic(diagnostic okf.Diagnostic) validation.Diagnostic {
	return validation.Diagnostic{
		Path: diagnostic.Path, Line: diagnostic.Line, Column: diagnostic.Column,
		Severity: validation.Severity(diagnostic.Severity), Rule: diagnostic.Rule, Message: diagnostic.Message,
	}
}

// Info implements PluginInfoProvider. The version tracks the OKF spec revision
// the validator targets (OKF v0.1).
func (p *okfPlugin) Info() PluginInfo {
	return PluginInfo{Name: "okf", Version: "0.1.0"}
}

var (
	_ WriteMiddlewareProvider = (*okfPlugin)(nil)
	_ MetaExtenderProvider    = (*okfPlugin)(nil)
	_ ValidatorProvider       = (*okfPlugin)(nil)
	_ PluginInfoProvider      = (*okfPlugin)(nil)
)
