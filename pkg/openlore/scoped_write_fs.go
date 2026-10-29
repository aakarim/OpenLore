package openlore

import (
	"fmt"
	"os"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// writeAuthorizer decides whether a session may perform a mutation action on a
// display path. It is the per-operation authority: the server binds it to the
// principal's current RBAC policy (Server.identityWriteError), so two agents that
// can see the same docsets are still authorized independently per write, and a
// grant like `publish` can permit create/edit only within an inbox while denying
// deletes. A nil error allows the operation; otherwise the error names the gate.
type writeAuthorizer func(action vfs.ChangeAction, p string) error

// scopedWriteFS gates a session's writes through a writeAuthorizer. Reads pass
// straight through to the wrapped filesystem; a mutation only reaches the
// backing substrate when the authorizer allows the specific (action, path),
// otherwise it fails closed. Atomic batches distinguish scope denials from
// read-only backends and unsupported batch admission.
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
		authz = func(action vfs.ChangeAction, p string) error { return mutationDeniedError(base, action, p) }
	}
	return &scopedWriteFS{FileSystem: base, inner: w, authorize: authz}
}

func (s *scopedWriteFS) ReadFileBounded(p string, maxBytes int64) ([]byte, error) {
	return readFileBounded(s.FileSystem, p, maxBytes)
}

func (s *scopedWriteFS) WriteFileAtomic(p string, data []byte, opts vfs.WriteOpts) (string, error) {
	if s.inner == nil {
		return "", fmt.Errorf("%w: no writable backend for %s", vfs.ErrReadOnly, p)
	}
	if err := s.authorize(vfs.ChangeActionWrite, p); err != nil {
		return "", err
	}
	return s.inner.WriteFileAtomic(p, data, opts)
}

func (s *scopedWriteFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	if err := vfs.ValidateChangeSet(cs); err != nil {
		return err
	}
	if s.inner == nil {
		return vfs.ErrReadOnly
	}
	for _, change := range cs.Leaves() {
		if err := s.authorize(change.Action, change.Target); err != nil {
			return fmt.Errorf("write scope denies %s on %s: %w: %w", change.Action, change.Target, os.ErrPermission, err)
		}
	}
	a, ok := s.inner.(vfs.ChangeSetAdmitter)
	if !ok {
		return syscall.ENOTSUP
	}
	return a.AdmitChangeSet(cs)
}

// CanWrite reports whether a whole-file write to p is authorized in this session
// (vfs.WriteScopeFS). It enables fail-fast checks (e.g. `spawn`) without writing.
func (s *scopedWriteFS) CanWrite(p string) bool {
	return s.inner != nil && s.authorize(vfs.ChangeActionWrite, p) == nil
}

func (s *scopedWriteFS) Mkdir(p string) error {
	if s.inner == nil {
		return fmt.Errorf("%w: no writable backend for %s", vfs.ErrReadOnly, p)
	}
	if err := s.authorize(vfs.ChangeActionMkdir, p); err != nil {
		return err
	}
	return s.inner.Mkdir(p)
}

func (s *scopedWriteFS) MkdirAll(p string) error {
	if s.inner == nil {
		return fmt.Errorf("%w: no writable backend for %s", vfs.ErrReadOnly, p)
	}
	if err := s.authorize(vfs.ChangeActionMkdirAll, p); err != nil {
		return err
	}
	return s.inner.MkdirAll(p)
}

func (s *scopedWriteFS) Remove(p string) error {
	if s.inner == nil {
		return fmt.Errorf("%w: no writable backend for %s", vfs.ErrReadOnly, p)
	}
	if err := s.authorize(vfs.ChangeActionRemove, p); err != nil {
		return err
	}
	return s.inner.Remove(p)
}

func (s *scopedWriteFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	if s.inner == nil {
		return fmt.Errorf("%w: no writable backend for %s", vfs.ErrReadOnly, p)
	}
	if err := s.authorize(vfs.ChangeActionRemoveAll, p); err != nil {
		return err
	}
	return s.inner.RemoveAll(p, opts)
}

func mutationDeniedError(fsys vfs.FileSystem, action vfs.ChangeAction, target string) error {
	if isDirConfigPath(target) {
		return fmt.Errorf("%w: %s requires config.edit authority", os.ErrPermission, target)
	}
	if action == vfs.ChangeActionRemoveAll && treeContainsDirConfig(fsys, target) {
		return fmt.Errorf("%w: cannot delete %s without config.edit authority for its folder configs", os.ErrPermission, target)
	}
	return fmt.Errorf("%w: write scope denies %s on %s; no grant permits this operation (run 'lore docsets')", vfs.ErrReadOnly, action, target)
}

func treeContainsDirConfig(fsys vfs.FileSystem, target string) bool {
	containsConfig := false
	_ = vfs.WalkDir(fsys, target, func(candidate string, _ *vfs.FileInfo, err error) error {
		if err == nil && isDirConfigPath(candidate) {
			containsConfig = true
		}
		return nil
	})
	return containsConfig
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
	if s.inner == nil || s.authorize(vfs.ChangeActionSetXattr, p) != nil {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.SetXattr(p, name, value, flags)
}
func (s *scopedWriteFS) RemoveXattr(p, name string) error {
	if s.inner == nil || s.authorize(vfs.ChangeActionRemoveXattr, p) != nil {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.RemoveXattr(p, name)
}
func (s *scopedWriteFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	if s.inner == nil || s.authorize(vfs.ChangeActionPreserveAndRecreateXattrs, p) != nil {
		return syscall.EPERM
	}
	x, ok := s.inner.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.PreserveAndRecreateXattrs(p, attrs)
}
func (s *scopedWriteFS) MigrateXattrs(p string, m vfs.XattrMigration) error {
	if s.inner == nil || s.authorize(vfs.ChangeActionMigrateXattrs, p) != nil {
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
