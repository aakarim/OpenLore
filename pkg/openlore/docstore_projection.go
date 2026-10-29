package openlore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// projectionFS exposes only a filtered manifest and has no writable substrate.
// Each operation reads one atomically published source state.
type projectionFS struct {
	store *Docstore
	spec  config.ProjectionSpec
}

func (s *Server) mountProjections() error {
	mounts, err := s.projectionMounts(s.auth)
	if err != nil {
		return err
	}
	s.initialProjections = mounts
	s.merge.projections = func() map[string]vfs.FileSystem {
		if live := s.runtimeConfig.Load(); live != nil {
			return live.projections
		}
		return mounts
	}
	return nil
}

func (p *projectionFS) manifest() (SourceSnapshot, error) {
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	s, err := p.store.state(p.spec.Store)
	if err != nil {
		return SourceSnapshot{}, err
	}
	snap := latestSnapshot(s)
	filtered := map[string]SourceFile{}
	for name, f := range snap.Files {
		if p.spec.Subpath != "" {
			prefix := p.spec.Subpath + "/"
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if config.SourcePathSelected(p.spec.Include, p.spec.Exclude, name) {
			filtered[name] = f
		}
	}
	snap.Files = filtered
	return snap, nil
}
func (p *projectionFS) Stat(name string) (*vfs.FileInfo, error) {
	snap, err := p.manifest()
	if err != nil {
		return nil, err
	}
	return projectionStat(snap, name)
}
func projectionStat(snap SourceSnapshot, name string) (*vfs.FileInfo, error) {
	name = strings.TrimPrefix(vfs.CleanPath(name), "/")
	info := &vfs.FileInfo{FileName: path.Base(name), FilePath: "/" + name, FileModTime: snap.Time}
	if name == "" {
		info.Dir = true
		info.FileName = "/"
		return info, nil
	}
	if f, ok := snap.Files[name]; ok {
		info.FileSize = f.Size
		return info, nil
	}
	for f := range snap.Files {
		if strings.HasPrefix(f, name+"/") {
			info.Dir = true
			return info, nil
		}
	}
	return nil, os.ErrNotExist
}
func (p *projectionFS) ReadDir(name string) ([]vfs.FileInfo, error) {
	snap, err := p.manifest()
	if err != nil {
		return nil, err
	}
	info, err := projectionStat(snap, name)
	if err != nil {
		return nil, err
	}
	if !info.Dir {
		return nil, fmt.Errorf("not a directory: %s", name)
	}
	prefix := strings.TrimPrefix(vfs.CleanPath(name), "/")
	if prefix != "" {
		prefix += "/"
	}
	seen := map[string]bool{}
	out := []vfs.FileInfo{}
	for f := range snap.Files {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		child := strings.SplitN(strings.TrimPrefix(f, prefix), "/", 2)[0]
		if seen[child] {
			continue
		}
		seen[child] = true
		entry, err := projectionStat(snap, prefix+child)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	return out, nil
}
func (p *projectionFS) ReadFile(name string) ([]byte, error) {
	snap, err := p.manifest()
	if err != nil {
		return nil, err
	}
	f, ok := snap.Files[strings.TrimPrefix(vfs.CleanPath(name), "/")]
	if !ok {
		return nil, os.ErrNotExist
	}
	r, _, err := p.store.blobs.Get(context.Background(), f.SHA256)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (s *Server) mutationDenied(action vfs.ChangeAction, target string) error {
	if _, ds, ok := s.mostSpecificDocset(target); ok && ds.Projection != nil {
		return fmt.Errorf("source %s is a read-only source copy: %w", ds.Projection.Store, vfs.ErrReadOnly)
	}
	return mutationDeniedError(s.merge, action, target)
}

type sourceView struct {
	Store       string         `json:"store"`
	Owner       string         `json:"owner"`
	Projections []string       `json:"projections"`
	Latest      SourceSnapshot `json:"latest"`
}

// visibleSource preserves original source-relative filenames for the API but
// filters both current and historical manifests through one captured policy.
// A nil predicate allows the complete source (owned imports or public skills).
func (s *Server) visibleSource(id Identity, state sourceState) (sourceView, func(string) bool, bool) {
	s = s.readView()
	id = s.liveReadIdentity(id)
	view := sourceView{Store: state.Store, Owner: state.Owner, Latest: latestSnapshot(state), Projections: []string{}}
	scope := newScopedReadFS(nil, s.readableRoots(id), s.allDocsetRoots())
	var native *config.DocsetSpec
	var projections []config.DocsetSpec
	if strings.HasPrefix(state.Store, "skills/") {
		// Public upstream bytes are shared, but source discovery still follows
		// collection access. Never expose the private installation registry.
		for dir := range state.Installations {
			if name, _, ok := s.mostSpecificDocset(dir); ok {
				if _, allowed := s.effectiveGrantNames(id, name); allowed {
					return view, nil, true
				}
			}
		}
	}
	if strings.HasPrefix(state.Store, "openlore/") {
		name := strings.TrimPrefix(state.Store, "openlore/")
		if ds, ok := s.currentAuth().Docsets[name]; ok && ds.Projection == nil {
			if _, ok := s.effectiveGrantNames(id, name); ok {
				view.Projections = append(view.Projections, name)
				native = &ds
			}
		}
	}
	for name, ds := range s.currentAuth().Docsets {
		if ds.Projection == nil || ds.Projection.Store != state.Store {
			continue
		}
		if _, ok := s.effectiveGrantNames(id, name); ok {
			view.Projections = append(view.Projections, name)
			projections = append(projections, ds)
		}
	}
	sort.Strings(view.Projections)
	if state.Owner == id.IdentityName && !strings.HasPrefix(state.Store, "openlore/") {
		return view, nil, true
	}
	if native == nil && len(projections) == 0 {
		return view, nil, false
	}
	selected := func(name string) bool {
		if native != nil {
			for _, mapping := range native.Paths {
				root := displayPath(mapping)
				target := "/" + name
				if len(native.Paths) == 1 {
					target = path.Join(root, name)
				}
				if pathWithinRoot(root, target) && scope.within(target) {
					return true
				}
			}
		}
		for _, ds := range projections {
			p := ds.Projection
			rel := name
			if p.Subpath != "" {
				if !strings.HasPrefix(name, p.Subpath+"/") {
					continue
				}
				rel = strings.TrimPrefix(name, p.Subpath+"/")
			}
			if config.SourcePathSelected(p.Include, p.Exclude, rel) {
				if scope.within(path.Join(primaryDisplayPath(ds), rel)) {
					return true
				}
			}
		}
		return false
	}
	view.Latest = filterSourceSnapshot(view.Latest, selected)
	return view, selected, true
}
func filterSourceSnapshot(snap SourceSnapshot, selected func(string) bool) SourceSnapshot {
	files := map[string]SourceFile{}
	for name, f := range snap.Files {
		if selected(name) {
			files[name] = f
		}
	}
	snap.Files = files
	snap.Record = CommitRecord{}
	snap.Deleted = 0
	return snap
}
func (s *Server) sourceRows(id Identity) ([]cmds.SourceInfo, error) {
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	states, err := s.docstore.states()
	if err != nil {
		return nil, err
	}
	rows := []cmds.SourceInfo{}
	for _, state := range states {
		view, _, visible := s.visibleSource(id, state)
		if !visible {
			continue
		}
		rows = append(rows, cmds.SourceInfo{Store: view.Store, Docset: strings.Join(view.Projections, ","), Snapshot: view.Latest.Snapshot})
	}
	return rows, nil
}
