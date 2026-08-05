package openlore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"syscall"

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

const agentSkillsMarker = "user.lore.plugins.openlore.skills.v1"

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
		for _, pm := range ds.Paths {
			r := p.canonical(displayPath(pm))
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

func (p *agentSkillsPlugin) governingRoot(target string) string {
	best := ""
	for _, root := range p.roots() {
		if pathWithinRoot(root, target) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

func (p *agentSkillsPlugin) effective(target string) bool {
	x, ok := p.fs.(vfs.XattrReader)
	if !ok {
		return false
	}
	root := p.governingRoot(target)
	if root == "" {
		return false
	}
	for cur := vfs.CleanPath(target); ; cur = path.Dir(cur) {
		b, err := x.GetXattr(cur, agentSkillsMarker)
		if err == nil {
			return len(b) == 0
		}
		if !errors.Is(err, syscall.ENODATA) {
			return false
		}
		if cur == root {
			return false
		}
	}
}

func (p *agentSkillsPlugin) validateTree(root string) error {
	boundaries := map[string]bool{}
	for _, candidate := range p.roots() {
		if candidate != root && pathWithinRoot(root, candidate) {
			boundaries[candidate] = true
		}
	}
	var findings []string
	err := vfs.WalkDir(p.fs, root, func(target string, info *vfs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if target != root && boundaries[target] && info.IsDir() {
			return fs.SkipDir
		}
		if info.IsDir() || info.Name() != "SKILL.md" {
			return nil
		}
		content, err := p.fs.ReadFile(target)
		if err != nil {
			return err
		}
		result, err := agentskills.Validate(path.Base(path.Dir(target)), content)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", target, err))
		} else if result.Disabled {
			return nil
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("agent_skills: validate %s: %w", root, err)
	}
	if len(findings) != 0 {
		return fmt.Errorf("agent_skills: invalid collection %s: %s", root, strings.Join(findings, "; "))
	}
	return nil
}

func (p *agentSkillsPlugin) validateChange(cs vfs.ChangeSet) error {
	if cs.Action == vfs.ChangeActionMkdir || cs.Action == vfs.ChangeActionMkdirAll {
		return nil
	}
	target := p.canonical(cs.Target)
	if cs.Action == vfs.ChangeActionSetXattr && cs.Xattr != nil && cs.Xattr.Name == agentSkillsMarker {
		if len(cs.Xattr.Value) != 0 {
			return fmt.Errorf("agent_skills: marker value must be empty")
		}
		return p.validateTree(target)
	}
	if cs.Action == vfs.ChangeActionPreserveAndRecreateXattrs && cs.XattrRepair != nil {
		if value, enabled := cs.XattrRepair.Attributes[agentSkillsMarker]; enabled {
			if len(value) != 0 {
				return fmt.Errorf("agent_skills: marker value must be empty")
			}
			return p.validateTree(target)
		}
	}
	if path.Base(target) != "SKILL.md" || !p.effective(path.Dir(target)) {
		return nil
	}
	dir := path.Dir(target)
	skill := target
	// Deleting a skill is explicitly allowed.
	if cs.Action == vfs.ChangeActionRemove || cs.Action == vfs.ChangeActionRemoveAll {
		return nil
	}
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
				p.logger.Error("agent skill validation read failed", "target", cs.Target, "action", cs.Action, "err", err)
			}
			return fmt.Errorf("agent_skills: %s: unable to validate SKILL.md", dir)
		}
		content = b
	}
	if _, err := agentskills.Validate(path.Base(dir), content); err != nil {
		return fmt.Errorf("agent_skills: %s: %w", dir, err)
	}
	return nil
}

// validateMutation is used both by admission middleware and by the serialized
// applier immediately before commit. The latter closes the validation-to-marker
// race for `skills enable`.
func (p *agentSkillsPlugin) validateMutation(cs vfs.ChangeSet) error {
	for _, leaf := range cs.Leaves() {
		if err := p.validateChange(vfs.ChangeSet{
			Target: leaf.Target, Action: leaf.Action, Write: leaf.Write,
			RemoveAll: leaf.RemoveAll, Xattr: leaf.Xattr,
			XattrRepair: leaf.XattrRepair, XattrMigration: leaf.XattrMigration,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *agentSkillsPlugin) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			for _, leaf := range op.Leaves() {
				cs := vfs.ChangeSet{Target: leaf.Target, Action: leaf.Action, Write: leaf.Write, RemoveAll: leaf.RemoveAll, Xattr: leaf.Xattr, XattrRepair: leaf.XattrRepair, XattrMigration: leaf.XattrMigration}
				if err := p.validateChange(cs); err != nil {
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
		if path.Base(abs) == "SKILL.md" && p.effective(path.Dir(abs)) {
			dir := path.Dir(abs)
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
		if path.Base(abs) == "SKILL.md" && p.effective(path.Dir(abs)) {
			trusted, _ := r.Fields["agent_skill"].(bool)
			return trusted
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
