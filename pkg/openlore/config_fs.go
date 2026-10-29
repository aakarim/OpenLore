package openlore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"syscall"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// The config view mounts the server's live configuration files for sessions
// that may edit configuration. lore.json is editable through it; openlore.yml
// is read-only because the server only reads it at startup.
const (
	authConfigVFSPath   = "/opt/openlore/lore.json"
	serverConfigVFSPath = "/opt/openlore/openlore.yml"
)

type configViewFS struct {
	vfs.WritableFS
	server      *Server
	identity    Identity
	attribution Attribution
}

func (f *configViewFS) authorized() bool {
	return scopeGrantsWrite(f.identity.Scopes) && f.server.hasCurrentCapability(f.identity, "lore:config:edit")
}

// hostFile maps a synthetic config path to the host file backing it. The
// server config is only mounted when the server actually loaded one.
func (f *configViewFS) hostFile(clean string) (string, bool) {
	switch clean {
	case authConfigVFSPath:
		return f.server.config.AuthFile, true
	case serverConfigVFSPath:
		return f.server.config.ConfigFile, f.server.config.ConfigFile != ""
	}
	return "", false
}

func (f *configViewFS) Stat(name string) (*vfs.FileInfo, error) {
	clean := vfs.CleanPath(name)
	if pathWithinRoot("/opt", clean) && !f.authorized() {
		return nil, os.ErrPermission
	}
	if clean == "/opt" || clean == "/opt/openlore" {
		return &vfs.FileInfo{FileName: path.Base(clean), FilePath: clean, Dir: true}, nil
	}
	host, ok := f.hostFile(clean)
	if !ok {
		return f.WritableFS.Stat(name)
	}
	b, err := os.ReadFile(host)
	if err != nil {
		return nil, withoutHostPath(err)
	}
	info, err := os.Stat(host)
	if err != nil {
		return nil, withoutHostPath(err)
	}
	return &vfs.FileInfo{FileName: path.Base(clean), FilePath: clean, FileSize: int64(len(b)), FileModTime: info.ModTime()}, nil
}

func (f *configViewFS) ReadDir(name string) ([]vfs.FileInfo, error) {
	clean := vfs.CleanPath(name)
	if pathWithinRoot("/opt", clean) && !f.authorized() {
		return nil, os.ErrPermission
	}
	switch clean {
	case "/":
		entries, err := f.WritableFS.ReadDir(name)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.FileName == "opt" {
				return entries, nil
			}
		}
		if f.authorized() {
			return append(entries, vfs.FileInfo{FileName: "opt", FilePath: "/opt", Dir: true}), nil
		}
		return entries, nil
	case "/opt":
		return []vfs.FileInfo{{FileName: "openlore", FilePath: "/opt/openlore", Dir: true}}, nil
	case "/opt/openlore":
		entries := []vfs.FileInfo{{FileName: "lore.json", FilePath: authConfigVFSPath}}
		if f.server.config.ConfigFile != "" {
			entries = append(entries, vfs.FileInfo{FileName: "openlore.yml", FilePath: serverConfigVFSPath})
		}
		return entries, nil
	default:
		return f.WritableFS.ReadDir(name)
	}
}

func (f *configViewFS) ReadFile(name string) ([]byte, error) {
	host, ok := f.hostFile(vfs.CleanPath(name))
	if !ok {
		return f.WritableFS.ReadFile(name)
	}
	if !f.authorized() {
		return nil, os.ErrPermission
	}
	data, err := os.ReadFile(host)
	return data, withoutHostPath(err)
}

func (f *configViewFS) ReadFileBounded(name string, maxBytes int64) ([]byte, error) {
	host, ok := f.hostFile(vfs.CleanPath(name))
	if !ok {
		return readFileBounded(f.WritableFS, name, maxBytes)
	}
	if !f.authorized() {
		return nil, os.ErrPermission
	}
	file, err := os.Open(host)
	if err != nil {
		return nil, withoutHostPath(err)
	}
	defer file.Close()
	data, err := readAllBounded(file, maxBytes)
	return data, withoutHostPath(err)
}

func (f *configViewFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	clean := vfs.CleanPath(name)
	if clean != authConfigVFSPath {
		if pathWithinRoot("/opt", clean) {
			return "", vfs.ErrReadOnly
		}
		return f.WritableFS.WriteFileAtomic(name, data, opts)
	}
	if !f.authorized() {
		return "", os.ErrPermission
	}
	f.server.authMu.Lock()
	defer f.server.authMu.Unlock()
	if !f.authorized() {
		return "", os.ErrPermission
	}
	current, err := os.ReadFile(f.server.config.AuthFile)
	if err != nil {
		return "", withoutHostPath(err)
	}
	currentSum := sha256.Sum256(current)
	currentHash := hex.EncodeToString(currentSum[:])
	if opts.IfNoneMatch || (opts.IfMatch != nil && *opts.IfMatch != currentHash) {
		return "", &vfs.PreconditionError{Path: name, Current: currentHash}
	}
	var parsed config.AuthConfig
	parseErr := json.Unmarshal(data, &parsed)
	if parseErr == nil {
		parseErr = config.ValidateAuthConfig(&parsed)
	}
	if parseErr == nil {
		_, parseErr = f.server.prepareLiveConfig(&parsed)
	}
	if parseErr != nil {
		if f.server.audit != nil {
			_ = f.server.audit.Record(context.Background(), AuditEvent{Type: "config.reject", Attribution: f.attribution, Details: map[string]any{"error": parseErr.Error()}})
		}
		return "", parseErr
	}
	tmp := f.server.config.AuthFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", withoutHostPath(err)
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, f.server.config.AuthFile); err != nil {
		return "", withoutHostPath(err)
	}
	sum := sha256.Sum256(data)
	if f.server.audit != nil {
		_ = f.server.audit.Record(context.Background(), AuditEvent{Type: "config.edit", Attribution: f.attribution})
	}
	return hex.EncodeToString(sum[:]), nil
}

// CanWrite forwards the session's write scope (vfs.WriteScopeFS); the live
// config file is writable exactly when the session may edit it.
func (f *configViewFS) CanWrite(name string) bool {
	if vfs.CleanPath(name) == authConfigVFSPath {
		return f.authorized()
	}
	if pathWithinRoot("/opt", vfs.CleanPath(name)) {
		return false
	}
	scope, ok := f.WritableFS.(vfs.WriteScopeFS)
	return !ok || scope.CanWrite(name)
}

func (f *configViewFS) Mkdir(name string) error {
	if pathWithinRoot("/opt", vfs.CleanPath(name)) {
		return vfs.ErrReadOnly
	}
	return f.WritableFS.Mkdir(name)
}
func (f *configViewFS) MkdirAll(name string) error {
	if pathWithinRoot("/opt", vfs.CleanPath(name)) {
		return vfs.ErrReadOnly
	}
	return f.WritableFS.MkdirAll(name)
}
func (f *configViewFS) Remove(name string) error {
	if pathWithinRoot("/opt", vfs.CleanPath(name)) {
		return vfs.ErrReadOnly
	}
	return f.WritableFS.Remove(name)
}
func (f *configViewFS) RemoveAll(name string, opts vfs.RemoveOpts) error {
	if pathWithinRoot("/opt", vfs.CleanPath(name)) {
		return vfs.ErrReadOnly
	}
	return f.WritableFS.RemoveAll(name, opts)
}

// AdmitChangeSet forwards atomic batches (such as `mv`) to the session's
// substrate. The synthetic /opt tree is backed by the auth file, not the
// substrate, so batches touching it are rejected like other /opt mutations.
func (f *configViewFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	admitter, ok := f.WritableFS.(vfs.ChangeSetAdmitter)
	if !ok {
		return syscall.ENOTSUP
	}
	for _, change := range cs.Leaves() {
		if pathWithinRoot("/opt", vfs.CleanPath(change.Target)) {
			return vfs.ErrReadOnly
		}
	}
	return admitter.AdmitChangeSet(cs)
}

// Collection markers belong to the underlying docsets, not the synthetic
// configuration tree. Preserve xattr support for config-admin sessions.
func (f *configViewFS) GetXattr(p, name string) ([]byte, error) {
	if pathWithinRoot("/opt", vfs.CleanPath(p)) {
		return nil, syscall.ENOTSUP
	}
	x, ok := f.WritableFS.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.GetXattr(p, name)
}

func (f *configViewFS) ListXattrs(p string) ([]string, error) {
	if pathWithinRoot("/opt", vfs.CleanPath(p)) {
		return nil, syscall.ENOTSUP
	}
	x, ok := f.WritableFS.(vfs.XattrReader)
	if !ok {
		return nil, syscall.ENOTSUP
	}
	return x.ListXattrs(p)
}

func (f *configViewFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	if pathWithinRoot("/opt", vfs.CleanPath(p)) {
		return vfs.ErrReadOnly
	}
	x, ok := f.WritableFS.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.SetXattr(p, name, value, flags)
}

func (f *configViewFS) RemoveXattr(p, name string) error {
	if pathWithinRoot("/opt", vfs.CleanPath(p)) {
		return vfs.ErrReadOnly
	}
	x, ok := f.WritableFS.(vfs.XattrWriter)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.RemoveXattr(p, name)
}

func (f *configViewFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	if pathWithinRoot("/opt", vfs.CleanPath(p)) {
		return vfs.ErrReadOnly
	}
	x, ok := f.WritableFS.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.PreserveAndRecreateXattrs(p, attrs)
}

func (f *configViewFS) MigrateXattrs(p string, migration vfs.XattrMigration) error {
	if pathWithinRoot("/opt", vfs.CleanPath(p)) {
		return vfs.ErrReadOnly
	}
	x, ok := f.WritableFS.(vfs.XattrMaintenance)
	if !ok {
		return syscall.ENOTSUP
	}
	return x.MigrateXattrs(p, migration)
}

var (
	_ vfs.WritableFS        = (*configViewFS)(nil)
	_ vfs.ChangeSetAdmitter = (*configViewFS)(nil)
	_ vfs.XattrReader       = (*configViewFS)(nil)
	_ vfs.XattrWriter       = (*configViewFS)(nil)
	_ vfs.XattrMaintenance  = (*configViewFS)(nil)
)
