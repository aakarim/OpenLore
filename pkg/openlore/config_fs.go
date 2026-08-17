package openlore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

const authConfigVFSPath = "/opt/openlore/lore.json"

type configViewFS struct {
	vfs.WritableFS
	server      *Server
	attribution Attribution
}

func (f *configViewFS) Stat(name string) (*vfs.FileInfo, error) {
	clean := vfs.CleanPath(name)
	if clean == "/opt" || clean == "/opt/openlore" {
		return &vfs.FileInfo{FileName: path.Base(clean), FilePath: clean, Dir: true}, nil
	}
	if clean != authConfigVFSPath {
		return f.WritableFS.Stat(name)
	}
	b, err := os.ReadFile(f.server.config.AuthFile)
	if err != nil {
		return nil, err
	}
	info, _ := os.Stat(f.server.config.AuthFile)
	return &vfs.FileInfo{FileName: "lore.json", FilePath: clean, FileSize: int64(len(b)), FileModTime: info.ModTime()}, nil
}

func (f *configViewFS) ReadDir(name string) ([]vfs.FileInfo, error) {
	switch vfs.CleanPath(name) {
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
		return append(entries, vfs.FileInfo{FileName: "opt", FilePath: "/opt", Dir: true}), nil
	case "/opt":
		return []vfs.FileInfo{{FileName: "openlore", FilePath: "/opt/openlore", Dir: true}}, nil
	case "/opt/openlore":
		return []vfs.FileInfo{{FileName: "lore.json", FilePath: authConfigVFSPath}}, nil
	default:
		return f.WritableFS.ReadDir(name)
	}
}

func (f *configViewFS) ReadFile(name string) ([]byte, error) {
	if vfs.CleanPath(name) == authConfigVFSPath {
		return os.ReadFile(f.server.config.AuthFile)
	}
	return f.WritableFS.ReadFile(name)
}

func (f *configViewFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	if vfs.CleanPath(name) != authConfigVFSPath {
		return f.WritableFS.WriteFileAtomic(name, data, opts)
	}
	f.server.authMu.Lock()
	defer f.server.authMu.Unlock()
	current, err := os.ReadFile(f.server.config.AuthFile)
	if err != nil {
		return "", err
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
	if parseErr != nil {
		if f.server.audit != nil {
			_ = f.server.audit.Record(context.Background(), AuditEvent{Type: "config.reject", Attribution: f.attribution, Details: map[string]any{"error": parseErr.Error()}})
		}
		return "", parseErr
	}
	tmp := f.server.config.AuthFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, f.server.config.AuthFile); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if f.server.audit != nil {
		_ = f.server.audit.Record(context.Background(), AuditEvent{Type: "config.edit", Attribution: f.attribution})
	}
	return hex.EncodeToString(sum[:]), nil
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

var _ vfs.WritableFS = (*configViewFS)(nil)
