package openlore

import (
	"context"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// readChainFS runs the read (before-read) middleware chain in front of every
// Stat / ReadDir / ReadFile, then delegates to the wrapped read view. The chain
// is a gate, not a transform: a middleware can run work (e.g. a debounced git
// pull) and abort the read by returning an error, but it never rewrites the
// bytes returned.
//
// It wraps the read view near the substrate (inside the write wrappers), so the
// gate fires for every read that actually reaches storage — including the
// internal reads other layers perform (e.g. a CAS base read). Writes never pass
// through it: they go to the log via middlewareFS, and the read chain is a
// read-only concern.
type readChainFS struct {
	vfs.FileSystem // read delegation

	attribution Attribution
	gate        ReadHandler // composed read chain; nil-safe via newReadChainFS guard
}

// newReadChainFS wraps base so each read first runs gate. Callers only install
// it when at least one read middleware is registered.
func newReadChainFS(base vfs.FileSystem, attribution Attribution, gate ReadHandler) *readChainFS {
	return &readChainFS{FileSystem: base, attribution: attribution, gate: gate}
}

func (r *readChainFS) Stat(p string) (*vfs.FileInfo, error) {
	if err := r.gate(context.Background(), ReadOp{Path: p, Kind: ReadKindStat, Attribution: r.attribution}); err != nil {
		return nil, err
	}
	return r.FileSystem.Stat(p)
}

func (r *readChainFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	if err := r.gate(context.Background(), ReadOp{Path: p, Kind: ReadKindDir, Attribution: r.attribution}); err != nil {
		return nil, err
	}
	return r.FileSystem.ReadDir(p)
}

func (r *readChainFS) ReadFile(p string) ([]byte, error) {
	if err := r.gate(context.Background(), ReadOp{Path: p, Kind: ReadKindFile, Attribution: r.attribution}); err != nil {
		return nil, err
	}
	return r.FileSystem.ReadFile(p)
}

func (r *readChainFS) GetXattr(p, name string) ([]byte, error) {
	x, ok := r.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.GetXattr(p, name)
}

func (r *readChainFS) ListXattrs(p string) ([]string, error) {
	x, ok := r.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.ListXattrs(p)
}

func (r *readChainFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	x, ok := r.FileSystem.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.SetXattr(p, name, value, flags)
}

func (r *readChainFS) RemoveXattr(p, name string) error {
	x, ok := r.FileSystem.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.RemoveXattr(p, name)
}

func (r *readChainFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	x, ok := r.FileSystem.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.PreserveAndRecreateXattrs(p, attrs)
}

func (r *readChainFS) MigrateXattrs(p string, migration vfs.XattrMigration) error {
	x, ok := r.FileSystem.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.MigrateXattrs(p, migration)
}

var _ vfs.FileSystem = (*readChainFS)(nil)
var _ vfs.XattrReader = (*readChainFS)(nil)
var _ vfs.XattrWriter = (*readChainFS)(nil)
var _ vfs.XattrMaintenance = (*readChainFS)(nil)
