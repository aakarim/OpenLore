package openlore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/skillsremote"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type agentSkillsPlugin struct {
	docsets      map[string]config.DocsetSpec
	fs           vfs.FileSystem
	canonicalize func(string) string
	logger       *slog.Logger
	remote       *skillsremote.Client
	ttl          time.Duration
	timeout      time.Duration
	mu           sync.Mutex
	checks       map[string]remoteState
	failures     map[string]remoteState
	syncLocks    map[string]*sync.Mutex
	submit       func(context.Context, vfs.ChangeSet) error
}

type remoteState struct {
	fingerprint string
	at          time.Time
	message     string
}

func remoteFingerprint(r agentskills.Remote) string {
	return strings.Join([]string{r.Repo, r.Path, r.Ref, r.Commit}, "\x00")
}

const agentSkillsMarker = "user.lore.plugins.openlore.skills.v1"

func newAgentSkills(ds map[string]config.DocsetSpec, fsys vfs.FileSystem, canonicalize func(string) string, logger *slog.Logger, configs ...config.SkillsPluginConfig) *agentSkillsPlugin {
	cfg := config.SkillsPluginConfig{RemoteCheckTTL: 60 * time.Second, RemoteTimeout: 3 * time.Second, RemoteMaxBytes: 10 * 1024 * 1024}
	if len(configs) > 0 {
		cfg = configs[0]
	}
	return &agentSkillsPlugin{docsets: ds, fs: fsys, canonicalize: canonicalize, logger: logger, remote: &skillsremote.Client{HTTP: skillsremote.NewPublicHTTPClient(cfg.RemoteTimeout), GitHubBase: "https://github.com", CodeloadBase: "https://codeload.github.com", MaxBytes: cfg.RemoteMaxBytes, MaxFiles: skillsremote.MaxFiles}, ttl: cfg.RemoteCheckTTL, timeout: cfg.RemoteTimeout, checks: map[string]remoteState{}, failures: map[string]remoteState{}, syncLocks: map[string]*sync.Mutex{}}
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
	if linkedDir, linked := p.effectiveLinkedSkill(target); linked {
		if (cs.Action == vfs.ChangeActionRemoveAll || cs.Action == vfs.ChangeActionRemove) && target == linkedDir {
			return nil
		}
		if cs.Action == vfs.ChangeActionWrite && target == path.Join(linkedDir, "SKILL.md") && cs.Write != nil {
			stored, _ := p.fs.ReadFile(target)
			normalized, allowed, err := agentskills.SurgicalRemoteEdit(stored, cs.Write.Bytes, path.Base(linkedDir))
			if err == nil && allowed && string(normalized) == string(cs.Write.Bytes) {
				if _, err := agentskills.Validate(path.Base(linkedDir), normalized); err != nil {
					return fmt.Errorf("agent_skills: %s: %w", linkedDir, err)
				}
				return nil
			}
		}
		return fmt.Errorf("remote is set; change it upstream or run 'skills remove-remote'")
	}
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

func (p *agentSkillsPlugin) linkedSkill(target string) (string, bool) {
	root := p.governingRoot(target)
	if root == "" {
		return "", false
	}
	for cur := target; ; cur = path.Dir(cur) {
		if info, err := p.fs.Stat(cur); err == nil && !info.IsDir() {
			cur = path.Dir(cur)
		}
		b, err := p.fs.ReadFile(path.Join(cur, "SKILL.md"))
		if err == nil {
			_, ok, _ := agentskills.ReadRemote(b)
			if ok {
				return cur, true
			}
		}
		if cur == root {
			break
		}
	}
	return "", false
}

func (p *agentSkillsPlugin) effectiveLinkedSkill(target string) (string, bool) {
	dir, linked := p.linkedSkill(target)
	return dir, linked && p.effective(dir)
}

func (p *agentSkillsPlugin) syncSkill(ctx context.Context, skillDir string, force bool) {
	skillDir = p.canonical(skillDir)
	p.mu.Lock()
	lock := p.syncLocks[skillDir]
	if lock == nil {
		lock = &sync.Mutex{}
		p.syncLocks[skillDir] = lock
	}
	p.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	skillFile := path.Join(skillDir, "SKILL.md")
	b, err := p.fs.ReadFile(skillFile)
	if err != nil {
		return
	}
	baseHash := hashBytes(b)
	r, linked, err := agentskills.ReadRemote(b)
	if err != nil || !linked {
		return
	}
	originalRepo := r.Repo
	originalFingerprint := remoteFingerprint(r)
	canonicalRepo, err := agentskills.CanonicalRepoURL(originalRepo)
	if err != nil {
		p.setRemoteFailure(skillDir, originalFingerprint, fmt.Sprintf("invalid remote repository: %v", err))
		return
	}
	r.Repo = canonicalRepo
	fingerprint := remoteFingerprint(r)
	p.mu.Lock()
	check := p.checks[skillDir]
	p.mu.Unlock()
	if canonicalRepo != originalRepo {
		canonical, err := agentskills.GraftRemote(b, r)
		if err != nil || p.submit == nil {
			p.setRemoteFailure(skillDir, originalFingerprint, "remote canonicalization failed")
			return
		}
		if err := p.submit(ctx, vfs.ChangeSet{Target: skillFile, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: canonical, Opts: vfs.WriteOpts{IfMatch: &baseHash}}}); err != nil {
			p.setRemoteFailure(skillDir, originalFingerprint, fmt.Sprintf("remote canonicalization failed: %v", err))
			return
		}
		b = canonical
		baseHash = hashBytes(b)
	}
	p.mu.Lock()
	if failure := p.failures[skillDir]; failure.fingerprint != "" && failure.fingerprint != fingerprint {
		delete(p.failures, skillDir)
	}
	p.mu.Unlock()
	if !force && r.Commit != "" && r.Kind == "pinned" {
		return
	}
	if !force && r.Commit != "" && check.fingerprint == fingerprint && time.Since(check.at) < p.ttl {
		return
	}
	p.mu.Lock()
	p.checks[skillDir] = remoteState{fingerprint: fingerprint, at: time.Now()}
	p.mu.Unlock()
	refs, err := p.remote.Resolve(ctx, r.Repo)
	if err != nil {
		p.setRemoteFailure(skillDir, fingerprint, fmt.Sprintf("remote unreachable; serving stored version (commit %.7s)", r.Commit))
		return
	}
	sha, kind, resolvedRef, err := refs.Resolve(r.Ref)
	if err != nil {
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	if r.Ref == "" {
		r.Ref = resolvedRef
	}
	if sha == r.Commit && r.Kind == kind {
		p.setRemoteFailure(skillDir, fingerprint, "")
		return
	}
	files, err := p.remote.Fetch(ctx, r.Repo, sha, r.Path)
	if err != nil {
		p.setRemoteFailure(skillDir, fingerprint, fmt.Sprintf("upstream at %.7s is not a valid skill; update refused, serving stored version (commit %.7s)", sha, r.Commit))
		return
	}
	if err := skillsremote.ValidateFiles(files); err != nil {
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	upstream, ok := files["SKILL.md"]
	if !ok {
		p.setRemoteFailure(skillDir, fingerprint, "upstream has no SKILL.md; update refused")
		return
	}
	_, upstream, err = agentskills.Normalize(path.Base(skillDir), upstream)
	if err != nil {
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	r.Commit = sha
	r.Kind = kind
	files["SKILL.md"], err = agentskills.GraftRemote(upstream, r)
	if err != nil {
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	validation, err := agentskills.Validate(path.Base(skillDir), files["SKILL.md"])
	if err != nil || validation.Disabled {
		if err == nil {
			err = fmt.Errorf("remote skill cannot be disabled")
		}
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	desiredDirs := map[string]bool{}
	for rel := range files {
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			desiredDirs[dir] = true
		}
	}
	type entry struct {
		target, rel string
		dir         bool
	}
	var existing []entry
	if err := vfs.WalkDir(p.fs, skillDir, func(target string, info *vfs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if target != skillDir {
			existing = append(existing, entry{target: target, rel: strings.TrimPrefix(target, skillDir+"/"), dir: info.IsDir()})
		}
		return nil
	}); err != nil {
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	sort.Slice(existing, func(i, j int) bool { return strings.Count(existing[i].rel, "/") < strings.Count(existing[j].rel, "/") })
	var removals []vfs.Change
	removedRoots := map[string]bool{}
	for _, e := range existing {
		stale := false
		if e.dir {
			_, stale = files[e.rel]
			stale = stale || !desiredDirs[e.rel]
		} else {
			_, keep := files[e.rel]
			stale = !keep || desiredDirs[e.rel]
		}
		if !stale {
			continue
		}
		covered := false
		for root := range removedRoots {
			if pathWithinRoot(root, e.target) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if e.dir {
			removals = append(removals, vfs.Change{Target: e.target, Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{Opts: vfs.RemoveOpts{}}})
			removedRoots[e.target] = true
		} else {
			removals = append(removals, vfs.Change{Target: e.target, Action: vfs.ChangeActionRemove})
		}
	}
	sort.Slice(removals, func(i, j int) bool {
		return strings.Count(removals[i].Target, "/") > strings.Count(removals[j].Target, "/")
	})
	dirs := map[string]bool{}
	var rels []string
	for rel := range files {
		rels = append(rels, rel)
		if dir := path.Dir(rel); dir != "." {
			dirs[path.Join(skillDir, dir)] = true
		}
	}
	var orderedDirs []string
	for dir := range dirs {
		orderedDirs = append(orderedDirs, dir)
	}
	sort.Slice(orderedDirs, func(i, j int) bool {
		if strings.Count(orderedDirs[i], "/") == strings.Count(orderedDirs[j], "/") {
			return orderedDirs[i] < orderedDirs[j]
		}
		return strings.Count(orderedDirs[i], "/") < strings.Count(orderedDirs[j], "/")
	})
	sort.Slice(rels, func(i, j int) bool {
		if rels[i] == "SKILL.md" {
			return false
		}
		if rels[j] == "SKILL.md" {
			return true
		}
		return rels[i] < rels[j]
	})
	changes := make([]vfs.Change, 0, 1+len(orderedDirs)+len(rels)+len(removals))
	changes = append(changes, vfs.Change{Target: skillFile, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: b, Opts: vfs.WriteOpts{IfMatch: &baseHash}}})
	changes = append(changes, removals...)
	for _, dir := range orderedDirs {
		changes = append(changes, vfs.Change{Target: dir, Action: vfs.ChangeActionMkdirAll})
	}
	for _, rel := range rels {
		data := files[rel]
		changes = append(changes, vfs.Change{Target: path.Join(skillDir, rel), Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: data}})
	}
	if p.submit == nil {
		p.setRemoteFailure(skillDir, fingerprint, "remote sync unavailable")
		return
	}
	current, err := p.fs.ReadFile(skillFile)
	if err != nil || hashBytes(current) != baseHash {
		p.setRemoteFailure(skillDir, fingerprint, "SKILL.md changed during remote sync; update refused")
		return
	}
	err = p.submit(ctx, vfs.ChangeSet{Changes: changes})
	if err != nil {
		p.setRemoteFailure(skillDir, fingerprint, err.Error())
		return
	}
	p.setRemoteFailure(skillDir, remoteFingerprint(r), "")
}

func (p *agentSkillsPlugin) updateRemoteSkill(ctx context.Context, skillDir string) (string, string, error) {
	skillDir = p.canonical(skillDir)
	before, err := p.fs.ReadFile(path.Join(skillDir, "SKILL.md"))
	if err != nil {
		return "", "", err
	}
	oldRemote, linked, err := agentskills.ReadRemote(before)
	if err != nil {
		return "", "", err
	}
	if !linked {
		return "", "", fmt.Errorf("skill is not linked to a remote")
	}
	p.syncSkill(ctx, skillDir, true)
	after, err := p.fs.ReadFile(path.Join(skillDir, "SKILL.md"))
	if err != nil {
		return oldRemote.Commit, "", err
	}
	newRemote, linked, err := agentskills.ReadRemote(after)
	if err != nil || !linked {
		return oldRemote.Commit, "", fmt.Errorf("skill remote changed during update")
	}
	p.mu.Lock()
	failure := p.failures[skillDir]
	p.mu.Unlock()
	if failure.fingerprint == remoteFingerprint(newRemote) && failure.message != "" {
		return oldRemote.Commit, newRemote.Commit, errors.New(failure.message)
	}
	return oldRemote.Commit, newRemote.Commit, nil
}

func (p *agentSkillsPlugin) setRemoteFailure(dir, fingerprint, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if msg == "" {
		delete(p.failures, dir)
	} else {
		p.failures[dir] = remoteState{fingerprint: fingerprint, message: msg}
	}
}

func (p *agentSkillsPlugin) ReadMiddleware() []ReadMiddleware {
	return []ReadMiddleware{func(next ReadHandler) ReadHandler {
		return func(ctx context.Context, op ReadOp) error {
			if op.Kind == ReadKindFile && path.Base(op.Path) == "SKILL.md" && p.effective(path.Dir(op.Path)) {
				p.syncSkill(ctx, path.Dir(op.Path), false)
			}
			return next(ctx, op)
		}
	}}
}
func (p *agentSkillsPlugin) ContentTransforms() []ContentTransform {
	return []ContentTransform{func(target string, content []byte) []byte {
		if path.Base(target) != "SKILL.md" {
			return content
		}
		_, linked, _ := agentskills.ReadRemote(content)
		if !linked {
			p.setRemoteFailure(path.Dir(p.canonical(target)), "", "")
			return content
		}
		p.mu.Lock()
		failure := p.failures[path.Dir(p.canonical(target))]
		p.mu.Unlock()
		remote, _, _ := agentskills.ReadRemote(content)
		if failure.message != "" && failure.fingerprint == remoteFingerprint(remote) {
			if b, err := agentskills.InjectRemoteStatus(content, failure.message); err == nil {
				return b
			}
		}
		return content
	}}
}

// validateMutation is used both by admission middleware and by the serialized
// applier immediately before commit. The latter closes the validation-to-marker
// race for `skills enable`.
func (p *agentSkillsPlugin) validateMutation(attribution Attribution, cs vfs.ChangeSet) error {
	if attribution.internal && attribution.Principal == "agent_skills_remote" {
		return nil
	}
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
			normalized := cloneWriteChangeSet(op.changeSet)
			leaves := normalized.Leaves()
			for i := range leaves {
				leaf := &leaves[i]
				target := p.canonical(leaf.Target)
				linkedDir, linked := p.effectiveLinkedSkill(target)
				if !linked || leaf.Action != vfs.ChangeActionWrite || leaf.Write == nil || target != path.Join(linkedDir, "SKILL.md") {
					continue
				}
				stored, err := p.fs.ReadFile(target)
				if err != nil {
					return WriteResult{}, err
				}
				content, allowed, err := agentskills.SurgicalRemoteEdit(stored, leaf.Write.Bytes, path.Base(linkedDir))
				if err != nil {
					return WriteResult{}, err
				}
				if !allowed {
					continue
				}
				if _, err := agentskills.Validate(path.Base(linkedDir), content); err != nil {
					return WriteResult{}, err
				}
				write := *leaf.Write
				write.Bytes = content
				leaf.Write = &write
			}
			if len(normalized.Changes) > 0 {
				normalized.Changes = leaves
			} else if len(leaves) == 1 {
				normalized.Write = leaves[0].Write
			}
			op = NewWriteOp(op.Attribution, normalized)
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
