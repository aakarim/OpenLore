package openlore

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sort"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// pathAlias maps an alternate display root to its canonical docset root.
type pathAlias struct {
	Alias  string
	Target string
}

// aliasFS exposes alternate paths while delegating every operation through the
// canonical path. It is installed outside scope and middleware wrappers so
// authorization, hooks, changesets, and storage observe one path identity.
type aliasFS struct {
	vfs.FileSystem
	aliases []pathAlias
}

func newAliasFS(base vfs.FileSystem, aliases []pathAlias) vfs.FileSystem {
	clean := make([]pathAlias, 0, len(aliases))
	for _, a := range aliases {
		clean = append(clean, pathAlias{Alias: vfs.CleanPath(a.Alias), Target: vfs.CleanPath(a.Target)})
	}
	sort.Slice(clean, func(i, j int) bool { return len(clean[i].Alias) > len(clean[j].Alias) })
	a := &aliasFS{FileSystem: base, aliases: clean}
	if writable, ok := base.(vfs.WritableFS); ok {
		return &writableAliasFS{aliasFS: a, inner: writable}
	}
	return a
}

// canonical maps p through the most-specific alias root.
func (a *aliasFS) canonical(p string) (string, *pathAlias) {
	clean := vfs.CleanPath(p)
	for i := range a.aliases {
		alias := &a.aliases[i]
		if pathWithinRoot(alias.Alias, clean) {
			return replacePathRoot(clean, alias.Alias, alias.Target), alias
		}
	}
	return clean, nil
}

func (a *aliasFS) CanonicalPath(p string) string {
	canonical, _ := a.canonical(p)
	return canonical
}

func (a *aliasFS) UpdateRemoteSkill(ctx context.Context, skillDir string) (string, string, error) {
	updater, ok := a.FileSystem.(interface {
		UpdateRemoteSkill(context.Context, string) (string, string, error)
	})
	if !ok {
		return "", "", syscall.ENOTSUP
	}
	canonical, _ := a.canonical(skillDir)
	return updater.UpdateRemoteSkill(ctx, canonical)
}

func replacePathRoot(p, from, to string) string {
	if p == from {
		return to
	}
	return vfs.CleanPath(to + p[len(from):])
}

func (a *aliasFS) Stat(p string) (*vfs.FileInfo, error) {
	requested := vfs.CleanPath(p)
	canonical, alias := a.canonical(p)
	if alias != nil {
		info, err := a.FileSystem.Stat(canonical)
		if err != nil {
			return nil, err
		}
		copy := *info
		copy.FileName = path.Base(requested)
		copy.FilePath = requested
		return &copy, nil
	}

	info, err := a.FileSystem.Stat(canonical)
	if err == nil {
		return info, nil
	}
	if errors.Is(err, fs.ErrNotExist) && a.hasAliasDescendant(canonical) {
		return &vfs.FileInfo{FileName: path.Base(canonical), FilePath: canonical, Dir: true}, nil
	}
	return nil, err
}

func (a *aliasFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	clean := vfs.CleanPath(p)
	canonical, alias := a.canonical(clean)
	if alias != nil {
		entries, err := a.FileSystem.ReadDir(canonical)
		if err != nil {
			return nil, err
		}
		out := make([]vfs.FileInfo, 0, len(entries))
		for i := range entries {
			copy := entries[i]
			copy.FilePath = path.Join(clean, copy.FileName)
			out = append(out, copy)
		}
		return out, nil
	}

	entries, err := a.FileSystem.ReadDir(clean)
	children := a.aliasChildren(clean)
	if err != nil && len(children) == 0 {
		return nil, err
	}
	if err != nil && (!errors.Is(err, fs.ErrNotExist) || !a.hasAliasDescendant(clean)) {
		return nil, err
	}

	seen := make(map[string]bool, len(entries)+len(children))
	for _, entry := range entries {
		seen[entry.FileName] = true
	}
	for _, child := range children {
		if seen[child] {
			continue
		}
		childPath := path.Join(clean, child)
		entry := vfs.FileInfo{FileName: child, FilePath: childPath, Dir: true}
		if target, ok := a.exactAliasTarget(childPath); ok {
			info, statErr := a.FileSystem.Stat(target)
			if statErr != nil {
				if errors.Is(statErr, fs.ErrNotExist) {
					continue
				}
				return nil, statErr
			}
			entry = *info
			entry.FileName = child
			entry.FilePath = childPath
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (a *aliasFS) ReadFile(p string) ([]byte, error) {
	canonical, _ := a.canonical(p)
	return a.FileSystem.ReadFile(canonical)
}

func (a *aliasFS) GetXattr(p, name string) ([]byte, error) {
	x, ok := a.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	canonical, _ := a.canonical(p)
	return x.GetXattr(canonical, name)
}
func (a *aliasFS) ListXattrs(p string) ([]string, error) {
	x, ok := a.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	canonical, _ := a.canonical(p)
	return x.ListXattrs(canonical)
}

func (a *aliasFS) hasAliasDescendant(parent string) bool {
	parent = vfs.CleanPath(parent)
	for _, alias := range a.aliases {
		if parent == "/" || pathWithinRoot(parent, alias.Alias) && parent != alias.Alias {
			return true
		}
	}
	return false
}

func (a *aliasFS) aliasChildren(parent string) []string {
	parent = vfs.CleanPath(parent)
	children := map[string]bool{}
	for _, alias := range a.aliases {
		if parent != "/" && !pathWithinRoot(parent, alias.Alias) {
			continue
		}
		rel := alias.Alias[1:]
		if parent != "/" {
			rel = alias.Alias[len(parent)+1:]
		}
		if rel == "" {
			continue
		}
		children[path.Base("/"+firstPathSegment(rel))] = true
	}
	out := make([]string, 0, len(children))
	for child := range children {
		out = append(out, child)
	}
	sort.Strings(out)
	return out
}

func (a *aliasFS) exactAliasTarget(p string) (string, bool) {
	clean := vfs.CleanPath(p)
	for _, alias := range a.aliases {
		if alias.Alias == clean {
			return alias.Target, true
		}
	}
	return "", false
}

func firstPathSegment(p string) string {
	for i, r := range p {
		if r == '/' {
			return p[:i]
		}
	}
	return p
}

// writableAliasFS adds canonicalized mutations when the wrapped session view
// is writable. Read-only alias views do not advertise vfs.WritableFS.
type writableAliasFS struct {
	*aliasFS
	inner vfs.WritableFS
}

func (a *writableAliasFS) WriteFileAtomic(p string, data []byte, opts vfs.WriteOpts) (string, error) {
	canonical, _ := a.canonical(p)
	return a.inner.WriteFileAtomic(canonical, data, opts)
}

func (a *writableAliasFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	admitter, ok := a.inner.(vfs.ChangeSetAdmitter)
	if !ok {
		return syscall.ENOTSUP
	}
	for i := range cs.Changes {
		cs.Changes[i].Target, _ = a.canonical(cs.Changes[i].Target)
	}
	cs.Target, _ = a.canonical(cs.Target)
	return admitter.AdmitChangeSet(cs)
}

func (a *writableAliasFS) Mkdir(p string) error {
	canonical, _ := a.canonical(p)
	return a.inner.Mkdir(canonical)
}

func (a *writableAliasFS) MkdirAll(p string) error {
	canonical, _ := a.canonical(p)
	return a.inner.MkdirAll(canonical)
}

func (a *writableAliasFS) Remove(p string) error {
	canonical, _ := a.canonical(p)
	return a.inner.Remove(canonical)
}

func (a *writableAliasFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	canonical, _ := a.canonical(p)
	opts = a.canonicalRemoveOpts(opts)
	return a.inner.RemoveAll(canonical, opts)
}
func (a *writableAliasFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	x, ok := a.inner.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	canonical, _ := a.canonical(p)
	return x.SetXattr(canonical, name, value, flags)
}
func (a *writableAliasFS) RemoveXattr(p, name string) error {
	x, ok := a.inner.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	canonical, _ := a.canonical(p)
	return x.RemoveXattr(canonical, name)
}
func (a *writableAliasFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	x, ok := a.inner.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	c, _ := a.canonical(p)
	return x.PreserveAndRecreateXattrs(c, attrs)
}
func (a *writableAliasFS) MigrateXattrs(p string, m vfs.XattrMigration) error {
	x, ok := a.inner.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	c, _ := a.canonical(p)
	return x.MigrateXattrs(c, m)
}

func (a *aliasFS) canonicalRemoveOpts(opts vfs.RemoveOpts) vfs.RemoveOpts {
	if opts.Expected == nil {
		return opts
	}
	opts.Expected = copyCanonicalSnapshot(opts.Expected, a.CanonicalPath)
	return opts
}

func copyCanonicalSnapshot(source *vfs.TreeSnapshot, canonical func(string) string) *vfs.TreeSnapshot {
	copy := *source
	copy.Root = canonical(copy.Root)
	copy.Ops = append([]vfs.TreeOp(nil), source.Ops...)
	return &copy
}

func (a *writableAliasFS) SetWriteable() error { return a.inner.SetWriteable() }
func (a *writableAliasFS) SetReadonly() error  { return a.inner.SetReadonly() }

func (a *writableAliasFS) CanWrite(p string) bool {
	canonical, _ := a.canonical(p)
	if scoped, ok := a.inner.(vfs.WriteScopeFS); ok {
		return scoped.CanWrite(canonical)
	}
	return true
}

func (a *writableAliasFS) LastReadHash(p string) (string, bool) {
	canonical, _ := a.canonical(p)
	if tracker, ok := a.inner.(vfs.ReadTracker); ok {
		return tracker.LastReadHash(canonical)
	}
	return "", false
}

var (
	_ vfs.FileSystem        = (*aliasFS)(nil)
	_ vfs.PathCanonicalizer = (*aliasFS)(nil)
	_ vfs.WritableFS        = (*writableAliasFS)(nil)
	_ vfs.WriteScopeFS      = (*writableAliasFS)(nil)
	_ vfs.ReadTracker       = (*writableAliasFS)(nil)
	_ vfs.ChangeSetAdmitter = (*writableAliasFS)(nil)
)
