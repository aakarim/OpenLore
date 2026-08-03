package openlore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type agentSkillsPlugin struct {
	docsets      map[string]config.DocsetSpec
	fs           vfs.FileSystem
	canonicalize func(string) string
	logger       *slog.Logger
}

func anyDocsetHasAgentSkills(ds map[string]config.DocsetSpec) bool {
	for _, d := range ds {
		if d.AgentSkills {
			return true
		}
	}
	return false
}

func newAgentSkills(ds map[string]config.DocsetSpec, fsys vfs.FileSystem, canonicalize func(string) string, logger *slog.Logger) *agentSkillsPlugin {
	return &agentSkillsPlugin{docsets: ds, fs: fsys, canonicalize: canonicalize, logger: logger}
}

func (p *agentSkillsPlugin) canonical(pth string) string {
	if p.canonicalize != nil {
		return vfs.CleanPath(p.canonicalize(pth))
	}
	if c, ok := p.fs.(vfs.PathCanonicalizer); ok {
		return vfs.CleanPath(c.CanonicalPath(pth))
	}
	return vfs.CleanPath(pth)
}

func (p *agentSkillsPlugin) roots() []string {
	seen := map[string]bool{}
	var out []string
	for _, ds := range p.docsets {
		if ds.AgentSkills {
			for _, pm := range ds.Paths {
				r := p.canonical(displayPath(pm))
				if !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		}
	}
	return out
}

func candidate(root, target string) (string, bool) {
	root, target = vfs.CleanPath(root), vfs.CleanPath(target)
	if !pathWithinRoot(root, target) || target == root {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(target, root), "/")
	first := strings.Split(rel, "/")[0]
	if first == "" || !strings.Contains(rel, "/") {
		return "", false
	}
	return path.Join(root, first), true
}

// validateMutation projects only SKILL.md, the sole constrained resource.
func (p *agentSkillsPlugin) validateMutation(cs vfs.ChangeSet) error {
	if cs.Action == vfs.ChangeActionMkdir || cs.Action == vfs.ChangeActionMkdirAll {
		return nil
	}
	for _, root := range p.roots() {
		dir, ok := candidate(root, cs.Target)
		if !ok {
			continue
		}
		// Removing the whole immediate-child directory is explicitly allowed.
		if (cs.Action == vfs.ChangeActionRemove || cs.Action == vfs.ChangeActionRemoveAll) && vfs.CleanPath(cs.Target) == dir {
			continue
		}
		skill := path.Join(dir, "SKILL.md")
		var content []byte
		if cs.Action == vfs.ChangeActionWrite && vfs.CleanPath(cs.Target) == skill && cs.Write != nil {
			content = cs.Write.Bytes
		} else {
			// Any operation that removes SKILL.md projects it missing.
			if (cs.Action == vfs.ChangeActionRemove || cs.Action == vfs.ChangeActionRemoveAll) && pathWithinRoot(vfs.CleanPath(cs.Target), skill) {
				return fmt.Errorf("agent_skills: %s: SKILL.md is required", dir)
			}
			b, err := p.fs.ReadFile(skill)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("agent_skills: %s: SKILL.md is required", dir)
				}
				if p.logger != nil {
					p.logger.Error("agent skill validation read failed", "target", cs.Target, "action", cs.Action, "root", root, "err", err)
				}
				return fmt.Errorf("agent_skills: %s: unable to validate SKILL.md", dir)
			}
			content = b
		}
		if _, err := agentskills.Validate(path.Base(dir), content); err != nil {
			return fmt.Errorf("agent_skills: %s: %w", dir, err)
		}
	}
	return nil
}

func (p *agentSkillsPlugin) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			for _, leaf := range op.Leaves() {
				cs := vfs.ChangeSet{Target: leaf.Target, Action: leaf.Action, Write: leaf.Write, RemoveAll: leaf.RemoveAll}
				if err := p.validateMutation(cs); err != nil {
					return WriteResult{}, err
				}
			}
			return next(ctx, op)
		}
	}}
}

func (p *agentSkillsPlugin) MetaExtenders() []meta.Extender {
	return []meta.Extender{func(abs string, content []byte, _ map[string]any) map[string]any {
		abs = p.canonical(abs)
		for _, root := range p.roots() {
			dir, ok := candidate(root, abs)
			if !ok || abs != path.Join(dir, "SKILL.md") {
				continue
			}
			r, err := agentskills.Validate(path.Base(dir), content)
			if r.Disabled {
				return nil
			}
			if err != nil {
				return map[string]any{"agent_skill": false, "agent_skill_error": err.Error()}
			}
			return map[string]any{"agent_skill": true}
		}
		return nil
	}}
}

func (p *agentSkillsPlugin) MetaFilters() []meta.Filter {
	return []meta.Filter{{Name: "agent_skills", Aliases: []string{"agent_skill", "skills", "skill"}, Roots: p.roots(), AbsolutePaths: true, Selector: func(abs string, r meta.Record) bool {
		if metadata, ok := r.Fields["metadata"].(map[string]any); ok && metadata["agent_skill"] == "disable" {
			return false
		}
		abs = p.canonical(abs)
		for _, root := range p.roots() {
			dir, ok := candidate(root, abs)
			if ok && abs == path.Join(dir, "SKILL.md") {
				trusted, _ := r.Fields["agent_skill"].(bool)
				return trusted
			}
		}
		return false
	}}}
}

func (*agentSkillsPlugin) Info() PluginInfo {
	return PluginInfo{Name: "agent_skills", Version: "1.0.0"}
}

var _ WriteMiddlewareProvider = (*agentSkillsPlugin)(nil)
var _ MetaExtenderProvider = (*agentSkillsPlugin)(nil)
var _ MetaFilterProvider = (*agentSkillsPlugin)(nil)
