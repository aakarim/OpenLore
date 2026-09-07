package openlore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/pkg/sftp"
)

// SFTPHandler implements the SFTP server interfaces using a vfs.FileSystem.
type SFTPHandler struct {
	fs             vfs.FileSystem
	writesDisabled bool
}

// NewSFTPHandler creates a new SFTP handler backed by the given filesystem.
func NewSFTPHandler(fs vfs.FileSystem) *SFTPHandler {
	return &SFTPHandler{fs: fs}
}

// Fileread handles SFTP file read requests.
func (h *SFTPHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	if r.Method != "Get" {
		return nil, sftp.ErrSSHFxOpUnsupported
	}

	data, err := h.fs.ReadFile(r.Filepath)
	if err != nil {
		return nil, os.ErrNotExist
	}

	return bytes.NewReader(data), nil
}

// Filewrite stages an SFTP upload in memory. Closing the returned writer
// commits the complete file through the same atomic, policy-controlled write
// seam used by the shell.
func (h *SFTPHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	if h.writesDisabled {
		return nil, os.ErrPermission
	}
	wfs, ok := h.fs.(vfs.WritableFS)
	if !ok {
		return nil, os.ErrPermission
	}
	if scope, ok := h.fs.(vfs.WriteScopeFS); ok && !scope.CanWrite(r.Filepath) {
		return nil, os.ErrPermission
	}

	flags := r.Pflags()
	info, statErr := h.fs.Stat(r.Filepath)
	exists := statErr == nil
	if exists && info.Dir {
		return nil, os.ErrInvalid
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if exists && flags.Creat && flags.Excl {
		return nil, os.ErrExist
	}
	if !exists && !flags.Creat {
		return nil, os.ErrNotExist
	}

	var base []byte
	if exists {
		var err error
		base, err = h.fs.ReadFile(r.Filepath)
		if err != nil {
			return nil, err
		}
	}
	var data []byte
	if !flags.Trunc {
		data = base
	}

	return &sftpAtomicWriter{
		fs:      wfs,
		path:    r.Filepath,
		data:    append([]byte(nil), data...),
		base:    append([]byte(nil), base...),
		existed: exists,
		append:  flags.Append,
		dirty:   flags.Trunc || !exists,
	}, nil
}

// Filecmd rejects namespace and metadata changes. File contents are writable
// through Filewrite, but OpenLore does not expose general SFTP filesystem
// mutation semantics such as rename, chmod, or recursive deletion.
func (h *SFTPHandler) Filecmd(r *sftp.Request) error {
	return sftp.ErrSSHFxPermissionDenied
}

// Filelist handles SFTP directory listing and stat requests.
func (h *SFTPHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		entries, err := h.fs.ReadDir(r.Filepath)
		if err != nil {
			return nil, os.ErrNotExist
		}

		var infos []os.FileInfo
		for _, e := range entries {
			infos = append(infos, sftpFileInfo{e})
		}
		sort.Slice(infos, func(i, j int) bool {
			return infos[i].Name() < infos[j].Name()
		})
		return listAt(infos), nil

	case "Stat":
		fi, err := h.fs.Stat(r.Filepath)
		if err != nil {
			return nil, os.ErrNotExist
		}
		return listAt([]os.FileInfo{sftpFileInfo{*fi}}), nil

	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

// sftpFileInfo wraps vfs.FileInfo to implement os.FileInfo.
type sftpFileInfo struct {
	info vfs.FileInfo
}

func (f sftpFileInfo) Name() string      { return f.info.FileName }
func (f sftpFileInfo) Size() int64       { return f.info.FileSize }
func (f sftpFileInfo) Mode() os.FileMode { return f.info.Mode() }
func (f sftpFileInfo) ModTime() time.Time {
	if f.info.FileModTime.IsZero() {
		return time.Now()
	}
	return f.info.FileModTime
}
func (f sftpFileInfo) IsDir() bool      { return f.info.Dir }
func (f sftpFileInfo) Sys() interface{} { return nil }

// listAt implements sftp.ListerAt.
type listAt []os.FileInfo

func (l listAt) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}

	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

// sftpAtomicWriter adapts SFTP's offset writes to OpenLore's whole-file atomic
// write contract. pkg/sftp calls Close and returns its error to the client.
type sftpAtomicWriter struct {
	mu      sync.Mutex
	fs      vfs.WritableFS
	path    string
	data    []byte
	base    []byte
	existed bool
	append  bool
	dirty   bool
	closed  bool
	aborted error
}

func (w *sftpAtomicWriter) WriteAt(p []byte, offset int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.aborted != nil {
		return 0, w.aborted
	}
	if w.append {
		offset = int64(len(w.data))
	}
	if offset < 0 || offset > int64(^uint(0)>>1)-int64(len(p)) {
		return 0, os.ErrInvalid
	}
	end := int(offset) + len(p)
	if end > len(w.data) {
		w.data = append(w.data, make([]byte, end-len(w.data))...)
	}
	copy(w.data[int(offset):], p)
	w.dirty = true
	return len(p), nil
}

func (w *sftpAtomicWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.aborted != nil || !w.dirty {
		return w.aborted
	}

	opts := vfs.WriteOpts{IfNoneMatch: !w.existed}
	if w.existed {
		sum := sha256.Sum256(w.base)
		hash := hex.EncodeToString(sum[:])
		opts.IfMatch = &hash
	}
	_, err := w.fs.WriteFileAtomic(w.path, w.data, opts)
	if errors.Is(err, vfs.ErrReadOnly) {
		return os.ErrPermission
	}
	return err
}

// TransferError prevents a partially transferred file from being committed if
// the SFTP connection ends before the handle is closed normally.
func (w *sftpAtomicWriter) TransferError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.aborted = err
}
