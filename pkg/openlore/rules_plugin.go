package openlore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/packagestate"
	"github.com/aakarim/go-openlore/pkg/rules"
	_ "github.com/aakarim/go-openlore/pkg/rules/link"
	_ "github.com/aakarim/go-openlore/pkg/rules/okfrule"
	_ "github.com/aakarim/go-openlore/pkg/rules/size"
	"github.com/aakarim/go-openlore/pkg/rules/tokenizer"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type configRuleLayers struct {
	global  map[string]rules.RuleSpec
	docsets map[string]config.DocsetSpec
}

type folderRuleCacheEntry struct {
	rules map[string]rules.RuleSpec
	err   error
	found bool
}

type folderRuleLayers struct {
	fs      vfs.FileSystem
	docsets map[string]config.DocsetSpec
	mu      sync.RWMutex
	cache   map[string]folderRuleCacheEntry
}

func newFolderRuleLayers(fsys vfs.FileSystem, docsets map[string]config.DocsetSpec) *folderRuleLayers {
	return &folderRuleLayers{fs: fsys, docsets: docsets, cache: map[string]folderRuleCacheEntry{}}
}

func (s *folderRuleLayers) LayersForDir(ctx context.Context, dir string) ([]rules.Layer, error) {
	return s.layersThrough(ctx, dir, true)
}

func (s *folderRuleLayers) LayersAbove(ctx context.Context, dir string) ([]rules.Layer, error) {
	return s.layersThrough(ctx, vfs.CleanPath(dir), false)
}

func (s *folderRuleLayers) layersThrough(_ context.Context, dir string, includeDir bool) ([]rules.Layer, error) {
	root, _, _, ok := owningDocset(s.docsets, dir)
	if !ok || !pathWithinRoot(root, dir) {
		return nil, nil
	}
	dirs := directoriesFrom(root, dir)
	if !includeDir && len(dirs) != 0 {
		dirs = dirs[:len(dirs)-1]
	}
	var layers []rules.Layer
	for _, candidate := range dirs {
		entry := s.load(candidate)
		if entry.err != nil {
			return nil, fmt.Errorf("%s: %w", path.Join(candidate, ".lore/config.yaml"), entry.err)
		}
		if entry.found {
			layers = append(layers, rules.Layer{Origin: path.Join(candidate, ".lore/config.yaml"), Scope: candidate, Rules: entry.rules})
		}
	}
	return layers, nil
}

func directoriesFrom(root, dir string) []string {
	root, dir = vfs.CleanPath(root), vfs.CleanPath(dir)
	result := []string{root}
	if root == dir {
		return result
	}
	rel := strings.TrimPrefix(dir, root)
	if root == "/" {
		rel = strings.TrimPrefix(dir, "/")
	} else {
		rel = strings.TrimPrefix(rel, "/")
	}
	current := root
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" {
			continue
		}
		current = path.Join(current, segment)
		result = append(result, current)
	}
	return result
}

func (s *folderRuleLayers) load(dir string) folderRuleCacheEntry {
	canonicalDir := s.canonicalDir(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[canonicalDir]
	if ok {
		return entry
	}
	configPath := path.Join(canonicalDir, ".lore/config.yaml")
	content, err := s.fs.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			entry = folderRuleCacheEntry{}
		} else {
			entry.err = err
		}
	} else {
		fileConfig, decodeErr := rules.DecodeFile(content)
		if decodeErr != nil {
			entry.err = decodeErr
		} else {
			entry = folderRuleCacheEntry{found: true, rules: fileConfig.Rules}
		}
	}
	s.cache[canonicalDir] = entry
	return entry
}

func (s *folderRuleLayers) canonicalDir(dir string) string {
	dir = vfs.CleanPath(dir)
	bestAlias, target := "", ""
	for _, docset := range s.docsets {
		if len(docset.Paths) == 0 {
			continue
		}
		for _, rawAlias := range docset.Aliases {
			alias := vfs.CleanPath(rawAlias)
			if pathWithinRoot(alias, dir) && len(alias) > len(bestAlias) {
				bestAlias = alias
				target = primaryDisplayPath(docset)
			}
		}
	}
	if bestAlias == "" {
		return dir
	}
	return replacePathRoot(dir, bestAlias, target)
}

func (s *folderRuleLayers) Invalidate(dir string) {
	dir = s.canonicalDir(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	for cached := range s.cache {
		if cached == dir || strings.HasPrefix(cached, strings.TrimSuffix(dir, "/")+"/") {
			delete(s.cache, cached)
		}
	}
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

type rulesPlugin struct {
	engine *rules.Engine
	fs     vfs.FileSystem
}

func newRulesPlugin(auth *config.AuthConfig, defaults rules.Defaults, fsys vfs.FileSystem, logger *slog.Logger) (*rulesPlugin, error) {
	var aliases []string
	for _, docset := range auth.Docsets {
		aliases = append(aliases, docset.Aliases...)
	}
	sort.Slice(aliases, func(i, j int) bool { return len(aliases[i]) > len(aliases[j]) })
	configLayers := configRuleLayers{global: auth.Rules, docsets: auth.Docsets}
	mapper, _ := fsys.(packagestate.HostMapper)
	env := func(member string) rules.Env {
		pkg := strings.SplitN(member, "/", 2)[0]
		return rules.Env{Defaults: defaults, State: packagestate.Open(mapper, pkg), Tokenizer: tokenizer.Estimator{}, Logger: logger, AliasRoots: aliases}
	}
	bootEngine := rules.New(rules.Options{Registry: rules.DefaultRegistry(), Config: configLayers, Env: env, Logger: logger})
	// Compile every configured docset at boot so invalid members, parameters,
	// and unification conflicts fail before the server begins accepting writes.
	for _, docset := range auth.Docsets {
		for _, mapping := range docset.Paths {
			root := displayPath(mapping)
			if _, err := bootEngine.Effective(context.Background(), path.Join(root, "__rules_boot_check__.md")); err != nil {
				return nil, fmt.Errorf("configuring rules: %w", err)
			}
		}
	}
	options := rules.Options{Registry: rules.DefaultRegistry(), Config: configLayers, Env: env, Logger: logger}
	if fsys != nil {
		options.Folders = newFolderRuleLayers(fsys, auth.Docsets)
	}
	engine := rules.New(options)
	return &rulesPlugin{engine: engine, fs: fsys}, nil
}

func (p *rulesPlugin) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			for _, leaf := range op.Leaves() {
				if rules.IsDirConfigPath(leaf.Target) {
					if leaf.Action == vfs.ChangeActionWrite && leaf.Write != nil {
						dir := path.Dir(path.Dir(leaf.Target))
						if err := p.engine.CheckConfigFile(ctx, dir, leaf.Write.Bytes); err != nil {
							return WriteResult{}, err
						}
					}
					continue
				}
				if err := p.engine.AdmitLeaf(ctx, leaf, op.Attribution.String(), p.existing(leaf.Target)); err != nil {
					return WriteResult{}, err
				}
			}
			return next(ctx, op)
		}
	}}
}

// PreApply re-evaluates rules against the serialized filesystem state. This
// closes the gap between concurrent admission and commit, and deliberately
// rejects batches whose folder-config mutation would require a projected view.
func (p *rulesPlugin) PreApply(attribution Attribution, changes vfs.ChangeSet) error {
	leaves := changes.Leaves()
	configChanges := 0
	for _, leaf := range leaves {
		if rules.IsDirConfigPath(leaf.Target) {
			configChanges++
		}
	}
	if configChanges != 0 && len(leaves) != 1 {
		return fmt.Errorf("rules: folder config mutations must be submitted separately")
	}
	for _, leaf := range leaves {
		if rules.IsDirConfigPath(leaf.Target) {
			if leaf.Action == vfs.ChangeActionWrite && leaf.Write != nil {
				if err := p.engine.CheckConfigFile(context.Background(), path.Dir(path.Dir(leaf.Target)), leaf.Write.Bytes); err != nil {
					return err
				}
			}
			continue
		}
		if err := p.engine.AdmitLeaf(context.Background(), leaf, attribution.String(), p.existing(leaf.Target)); err != nil {
			return err
		}
	}
	return nil
}

func (p *rulesPlugin) existing(target string) func() ([]byte, bool, error) {
	return func() ([]byte, bool, error) {
		if p.fs == nil {
			return nil, false, nil
		}
		content, err := p.fs.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return content, err == nil, err
	}
}

func (p *rulesPlugin) invalidateChanges(changes []vfs.Change) {
	for _, leaf := range changes {
		if rules.IsDirConfigPath(leaf.Target) {
			p.engine.Invalidate(path.Dir(path.Dir(leaf.Target)))
		} else if leaf.Action == vfs.ChangeActionRemoveAll {
			p.engine.Invalidate(leaf.Target)
		}
	}
}

func (p *rulesPlugin) PostCommitMiddleware() []PostCommitMiddleware {
	return []PostCommitMiddleware{func(next PostCommitHandler) PostCommitHandler {
		return func(ctx context.Context, info CommitInfo) error {
			ctx = rules.WithActor(ctx, info.Attribution.String())
			p.invalidateChanges(info.ChangeSet.Leaves())
			leaves := info.ChangeSet.Leaves()
			moved := map[int]bool{}
			for i, remove := range leaves {
				if remove.Action != vfs.ChangeActionRemoveAll || remove.RemoveAll == nil || remove.RemoveAll.Opts.Expected == nil {
					continue
				}
				var sourceHash string
				for _, op := range remove.RemoveAll.Opts.Expected.Ops {
					if op.RelPath == "." && op.Kind == "file" {
						sourceHash = op.Hash
					}
				}
				for j, write := range leaves {
					if write.Action == vfs.ChangeActionWrite && write.Write != nil && hashBytes(write.Write.Bytes) == sourceHash {
						if err := p.engine.OnMove(ctx, remove.Target, write.Target); err != nil {
							return err
						}
						moved[i], moved[j] = true, true
						break
					}
				}
			}
			for i, leaf := range leaves {
				if !moved[i] && (leaf.Action == vfs.ChangeActionRemove || leaf.Action == vfs.ChangeActionRemoveAll) {
					if err := p.engine.OnRemove(ctx, leaf.Target); err != nil {
						return err
					}
				}
			}
			return next(ctx, info)
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
	_ PostCommitProvider      = (*rulesPlugin)(nil)
	_ ValidatorProvider       = (*rulesPlugin)(nil)
	_ PluginInfoProvider      = (*rulesPlugin)(nil)
)
