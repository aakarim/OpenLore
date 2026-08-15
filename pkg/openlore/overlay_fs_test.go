package openlore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func newTestOverlay(t *testing.T) (*OverlayFS, string) {
	t.Helper()
	upperDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(upperDir, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(upperDir, "upper-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upperDir, "shared", "upper.md"), []byte("upper"), 0o644); err != nil {
		t.Fatal(err)
	}
	lower := NewFSAdapter(fstest.MapFS{
		"lower.md":        &fstest.MapFile{Data: []byte("lower")},
		"shared/lower.md": &fstest.MapFile{Data: []byte("lower child")},
		"shared/upper.md": &fstest.MapFile{Data: []byte("shadowed")},
		"upper-dir":       &fstest.MapFile{Data: []byte("lower file")},
	})
	overlay := NewOverlayFS(NewDirFS(upperDir, config.FilesConfig{}), lower)
	if err := overlay.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	return overlay, upperDir
}

func TestOverlayFSReadsUpperAndLowerAndMergesDirectories(t *testing.T) {
	overlay, _ := newTestOverlay(t)

	if got, err := overlay.ReadFile("/lower.md"); err != nil || string(got) != "lower" {
		t.Fatalf("lower read = %q, %v", got, err)
	}
	if got, err := overlay.ReadFile("/shared/upper.md"); err != nil || string(got) != "upper" {
		t.Fatalf("shadowed read = %q, %v", got, err)
	}
	entries, err := overlay.ReadDir("/shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].FileName != "lower.md" || entries[1].FileName != "upper.md" {
		t.Fatalf("merged entries = %+v", entries)
	}
	if _, err := overlay.ReadDir("/upper-dir"); err != nil {
		t.Fatalf("upper directory must shadow lower file: %v", err)
	}
}

func TestOverlayFSWritesAgainstVisibleLowerCAS(t *testing.T) {
	overlay, upperDir := newTestOverlay(t)
	sum := sha256.Sum256([]byte("lower"))
	want := hex.EncodeToString(sum[:])

	stale := "stale"
	if _, err := overlay.WriteFileAtomic("/lower.md", []byte("new"), vfs.WriteOpts{IfMatch: &stale}); err == nil {
		t.Fatal("stale lower write: want precondition error")
	}
	if _, err := overlay.WriteFileAtomic("/lower.md", []byte("new"), vfs.WriteOpts{IfMatch: &want}); err != nil {
		t.Fatalf("matching lower write: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(upperDir, "lower.md")); err != nil || string(got) != "new" {
		t.Fatalf("upper copy = %q, %v", got, err)
	}
	if _, err := overlay.WriteFileAtomic("/shared/lower.md", []byte("new"), vfs.WriteOpts{IfNoneMatch: true}); err == nil {
		t.Fatal("create-only over lower file: want precondition error")
	}
}

func TestOverlayFSRejectsLowerBackedRemoval(t *testing.T) {
	overlay, upperDir := newTestOverlay(t)

	if err := overlay.Remove("/lower.md"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("remove lower = %v, want ErrReadOnly", err)
	}
	if err := os.WriteFile(filepath.Join(upperDir, "only-upper.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := overlay.Remove("/only-upper.md"); err != nil {
		t.Fatalf("remove upper: %v", err)
	}
}

func TestNewServerWithLowerFSUsesWritableDirAtRoot(t *testing.T) {
	upperDir := t.TempDir()
	lower := fstest.MapFS{"embedded.md": &fstest.MapFile{Data: []byte("embedded")}}
	s, err := NewServerWithLowerFS(lower, WithWritableDir(upperDir), WithReadonly(false))
	if err != nil {
		t.Fatalf("NewServerWithLowerFS: %v", err)
	}
	defer s.Shutdown(context.Background())
	if s.writeLog == nil {
		t.Fatal("writable overlay must initialize write log")
	}
	if got, err := s.merge.ReadFile("/embedded.md"); err != nil || string(got) != "embedded" {
		t.Fatalf("embedded read = %q, %v", got, err)
	}
	if _, err := s.merge.WriteFileAtomic("/agent/jared/note.md", []byte("hi"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("overlay write: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(upperDir, "agent", "jared", "note.md")); err != nil || string(got) != "hi" {
		t.Fatalf("physical write = %q, %v", got, err)
	}
}

func TestOverlayFSMkdirAllMaterializesLowerOnlyNestedDocset(t *testing.T) {
	upperDir := t.TempDir()
	upper := NewDirFS(upperDir, config.FilesConfig{}).WithDocsetRoots([]string{"/", "/agent/jared"})
	lower := NewFSAdapter(fstest.MapFS{
		"agent/jared/existing.md": &fstest.MapFile{Data: []byte("existing")},
	})
	overlay := NewOverlayFS(upper, lower)
	if err := overlay.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := overlay.MkdirAll("/agent/jared/new/deep"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if info, err := os.Stat(filepath.Join(upperDir, "agent", "jared", "new", "deep")); err != nil || !info.IsDir() {
		t.Fatalf("physical nested dir: info=%v err=%v", info, err)
	}
	if root, ok := upper.docsetRootFor("/agent/jared/new"); !ok || root != "/agent/jared" {
		t.Fatalf("docset root = %q, %v", root, ok)
	}
}

func TestOverlayFSRejectsDeletingNestedDocsetRoot(t *testing.T) {
	upperDir := t.TempDir()
	root := filepath.Join(upperDir, "agent", "jared")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	upper := NewDirFS(upperDir, config.FilesConfig{}).WithDocsetRoots([]string{"/", "/agent/jared"})
	overlay := NewOverlayFS(upper, nil)
	if err := overlay.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := overlay.RemoveAll("/agent/jared", vfs.RemoveOpts{}); err == nil {
		t.Fatal("RemoveAll nested docset root: want error")
	}
	if got, err := os.ReadFile(filepath.Join(root, "note.md")); err != nil || string(got) != "keep" {
		t.Fatalf("nested docset content = %q, %v", got, err)
	}
}
