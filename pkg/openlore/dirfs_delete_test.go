package openlore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestDirFS_MkdirAll_BoundaryAndParents(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{}).WithDocsetRoots([]string{"/chan"})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "chan"), 0o755); err != nil {
		t.Fatal(err)
	}

	// mkdir -p creates intermediate dirs strictly below the docset root.
	if err := d.MkdirAll("/chan/a/b/c"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, p := range []string{"/chan/a", "/chan/a/b", "/chan/a/b/c"} {
		if info, err := d.Stat(p); err != nil || !info.Dir {
			t.Fatalf("stat %s: info=%+v err=%v", p, info, err)
		}
	}
	// Existing dir is a no-op success.
	if err := d.MkdirAll("/chan/a/b"); err != nil {
		t.Fatalf("MkdirAll existing: %v", err)
	}
	// Cannot create a new docset (/other is not a declared docset root).
	if err := d.MkdirAll("/other/x"); err == nil {
		t.Fatal("MkdirAll /other/x: want error (not a declared docset)")
	}
	// Cannot create a docset root.
	if err := d.MkdirAll("/chan"); err == nil {
		t.Fatal("MkdirAll /chan: want error (is a docset root)")
	}
}

// A docset declared in lore.json whose root is not on disk yet must accept
// mkdir and mkdir -p below it (OPE-53), on both writable substrates.
func TestMkdir_MaterializesMissingDeclaredDocsetRoot(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		dir := t.TempDir()
		d := NewDirFS(dir, config.FilesConfig{}).WithDocsetRoots([]string{"/acme", "/beta", "/gamma"})
		var w vfs.WritableFS = d
		if overlay {
			w = NewOverlayFS(d, NewFSAdapter(fstest.MapFS{}, config.FilesConfig{}))
		}
		if err := w.SetWriteable(); err != nil {
			t.Fatal(err)
		}
		if err := w.MkdirAll("/acme/brand/assets"); err != nil {
			t.Fatalf("overlay=%v MkdirAll below missing root: %v", overlay, err)
		}
		if err := w.Mkdir("/beta/brand"); err != nil {
			t.Fatalf("overlay=%v Mkdir below missing root: %v", overlay, err)
		}
		for _, p := range []string{"acme/brand/assets", "beta/brand"} {
			if info, err := os.Stat(filepath.Join(dir, p)); err != nil || !info.IsDir() {
				t.Fatalf("overlay=%v %s not created on disk: %v", overlay, p, err)
			}
		}
		if err := w.MkdirAll("/undeclared/x"); err == nil {
			t.Fatalf("overlay=%v MkdirAll under undeclared root: want error", overlay)
		}
		// A mkdir that fails (missing deeper parent) or is denied by policy
		// must not leave the docset root behind.
		if err := w.Mkdir("/gamma/missing/leaf"); err == nil {
			t.Fatalf("overlay=%v Mkdir with missing parent: want error", overlay)
		}
		if err := w.Mkdir("/gamma/.lore"); err == nil {
			t.Fatalf("overlay=%v Mkdir of reserved path: want error", overlay)
		}
		if err := w.MkdirAll("/gamma/.lore/x"); err == nil {
			t.Fatalf("overlay=%v MkdirAll of reserved path: want error", overlay)
		}
		if _, err := os.Stat(filepath.Join(dir, "gamma")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("overlay=%v failed mkdir left /gamma on disk: %v", overlay, err)
		}
	}
}

func TestDirFS_Remove_FileAndEmptyDir(t *testing.T) {
	d, _ := newWritableDirFS(t)
	if _, err := d.WriteFileAtomic("/a.md", []byte("x"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("/a.md"); err != nil {
		t.Fatalf("Remove file: %v", err)
	}
	if _, err := d.Stat("/a.md"); err == nil {
		t.Fatal("file should be gone")
	}

	if err := d.Mkdir("/emptydir"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("/emptydir"); err != nil {
		t.Fatalf("Remove empty dir: %v", err)
	}
}

func TestDirFS_Remove_NonEmptyDirFails(t *testing.T) {
	d, _ := newWritableDirFS(t)
	if err := d.Mkdir("/d"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/d/f.md", []byte("x"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("/d"); err == nil {
		t.Fatal("Remove non-empty dir: want error")
	}
}

func TestDirFS_RemoveAll_Tree(t *testing.T) {
	d, _ := newWritableDirFS(t)
	if err := d.Mkdir("/d"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/d/a.md", []byte("a"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Mkdir("/d/sub"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/d/sub/b.md", []byte("b"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("/d", vfs.RemoveOpts{}); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := d.Stat("/d"); err == nil {
		t.Fatal("/d should be gone")
	}
}

func TestDirFS_RemoveAll_BoundaryRefusesRoots(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{}).WithDocsetRoots([]string{"/chan"})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "chan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("/", vfs.RemoveOpts{}); err == nil {
		t.Fatal("RemoveAll /: want error")
	}
	if err := d.RemoveAll("/chan", vfs.RemoveOpts{}); err == nil {
		t.Fatal("RemoveAll /chan (docset root): want error")
	}
	if err := d.RemoveAll("/outside", vfs.RemoveOpts{}); err == nil {
		t.Fatal("RemoveAll /outside (not in docset): want error")
	}
}

func TestDirFS_RemoveAll_TreeCAS(t *testing.T) {
	d, _ := newWritableDirFS(t)
	if err := d.Mkdir("/d"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/d/a.md", []byte("a"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}

	// A snapshot expecting different content must fail with TreeStaleError.
	stale := &vfs.TreeSnapshot{Root: "/d", Ops: []vfs.TreeOp{
		{RelPath: ".", Kind: "dir"},
		{RelPath: "a.md", Kind: "file", Hash: hashOf([]byte("different"))},
	}}
	err := d.RemoveAll("/d", vfs.RemoveOpts{Expected: stale})
	var te *vfs.TreeStaleError
	if !errors.As(err, &te) {
		t.Fatalf("want TreeStaleError, got %v", err)
	}
	if _, serr := d.Stat("/d"); serr != nil {
		t.Fatal("/d should still exist after stale delete")
	}

	// A matching snapshot commits the delete.
	good := &vfs.TreeSnapshot{Root: "/d", Ops: []vfs.TreeOp{
		{RelPath: ".", Kind: "dir"},
		{RelPath: "a.md", Kind: "file", Hash: hashOf([]byte("a"))},
	}}
	if err := d.RemoveAll("/d", vfs.RemoveOpts{Expected: good}); err != nil {
		t.Fatalf("RemoveAll with matching snapshot: %v", err)
	}
	if _, serr := d.Stat("/d"); serr == nil {
		t.Fatal("/d should be gone")
	}
}

func TestDirFS_RemoveAll_RefusesHiddenDescendant(t *testing.T) {
	dir := t.TempDir()
	// Deny *.secret files from the VFS view.
	d := NewDirFS(dir, config.FilesConfig{Denied: []string{"*.secret"}})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A physically-present file that the VFS hides.
	if err := os.WriteFile(filepath.Join(dir, "d", "x.secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("/d", vfs.RemoveOpts{}); err == nil {
		t.Fatal("RemoveAll of tree with hidden descendant: want refusal")
	}
	// The tree must be untouched.
	if _, err := os.Stat(filepath.Join(dir, "d", "x.secret")); err != nil {
		t.Fatal("hidden file should still exist after refused delete")
	}
}

func TestDirFS_TrashHiddenFromReads(t *testing.T) {
	d, dir := newWritableDirFS(t)
	// Manufacture a leftover trash dir.
	if err := os.MkdirAll(filepath.Join(dir, trashDirName, "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Stat("/" + trashDirName); err == nil {
		t.Fatal("trash path should not be stat-able")
	}
	entries, err := d.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.FileName == trashDirName {
			t.Fatal("trash dir should not appear in ReadDir")
		}
	}
}

func TestDirFS_RemoveAll_ReadOnly(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("/d", vfs.RemoveOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("RemoveAll on read-only DirFS: want ErrReadOnly, got %v", err)
	}
}
