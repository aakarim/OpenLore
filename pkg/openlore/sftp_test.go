package openlore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/pkg/sftp"
)

const (
	sftpWrite = 1 << 1
	sftpCreat = 1 << 3
	sftpTrunc = 1 << 4
)

func TestSFTPWriteCommitsCompleteFileOnClose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := NewDirFS(dir, config.FilesConfig{})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}

	req := sftp.NewRequest("Put", "/note.md")
	req.Flags = sftpWrite | sftpTrunc
	writer, err := NewSFTPHandler(fsys).Filewrite(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("world"), 6); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("hello "), 0); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, fsys, "/note.md")); got != "original" {
		t.Fatalf("write became visible before close: %q", got)
	}
	if err := writer.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, fsys, "/note.md")); got != "hello world" {
		t.Fatalf("committed content = %q", got)
	}
}

func TestSFTPWriteCreatesEmptyFile(t *testing.T) {
	fsys := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	req := sftp.NewRequest("Put", "/empty.md")
	req.Flags = sftpWrite | sftpCreat
	writer, err := NewSFTPHandler(fsys).Filewrite(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, fsys, "/empty.md"); len(got) != 0 {
		t.Fatalf("empty file contains %q", got)
	}
}

func TestSFTPWriteRejectsConcurrentChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := NewDirFS(dir, config.FilesConfig{})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	req := sftp.NewRequest("Put", "/note.md")
	req.Flags = sftpWrite | sftpTrunc
	writer, err := NewSFTPHandler(fsys).Filewrite(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("editor"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("newer"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = writer.(io.Closer).Close()
	var conflict *vfs.PreconditionError
	if !errors.As(err, &conflict) {
		t.Fatalf("close error = %v, want precondition error", err)
	}
	if got := string(mustReadFile(t, fsys, "/note.md")); got != "newer" {
		t.Fatalf("concurrent content was overwritten: %q", got)
	}
}

func TestSFTPWriteAbortsIncompleteTransfer(t *testing.T) {
	fsys := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	req := sftp.NewRequest("Put", "/partial.md")
	req.Flags = sftpWrite | sftpCreat | sftpTrunc
	writer, err := NewSFTPHandler(fsys).Filewrite(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("partial"), 0); err != nil {
		t.Fatal(err)
	}
	writer.(*sftpAtomicWriter).TransferError(io.ErrUnexpectedEOF)
	if err := writer.(io.Closer).Close(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("close error = %v", err)
	}
	if _, err := fsys.Stat("/partial.md"); err == nil {
		t.Fatal("partial transfer was committed")
	}
}

func TestSFTPWriteCanBeDisabledForReadonlyServer(t *testing.T) {
	fsys := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	handler := NewSFTPHandler(fsys)
	handler.writesDisabled = true
	req := sftp.NewRequest("Put", "/note.md")
	req.Flags = sftpWrite | sftpCreat | sftpTrunc
	if _, err := handler.Filewrite(req); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Filewrite error = %v, want permission denied", err)
	}
}

func mustReadFile(t *testing.T, fsys vfs.FileSystem, path string) []byte {
	t.Helper()
	data, err := fsys.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
