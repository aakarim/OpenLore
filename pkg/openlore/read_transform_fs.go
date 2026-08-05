package openlore

import (
	"context"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type readTransformFS struct {
	vfs.FileSystem
	transforms        []ContentTransform
	updateRemoteSkill func(context.Context, string) (string, string, error)
}

type writableReadTransformFS struct {
	vfs.WritableFS
	transforms        []ContentTransform
	updateRemoteSkill func(context.Context, string) (string, string, error)
}

func (f *readTransformFS) UpdateRemoteSkill(ctx context.Context, skillDir string) (string, string, error) {
	if f.updateRemoteSkill == nil {
		return "", "", syscall.ENOTSUP
	}
	return f.updateRemoteSkill(ctx, skillDir)
}

func (f *writableReadTransformFS) UpdateRemoteSkill(ctx context.Context, skillDir string) (string, string, error) {
	if f.updateRemoteSkill == nil {
		return "", "", syscall.ENOTSUP
	}
	return f.updateRemoteSkill(ctx, skillDir)
}

func (f *readTransformFS) ReadFile(p string) ([]byte, error) {
	b, err := f.FileSystem.ReadFile(p)
	if err != nil {
		return nil, err
	}
	for _, transform := range f.transforms {
		b = transform(p, b)
	}
	return b, nil
}

var _ vfs.FileSystem = (*readTransformFS)(nil)

func (f *readTransformFS) LastReadHash(p string) (string, bool) {
	r, ok := f.FileSystem.(vfs.ReadTracker)
	if !ok {
		return "", false
	}
	return r.LastReadHash(p)
}

func (f *readTransformFS) CanWrite(p string) bool {
	s, ok := f.FileSystem.(vfs.WriteScopeFS)
	return !ok || s.CanWrite(p)
}

func (f *readTransformFS) CanonicalPath(p string) string {
	c, ok := f.FileSystem.(vfs.PathCanonicalizer)
	if !ok {
		return vfs.CleanPath(p)
	}
	return c.CanonicalPath(p)
}

func (f *readTransformFS) GetXattr(p, name string) ([]byte, error) {
	x, ok := f.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.GetXattr(p, name)
}

func (f *readTransformFS) ListXattrs(p string) ([]string, error) {
	x, ok := f.FileSystem.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.ListXattrs(p)
}

func (f *readTransformFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	x, ok := f.FileSystem.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.SetXattr(p, name, value, flags)
}

func (f *readTransformFS) RemoveXattr(p, name string) error {
	x, ok := f.FileSystem.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.RemoveXattr(p, name)
}

func (f *readTransformFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	x, ok := f.FileSystem.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.PreserveAndRecreateXattrs(p, attrs)
}

func (f *readTransformFS) MigrateXattrs(p string, migration vfs.XattrMigration) error {
	x, ok := f.FileSystem.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.MigrateXattrs(p, migration)
}

func (f *writableReadTransformFS) ReadFile(p string) ([]byte, error) {
	b, err := f.WritableFS.ReadFile(p)
	if err != nil {
		return nil, err
	}
	for _, transform := range f.transforms {
		b = transform(p, b)
	}
	return b, nil
}

func (f *writableReadTransformFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	a, ok := f.WritableFS.(vfs.ChangeSetAdmitter)
	if !ok {
		return syscall.ENOTSUP
	}
	return a.AdmitChangeSet(cs)
}

var _ vfs.WritableFS = (*writableReadTransformFS)(nil)

func (f *writableReadTransformFS) LastReadHash(p string) (string, bool) {
	return (&readTransformFS{FileSystem: f.WritableFS}).LastReadHash(p)
}
func (f *writableReadTransformFS) CanWrite(p string) bool {
	return (&readTransformFS{FileSystem: f.WritableFS}).CanWrite(p)
}
func (f *writableReadTransformFS) CanonicalPath(p string) string {
	return (&readTransformFS{FileSystem: f.WritableFS}).CanonicalPath(p)
}
func (f *writableReadTransformFS) GetXattr(p, name string) ([]byte, error) {
	return (&readTransformFS{FileSystem: f.WritableFS}).GetXattr(p, name)
}
func (f *writableReadTransformFS) ListXattrs(p string) ([]string, error) {
	return (&readTransformFS{FileSystem: f.WritableFS}).ListXattrs(p)
}
func (f *writableReadTransformFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	return (&readTransformFS{FileSystem: f.WritableFS}).SetXattr(p, name, value, flags)
}
func (f *writableReadTransformFS) RemoveXattr(p, name string) error {
	return (&readTransformFS{FileSystem: f.WritableFS}).RemoveXattr(p, name)
}
func (f *writableReadTransformFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	return (&readTransformFS{FileSystem: f.WritableFS}).PreserveAndRecreateXattrs(p, attrs)
}
func (f *writableReadTransformFS) MigrateXattrs(p string, migration vfs.XattrMigration) error {
	return (&readTransformFS{FileSystem: f.WritableFS}).MigrateXattrs(p, migration)
}

var (
	_ vfs.ReadTracker       = (*readTransformFS)(nil)
	_ vfs.XattrReader       = (*readTransformFS)(nil)
	_ vfs.XattrWriter       = (*readTransformFS)(nil)
	_ vfs.XattrMaintenance  = (*readTransformFS)(nil)
	_ vfs.WriteScopeFS      = (*readTransformFS)(nil)
	_ vfs.PathCanonicalizer = (*readTransformFS)(nil)
	_ vfs.ReadTracker       = (*writableReadTransformFS)(nil)
	_ vfs.XattrReader       = (*writableReadTransformFS)(nil)
	_ vfs.XattrWriter       = (*writableReadTransformFS)(nil)
	_ vfs.XattrMaintenance  = (*writableReadTransformFS)(nil)
	_ vfs.WriteScopeFS      = (*writableReadTransformFS)(nil)
	_ vfs.PathCanonicalizer = (*writableReadTransformFS)(nil)
	_ vfs.ChangeSetAdmitter = (*writableReadTransformFS)(nil)
)
