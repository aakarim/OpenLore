package openlore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"sync"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// OverlayFS exposes a writable directory over a read-only filesystem at one
// virtual root. Upper entries shadow lower entries; directories are merged.
// Deletes of lower-backed paths are rejected because the overlay deliberately
// has no persistent whiteout format.
type OverlayFS struct {
	upper *DirFS
	lower vfs.FileSystem
	mu    sync.Mutex
}

// NewOverlayFS creates a filesystem with upper as its writable layer and lower
// as its read-only fallback.
func NewOverlayFS(upper *DirFS, lower vfs.FileSystem) *OverlayFS {
	return &OverlayFS{upper: upper, lower: lower}
}

func (o *OverlayFS) SetWriteable() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.upper.SetWriteable()
}

func (o *OverlayFS) SetReadonly() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.upper.SetReadonly()
}

func (o *OverlayFS) PreflightChange(change vfs.Change) error {
	return o.upper.PreflightChange(change)
}

func (o *OverlayFS) Stat(p string) (*vfs.FileInfo, error) {
	info, err := o.upper.Stat(p)
	if err == nil || !errors.Is(err, fs.ErrNotExist) || o.lower == nil {
		return info, err
	}
	return o.lower.Stat(p)
}

func (o *OverlayFS) ReadFile(p string) ([]byte, error) {
	b, err := o.upper.ReadFile(p)
	if err == nil || !errors.Is(err, fs.ErrNotExist) || o.lower == nil {
		return b, err
	}
	return o.lower.ReadFile(p)
}

func (o *OverlayFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	upperInfo, upperErr := o.upper.Stat(p)
	if upperErr != nil {
		if !errors.Is(upperErr, fs.ErrNotExist) || o.lower == nil {
			return nil, upperErr
		}
		return o.lower.ReadDir(p)
	}
	upper, err := o.upper.ReadDir(p)
	if err != nil || o.lower == nil || !upperInfo.Dir {
		return upper, err
	}
	lowerInfo, lowerErr := o.lower.Stat(p)
	if lowerErr != nil {
		if errors.Is(lowerErr, fs.ErrNotExist) {
			return upper, nil
		}
		return nil, lowerErr
	}
	if !lowerInfo.Dir {
		return upper, nil
	}
	lower, err := o.lower.ReadDir(p)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(upper))
	entries := append([]vfs.FileInfo(nil), upper...)
	for _, entry := range upper {
		seen[entry.FileName] = struct{}{}
	}
	for _, entry := range lower {
		if _, ok := seen[entry.FileName]; !ok {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].FileName < entries[j].FileName })
	return entries, nil
}

func (o *OverlayFS) GetXattr(p, name string) ([]byte, error) {
	if _, err := o.upper.Stat(p); err == nil {
		return o.upper.GetXattr(p, name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	x, ok := o.lower.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.GetXattr(p, name)
}
func (o *OverlayFS) ListXattrs(p string) ([]string, error) {
	if _, err := o.upper.Stat(p); err == nil {
		return o.upper.ListXattrs(p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	x, ok := o.lower.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.ListXattrs(p)
}
func (o *OverlayFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.upper.Stat(p); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		info, err := o.Stat(p)
		if err != nil {
			return err
		}
		if !info.Dir {
			return syscall.ENOTSUP
		}
		if err := o.materializeDocsetRoot(p); err != nil {
			return err
		}
		if err := o.upper.materializeDir(p); err != nil {
			return err
		}
	}
	return o.upper.SetXattr(p, name, value, flags)
}
func (o *OverlayFS) RemoveXattr(p, name string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.upper.Stat(p); err != nil {
		return syscall.ENODATA
	}
	return o.upper.RemoveXattr(p, name)
}
func (o *OverlayFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.upper.PreserveAndRecreateXattrs(p, attrs)
}
func (o *OverlayFS) MigrateXattrs(p string, m vfs.XattrMigration) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.upper.MigrateXattrs(p, m)
}

func (o *OverlayFS) WriteFileAtomic(p string, content []byte, opts vfs.WriteOpts) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if _, err := o.upper.Stat(p); err == nil {
		return o.upper.WriteFileAtomic(p, content, opts)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	if o.lower == nil {
		return o.upper.WriteFileAtomic(p, content, opts)
	}
	info, err := o.lower.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return o.upper.WriteFileAtomic(p, content, opts)
		}
		return "", err
	}
	if info.Dir {
		return "", fmt.Errorf("cannot write file over directory: %s", p)
	}
	lowerContent, err := o.lower.ReadFile(p)
	if err != nil {
		return "", err
	}
	current := sha256.Sum256(lowerContent)
	currentHash := hex.EncodeToString(current[:])
	if opts.IfNoneMatch || (opts.IfMatch != nil && *opts.IfMatch != currentHash) {
		return "", &vfs.PreconditionError{Path: vfs.CleanPath(p), Current: currentHash}
	}
	return o.upper.WriteFileAtomic(p, content, vfs.WriteOpts{IfNoneMatch: true})
}

func (o *OverlayFS) Mkdir(p string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	parent := path.Dir(vfs.CleanPath(p))
	info, err := o.Stat(parent)
	if err != nil || !info.Dir {
		return fmt.Errorf("mkdir parent %s is not a directory", parent)
	}
	if err := o.materializeDocsetRoot(p); err != nil {
		return err
	}
	if err := o.upper.materializeDir(parent); err != nil {
		return err
	}
	return o.upper.Mkdir(p)
}

func (o *OverlayFS) MkdirAll(p string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.materializeDocsetRoot(p); err != nil {
		return err
	}
	return o.upper.MkdirAll(p)
}

func (o *OverlayFS) materializeDocsetRoot(p string) error {
	root, ok := o.upper.docsetRootFor(vfs.CleanPath(p))
	if !ok {
		return fmt.Errorf("cannot create folder outside a docset: %s", p)
	}
	if root == "/" {
		return nil
	}
	info, err := o.Stat(root)
	if err != nil || !info.Dir {
		return fmt.Errorf("docset root does not exist: %s", root)
	}
	return o.upper.materializeDir(root)
}

func (o *OverlayFS) Remove(p string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.rejectLowerRemoval(p); err != nil {
		return err
	}
	return o.upper.Remove(p)
}

func (o *OverlayFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.rejectLowerRemoval(p); err != nil {
		return err
	}
	return o.upper.RemoveAll(p, opts)
}

func (o *OverlayFS) rejectLowerRemoval(p string) error {
	if o.lower == nil {
		return nil
	}
	if _, err := o.lower.Stat(p); err == nil {
		return fmt.Errorf("%w: lower-layer path cannot be removed: %s", vfs.ErrReadOnly, p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

var _ vfs.WritableFS = (*OverlayFS)(nil)
