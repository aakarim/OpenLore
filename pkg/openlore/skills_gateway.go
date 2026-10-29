package openlore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"

	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/openlore/skillsremote"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// The installed SKILL.md remains authoritative for tracking. These records
// retain ancestry after detach/removal and link installations to raw snapshots.
type skillInstallation struct {
	Remote      agentskills.Remote `json:"remote"`
	DerivedFrom agentskills.Remote `json:"derived_from"`
	Snapshot    string             `json:"snapshot,omitempty"`
	Detached    bool               `json:"detached,omitempty"`
	Removed     bool               `json:"removed,omitempty"`
	Rejections  []skillRejection   `json:"rejections,omitempty"`
}

type skillRejection struct {
	Commit   string `json:"commit"`
	Snapshot string `json:"snapshot,omitempty"`
	Message  string `json:"message"`
}

func skillSourceStore(repo, subtree string) (string, error) {
	canonical, err := agentskills.CanonicalRepoURL(repo)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(canonical)
	host := u.Hostname()
	if port := u.Port(); port != "" {
		host += "_" + port
	}
	// Keep server-owned public archives separate from client-owned Git stores.
	// Ref selection resolves to a commit before lookup; never install "latest".
	return "skills/" + host + u.Path + "/" + hashContent([]byte(subtree)), nil
}

func skillSourceSnapshot(state sourceState, commit string) SourceSnapshot {
	for _, snap := range state.Snapshots {
		if snap.Meta["commit"] == commit {
			return snap
		}
	}
	return SourceSnapshot{}
}

func (d *Docstore) LoadSkillArchive(ctx context.Context, repo, commit, subtree string) (map[string][]byte, bool, error) {
	store, err := skillSourceStore(repo, subtree)
	if err != nil {
		return nil, false, err
	}
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	state, err := d.state(store)
	if err != nil {
		return nil, false, err
	}
	snap := skillSourceSnapshot(state, commit)
	if snap.Snapshot == "" {
		return nil, false, nil
	}
	files := map[string][]byte{}
	for name, f := range snap.Files {
		r, _, err := d.blobs.Get(ctx, f.SHA256)
		if err != nil {
			return nil, false, err
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, false, err
		}
		files[name] = b
	}
	return files, true, nil
}

func (d *Docstore) SaveSkillArchive(ctx context.Context, repo, commit, subtree string, files map[string][]byte) error {
	if err := skillsremote.ValidateFiles(files); err != nil {
		return err
	}
	store, err := skillSourceStore(repo, subtree)
	if err != nil {
		return err
	}
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	state, err := d.state(store)
	if err != nil {
		return err
	}
	if skillSourceSnapshot(state, commit).Snapshot != "" {
		return nil
	}
	manifest := map[string]SourceFile{}
	for name, b := range files {
		hash := hashContent(b)
		if err := d.blobs.Put(ctx, hash, bytes.NewReader(b)); err != nil {
			return err
		}
		manifest[name] = SourceFile{SHA256: hash, Size: int64(len(b)), Mode: 0644}
	}
	// Extraction has already enforced the skills download limits. As with
	// native writes, do not impose the client-upload policy on server retrieval.
	req := SnapshotRequest{Files: manifest, Parent: latestSnapshot(state).Snapshot, Meta: map[string]string{"type": "skill", "origin": repo, "subpath": subtree, "commit": commit}}
	id := hashContent([]byte(store + "\x00" + commit))
	state.Owner = "agent_skills_remote"
	state.Pending[id] = SourceSnapshot{SnapshotRequest: req, Snapshot: id}
	if err := d.save(state); err != nil {
		return err
	}
	_, err = d.commit(ctx, store, state.Owner, id)
	return err
}

// reconcileInstallations runs at startup and in the ordered commit journal.
// Startup records legacy installs without fetching or inventing raw snapshots.
func (p *agentSkillsPlugin) reconcileInstallations(cs *vfs.ChangeSet) error {
	if p.gateway == nil {
		return nil
	}
	targets := map[string]bool{}
	var roots []string
	if cs == nil {
		// The only all-source scan: rebuild the derived directory index once
		// at startup, never on the ordered content-commit path.
		docstoreMu.Lock()
		states, err := p.gateway.states()
		if err == nil {
			p.installations = map[string]map[string]bool{}
			for _, state := range states {
				for dir := range state.Installations {
					if p.installations[dir] == nil {
						p.installations[dir] = map[string]bool{}
					}
					p.installations[dir][state.Store] = true
					targets[dir] = true
				}
			}
		}
		docstoreMu.Unlock()
		if err != nil {
			return err
		}
		roots = p.roots()
	} else {
		for _, leaf := range cs.Leaves() {
			if path.Base(leaf.Target) == "SKILL.md" && p.effective(path.Dir(leaf.Target)) {
				targets[p.canonical(path.Dir(leaf.Target))] = true
			}
			if leaf.Action == vfs.ChangeActionSetXattr && leaf.Xattr != nil && leaf.Xattr.Name == agentSkillsMarker {
				roots = append(roots, leaf.Target)
			}
			if leaf.Action == vfs.ChangeActionPreserveAndRecreateXattrs && leaf.XattrRepair != nil {
				if _, enabled := leaf.XattrRepair.Attributes[agentSkillsMarker]; enabled {
					roots = append(roots, leaf.Target)
				}
			}
			// Only tree/file deletion or a SKILL.md mutation can invalidate an
			// existing installation. Ordinary edits need no history lookup.
			if leaf.Action == vfs.ChangeActionRemove || leaf.Action == vfs.ChangeActionRemoveAll || path.Base(leaf.Target) == "SKILL.md" {
				docstoreMu.Lock()
				for dir := range p.installations {
					if pathWithinRoot(p.canonical(leaf.Target), path.Join(dir, "SKILL.md")) {
						targets[dir] = true
					}
				}
				docstoreMu.Unlock()
			}
		}
	}
	if len(targets) == 0 && len(roots) == 0 {
		return nil
	}
	if err := p.discoverInstallations(roots, targets); err != nil {
		return err
	}
	for dir := range targets {
		b, readErr := p.fs.ReadFile(path.Join(dir, "SKILL.md"))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		remote, linked, parseErr := agentskills.ReadRemote(b)
		linked = readErr == nil && parseErr == nil && linked
		if err := p.reconcileInstallation(dir, remote, linked, readErr == nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *agentSkillsPlugin) discoverInstallations(roots []string, targets map[string]bool) error {
	visited := map[string]bool{}
	docsets := p.docsets
	if p.liveDocsets != nil {
		docsets = p.liveDocsets()
	}
	for _, ds := range docsets {
		if ds.Projection != nil {
			for _, mapping := range ds.Paths {
				visited[p.canonical(displayPath(mapping))] = true
			}
		}
	}
	var walk func(string) error
	walk = func(dir string) error {
		dir = p.canonical(dir)
		if visited[dir] {
			return nil
		}
		visited[dir] = true
		info, err := p.fs.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		entries, err := p.fs.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				if entry.Name() == ".lore" {
					continue
				}
				if err := walk(path.Join(dir, entry.Name())); err != nil {
					return err
				}
			} else if entry.Name() == "SKILL.md" && p.effective(dir) {
				targets[dir] = true
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := walk(root); err != nil {
			return fmt.Errorf("registering skills in %s: %w", root, err)
		}
	}
	return nil
}

func (p *agentSkillsPlugin) reconcileInstallation(dir string, remote agentskills.Remote, linked, exists bool) error {
	var currentStore string
	var err error
	if linked {
		currentStore, err = skillSourceStore(remote.Repo, remote.Path)
		if err != nil {
			return err
		}
	}
	// Never read the VFS under this lock: a projection itself uses docstoreMu.
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	var derivedFrom agentskills.Remote
	var appliedSnapshot string
	for store := range p.installations[dir] {
		state, err := p.gateway.state(store)
		if err != nil {
			return err
		}
		old, found := state.Installations[dir]
		if found && !old.Removed {
			derivedFrom = old.DerivedFrom
			appliedSnapshot = old.Snapshot
		}
		if !found || state.Store == currentStore {
			continue
		}
		if !old.Detached && !old.Removed {
			old.Detached = !linked && exists
		}
		old.Removed = old.Removed || !exists || linked
		state.Installations[dir] = old
		if err := p.gateway.save(state); err != nil {
			return err
		}
	}
	if !linked {
		return nil
	}
	state, err := p.gateway.state(currentStore)
	if err != nil {
		return err
	}
	if state.Installations == nil {
		state.Installations = map[string]skillInstallation{}
	}
	old := state.Installations[dir]
	old.Remote, old.Detached, old.Removed = remote, false, false
	if remote.Commit != "" {
		old.DerivedFrom = remote
		old.Snapshot = skillSourceSnapshot(state, remote.Commit).Snapshot
	} else {
		// Retargeting a link clears its commit before a new version is applied.
		// It must not erase where the files still on disk actually came from.
		old.DerivedFrom = derivedFrom
		old.Snapshot = appliedSnapshot
	}
	state.Installations[dir] = old
	if state.Owner == "" {
		state.Owner = "agent_skills_remote"
	}
	if err := p.gateway.save(state); err != nil {
		return err
	}
	if p.installations == nil {
		p.installations = map[string]map[string]bool{}
	}
	if p.installations[dir] == nil {
		p.installations[dir] = map[string]bool{}
	}
	p.installations[dir][currentStore] = true
	return nil
}

// Run metadata-only refreshes on the content applier. Checking durable bytes
// there prevents a cached no-op from restoring a concurrently detached link.
func (p *agentSkillsPlugin) backfillSnapshot(ctx context.Context, dir, expectedHash string) error {
	if p.gateway == nil {
		return nil
	}
	update := func() error {
		b, err := p.fs.ReadFile(path.Join(dir, "SKILL.md"))
		if err != nil {
			return err
		}
		if hashBytes(b) != expectedHash {
			return fmt.Errorf("SKILL.md changed during remote sync; update refused")
		}
		remote, linked, err := agentskills.ReadRemote(b)
		if err != nil {
			return err
		}
		if !linked {
			return fmt.Errorf("skill remote changed during update")
		}
		return p.reconcileInstallation(dir, remote, true, true)
	}
	if p.ordered != nil {
		return p.ordered(ctx, update)
	}
	return update()
}

func (p *agentSkillsPlugin) recordRejection(dir string, remote agentskills.Remote) {
	if p.gateway == nil {
		return
	}
	p.mu.Lock()
	failure := p.failures[dir]
	p.mu.Unlock()
	if failure.message == "" {
		return
	}
	store, err := skillSourceStore(remote.Repo, remote.Path)
	if err == nil {
		docstoreMu.Lock()
		defer docstoreMu.Unlock()
		var state sourceState
		state, err = p.gateway.state(store)
		if err == nil {
			installation, exists := state.Installations[dir]
			if !exists {
				return
			}
			rejection := skillRejection{Commit: remote.Commit, Snapshot: skillSourceSnapshot(state, remote.Commit).Snapshot, Message: failure.message}
			for _, old := range installation.Rejections {
				if old == rejection {
					return
				}
			}
			installation.Rejections = append(installation.Rejections, rejection)
			state.Installations[dir] = installation
			err = p.gateway.save(state)
		}
	}
	if err != nil {
		p.logger.Error("recording skill rejection", "skill", dir, "error", err)
	}
}

// Called only through the session filesystem, after checking that the caller
// can read this installation. Do not expose another collection's install paths.
func (p *agentSkillsPlugin) installationInfo(dir string) []map[string]any {
	if p.gateway == nil {
		return nil
	}
	dir = p.canonical(dir)
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	var records []map[string]any
	for store := range p.installations[dir] {
		state, err := p.gateway.state(store)
		if err != nil {
			return nil
		}
		if install, ok := state.Installations[dir]; ok {
			records = append(records, map[string]any{"type": "installation", "path": dir, "store": state.Store, "installation": install})
		}
	}
	return records
}
