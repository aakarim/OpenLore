package openlore

import (
	"github.com/aakarim/go-openlore/pkg/vfs"
	"syscall"
)

// writeAuthorizer decides whether a session may perform a mutation action on a
// display path. It is the per-operation authority: the server binds it to the
// principal's current RBAC policy (Server.identityCanWrite), so two agents that
// can see the same docsets are still authorized independently per write, and a
// grant like `publish` can permit create/edit only within an inbox while denying
// deletes.
type writeAuthorizer func(action vfs.ChangeAction, p string) bool

// scopedWriteFS gates a session's writes through a writeAuthorizer. Reads pass
// straight through to the wrapped filesystem; a mutation only reaches the
// backing substrate when the authorizer allows the specific (action, path),
// otherwise it fails closed with vfs.ErrReadOnly.
type scopedWriteFS struct {
	vfs.FileSystem                // read delegation (Stat / ReadDir / ReadFile)
	inner          vfs.WritableFS // the writable substrate, nil if base is read-only
	authorize      writeAuthorizer
}

// newScopedWriteFS wraps base so every mutation is authorized by authz. If base
// is not itself writable, every write fails closed with vfs.ErrReadOnly. A nil
// authz denies all writes.
func newScopedWriteFS(base vfs.FileSystem, authz writeAuthorizer) *scopedWriteFS {
	w, _ := base.(vfs.WritableFS)
	if authz == nil {
		authz = func(vfs.ChangeAction, string) bool { return false }
	}
	return &scopedWriteFS{FileSystem: base, inner: w, authorize: authz}
}

func (s *scopedWriteFS) WriteFileAtomic(p string, data []byte, opts vfs.WriteOpts) (string, error) {
	if s.inner == nil || !s.authorize(vfs.ChangeActionWrite, p) {
		return "", vfs.ErrReadOnly
	}
	return s.inner.WriteFileAtomic(p, data, opts)
}

func (s *scopedWriteFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	a, ok := s.inner.(vfs.ChangeSetAdmitter)
	if !ok || vfs.ValidateChangeSet(cs) != nil {
		return vfs.ErrReadOnly
	}
	for _, change := range cs.Leaves() {
		if !s.authorize(change.Action, change.Target) {
			return vfs.ErrReadOnly
		}
	}
	return a.AdmitChangeSet(cs)
}

// CanWrite reports whether a whole-file write to p is authorized in this session
// (vfs.WriteScopeFS). It enables fail-fast checks (e.g. `spawn`) without writing.
func (s *scopedWriteFS) CanWrite(p string) bool {
	return s.inner != nil && s.authorize(vfs.ChangeActionWrite, p)
}

func (s *scopedWriteFS) Mkdir(p string) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionMkdir, p) {
		return vfs.ErrReadOnly
	}
	return s.inner.Mkdir(p)
}

func (s *scopedWriteFS) MkdirAll(p string) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionMkdirAll, p) {
		return vfs.ErrReadOnly
	}
	return s.inner.MkdirAll(p)
}

func (s *scopedWriteFS) Remove(p string) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionRemove, p) {
		return vfs.ErrReadOnly
	}
	return s.inner.Remove(p)
}

func (s *scopedWriteFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionRemoveAll, p) {
		return vfs.ErrReadOnly
	}
	return s.inner.RemoveAll(p, opts)
}
func (s *scopedWriteFS) GetXattr(p, name string) ([]byte, error) {
	x, ok := s.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.GetXattr(p, name)
}
func (s *scopedWriteFS) ListXattrs(p string) ([]string, error) {
	x, ok := s.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.ListXattrs(p)
}
func (s *scopedWriteFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionSetXattr, p) {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.SetXattr(p, name, value, flags)
}
func (s *scopedWriteFS) RemoveXattr(p, name string) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionRemoveXattr, p) {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.RemoveXattr(p, name)
}
func (s *scopedWriteFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionPreserveAndRecreateXattrs, p) {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.PreserveAndRecreateXattrs(p, attrs)
}
func (s *scopedWriteFS) MigrateXattrs(p string, m vfs.XattrMigration) error {
	if s.inner == nil || !s.authorize(vfs.ChangeActionMigrateXattrs, p) {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.MigrateXattrs(p, m)
}

// SetWriteable / SetReadonly are no-ops: a session must not be able to toggle
// the substrate-wide write lock. The lock is owned centrally by the server
// (MergeFS.SetWriteable at startup); the session only ever narrows it.
func (s *scopedWriteFS) SetWriteable() error { return nil }
func (s *scopedWriteFS) SetReadonly() error  { return nil }

var _ vfs.WritableFS = (*scopedWriteFS)(nil)
var _ vfs.ChangeSetAdmitter = (*scopedWriteFS)(nil)
