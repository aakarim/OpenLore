package openlore

import (
	"context"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// middlewareFS is the per-session write sink: every mutation it receives is
// turned into a vfs.ChangeSet, run through the admission chain, and (if
// admitted) submitted to the single global write log — never written to a
// substrate directly. Reads delegate straight through to the session's read
// view. It is the seam that funnels all writes into the one serialized applier,
// so writes, directory creation, and removals are globally ordered.
//
// Layering: it is the innermost writable wrapper. Fixed scope layers
// (scopedWriteFS, and any admission middleware) sit outside it and either deny
// or defer a mutation before it becomes a log entry; read-hash tracking
// (readTrackingFS) sits outside too and observes the committed hash the log
// returns.
type middlewareFS struct {
	vfs.FileSystem // read delegation (Stat / ReadDir / ReadFile)

	attribution Attribution
	identity    *Identity
	admit       WriteHandler // composed admission chain; terminal handler submits to the log
}

func newIdentityMiddlewareFS(readView vfs.FileSystem, identity Identity, admit WriteHandler) *middlewareFS {
	return &middlewareFS{FileSystem: readView, attribution: identity.attribution(), identity: &identity, admit: admit}
}

// newMiddlewareFS wraps a session read view so its mutations flow through admit.
// admit is the admission chain composed around a terminal handler that submits
// to the global log (see Server.writeHandler).
func newMiddlewareFS(readView vfs.FileSystem, attribution Attribution, admit WriteHandler) *middlewareFS {
	return &middlewareFS{FileSystem: readView, attribution: attribution, admit: admit}
}

// run drives a ChangeSet through the admission chain and returns the committed
// hash (empty for non-write actions) or the chain's error. A deferred write
// surfaces as *vfs.PendingChangeError; a rejected one as the middleware's error.
func (m *middlewareFS) run(cs vfs.ChangeSet) (string, error) {
	op := NewWriteOp(m.attribution, cs)
	if m.identity != nil {
		op = newIdentityWriteOp(*m.identity, cs)
	}
	res, err := m.admit(context.Background(), op)
	return res.Hash, err
}

func (m *middlewareFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	_, err := m.run(cs)
	return err
}

func (m *middlewareFS) WriteFileAtomic(p string, data []byte, opts vfs.WriteOpts) (string, error) {
	return m.run(vfs.ChangeSet{
		Target: p,
		Action: vfs.ChangeActionWrite,
		Write:  &vfs.WriteChange{Bytes: data, Opts: opts},
	})
}

func (m *middlewareFS) Mkdir(p string) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionMkdir})
	return err
}

func (m *middlewareFS) MkdirAll(p string) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionMkdirAll})
	return err
}

func (m *middlewareFS) Remove(p string) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionRemove})
	return err
}

func (m *middlewareFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	_, err := m.run(vfs.ChangeSet{
		Target:    p,
		Action:    vfs.ChangeActionRemoveAll,
		RemoveAll: &vfs.RemoveAllChange{Opts: opts},
	})
	return err
}

func (m *middlewareFS) GetXattr(p, name string) ([]byte, error) {
	x, ok := m.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.GetXattr(p, name)
}
func (m *middlewareFS) ListXattrs(p string) ([]string, error) {
	x, ok := m.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.ListXattrs(p)
}
func (m *middlewareFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionSetXattr, Xattr: &vfs.XattrChange{Name: name, Value: append([]byte(nil), value...), Flags: flags}})
	return err
}
func (m *middlewareFS) RemoveXattr(p, name string) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionRemoveXattr, Xattr: &vfs.XattrChange{Name: name}})
	return err
}
func (m *middlewareFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionPreserveAndRecreateXattrs, XattrRepair: &vfs.XattrRepairChange{Attributes: attrs}})
	return err
}
func (m *middlewareFS) MigrateXattrs(p string, migration vfs.XattrMigration) error {
	_, err := m.run(vfs.ChangeSet{Target: p, Action: vfs.ChangeActionMigrateXattrs, XattrMigration: &migration})
	return err
}

// SetWriteable / SetReadonly are no-ops: the substrate-wide write lock is owned
// centrally by the server (MergeFS.SetWriteable at startup) and the applier is
// the sole writer. A session may only narrow access, never toggle the lock.
func (m *middlewareFS) SetWriteable() error { return nil }
func (m *middlewareFS) SetReadonly() error  { return nil }

var _ vfs.WritableFS = (*middlewareFS)(nil)
var _ vfs.ChangeSetAdmitter = (*middlewareFS)(nil)
