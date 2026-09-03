package openlore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestDirFSExposesOnlyFolderConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs", "backend")
	if err := os.MkdirAll(filepath.Join(dir, ".lore", "xattrs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lore", "config.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lore", "xattrs", "self"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{Allowed: []string{"*.md"}}).WithDocsetRoots([]string{"/docs"})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}

	if got, err := d.ReadFile("/docs/backend/.lore/config.yaml"); err != nil || string(got) != "version: 1\n" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if _, err := d.Stat("/docs/backend/.lore/config.yaml"); err != nil {
		t.Fatal(err)
	}
	entries, err := d.ReadDir("/docs/backend")
	if err != nil || len(entries) != 1 || entries[0].FileName != ".lore" || !entries[0].Dir {
		t.Fatalf("backend entries = %#v, %v", entries, err)
	}
	entries, err = d.ReadDir("/docs/backend/.lore")
	if err != nil || len(entries) != 1 || entries[0].FileName != "config.yaml" {
		t.Fatalf(".lore entries = %#v, %v", entries, err)
	}
	if _, err := d.ReadFile("/docs/backend/.lore/xattrs/self"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hidden read: %v", err)
	}
	if _, err := d.WriteFileAtomic("/docs/backend/.lore/other.yaml", nil, vfs.WriteOpts{}); err == nil {
		t.Fatal("write under .lore succeeded")
	}
	if _, err := d.WriteFileAtomic("/docs/backend/.lore/xattrs/.lore/config.yaml", nil, vfs.WriteOpts{}); err == nil {
		t.Fatal("nested .lore config path escaped reservation")
	}
	if err := d.Mkdir("/docs/backend/.lore"); err == nil {
		t.Fatal("mkdir .lore succeeded")
	}
	if _, err := d.WriteFileAtomic("/docs/backend/.lore/config.yaml", []byte("version: 1\nrules: {}\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("config write: %v", err)
	}
	if err := d.RemoveAll("/docs/backend", vfs.RemoveOpts{}); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backend remains: %v", err)
	}
}

func TestDirFSDoesNotListLoreWithoutConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", ".lore", "xattrs"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{})
	entries, err := d.ReadDir("/docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.FileName == ".lore" {
			t.Fatal(".lore listed without config.yaml")
		}
	}
}
