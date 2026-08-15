package openlore

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func hashOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newWritableDirFS(t *testing.T) (*DirFS, string) {
	t.Helper()
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	return d, dir
}

func TestDirFSFailedBatchRestoresXattrs(t *testing.T) {
	d, _ := newWritableDirFS(t)
	if err := d.MkdirAll("/tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/tree/file.md", []byte("old"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/sentinel.md", []byte("sentinel"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetXattr("/tree", "user.lore.test", []byte("directory"), 0); err != nil {
		t.Fatal(err)
	}
	badHash := "not-the-sentinel-hash"
	failingWrite := vfs.Change{Target: "/sentinel.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new"), Opts: vfs.WriteOpts{IfMatch: &badHash}}}

	_, err := vfs.CommitChangeSet(d, vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/tree", Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{}},
		failingWrite,
	}})
	if err == nil {
		t.Fatal("batch succeeded, want CAS failure")
	}
	if got, readErr := d.ReadFile("/tree/file.md"); readErr != nil || string(got) != "old" {
		t.Fatalf("restored file=%q err=%v", got, readErr)
	}
	if got, xattrErr := d.GetXattr("/tree", "user.lore.test"); xattrErr != nil || string(got) != "directory" {
		t.Fatalf("restored xattr=%q err=%v", got, xattrErr)
	}

	_, err = vfs.CommitChangeSet(d, vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/tree", Action: vfs.ChangeActionSetXattr, Xattr: &vfs.XattrChange{Name: "user.lore.test", Value: []byte("changed")}},
		failingWrite,
	}})
	if err == nil {
		t.Fatal("xattr batch succeeded, want CAS failure")
	}
	if got, xattrErr := d.GetXattr("/tree", "user.lore.test"); xattrErr != nil || string(got) != "directory" {
		t.Fatalf("xattr-only rollback=%q err=%v", got, xattrErr)
	}
}

func TestDirFS_ReadOnlyByDefault(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})

	if _, err := d.WriteFileAtomic("/a.md", []byte("x"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("write on read-only DirFS: want ErrReadOnly, got %v", err)
	}
	if err := d.Mkdir("/sub"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("mkdir on read-only DirFS: want ErrReadOnly, got %v", err)
	}
}

func TestDirFS_WriteFileAtomic_CreateAndReadBack(t *testing.T) {
	d, _ := newWritableDirFS(t)
	body := []byte("hello world\n")

	h, err := d.WriteFileAtomic("/notes.md", body, vfs.WriteOpts{})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if h != hashOf(body) {
		t.Fatalf("returned hash %s, want %s", h, hashOf(body))
	}
	got, err := d.ReadFile("/notes.md")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("readback = %q, want %q", got, body)
	}
}

func TestDirFS_WriteOpts_IfNoneMatch(t *testing.T) {
	d, _ := newWritableDirFS(t)

	// create-only on a missing file succeeds
	if _, err := d.WriteFileAtomic("/x.md", []byte("v1"), vfs.WriteOpts{IfNoneMatch: true}); err != nil {
		t.Fatalf("create-only on missing: %v", err)
	}
	// create-only on an existing file fails with a precondition error
	_, err := d.WriteFileAtomic("/x.md", []byte("v2"), vfs.WriteOpts{IfNoneMatch: true})
	var pe *vfs.PreconditionError
	if !errors.As(err, &pe) {
		t.Fatalf("create-only on existing: want PreconditionError, got %v", err)
	}
}

func TestDirFS_WriteOpts_IfMatch(t *testing.T) {
	d, _ := newWritableDirFS(t)

	// if-match on a missing file fails (cannot match a nonexistent object)
	miss := hashOf([]byte("anything"))
	_, err := d.WriteFileAtomic("/x.md", []byte("v1"), vfs.WriteOpts{IfMatch: &miss})
	var pe *vfs.PreconditionError
	if !errors.As(err, &pe) {
		t.Fatalf("if-match on missing: want PreconditionError, got %v", err)
	}

	v1 := []byte("v1")
	h1, err := d.WriteFileAtomic("/x.md", v1, vfs.WriteOpts{})
	if err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// if-match with the current hash commits
	v2 := []byte("v2")
	if _, err := d.WriteFileAtomic("/x.md", v2, vfs.WriteOpts{IfMatch: &h1}); err != nil {
		t.Fatalf("if-match current: %v", err)
	}

	// if-match with a stale hash fails
	_, err = d.WriteFileAtomic("/x.md", []byte("v3"), vfs.WriteOpts{IfMatch: &h1})
	if !errors.As(err, &pe) {
		t.Fatalf("if-match stale: want PreconditionError, got %v", err)
	}
	if pe.Current != hashOf(v2) {
		t.Fatalf("conflict reports current %s, want %s", pe.Current, hashOf(v2))
	}
}

func TestDirFS_ConcurrentOverwrite_OneFinalState_NeverTorn(t *testing.T) {
	d, _ := newWritableDirFS(t)

	const n = 50
	const size = 64 * 1024
	var wg sync.WaitGroup
	valid := make(map[string]bool)

	for i := 0; i < n; i++ {
		body := []byte(fmt.Sprintf("writer-%02d", i))
		// pad to a large size so a torn write would be detectable
		body = append(body, make([]byte, size-len(body))...)
		valid[hashOf(body)] = true
		wg.Add(1)
		go func(b []byte) {
			defer wg.Done()
			if _, err := d.WriteFileAtomic("/race.bin", b, vfs.WriteOpts{}); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		}(body)
	}
	wg.Wait()

	got, err := d.ReadFile("/race.bin")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(got) != size {
		t.Fatalf("final file size %d, want %d (torn write)", len(got), size)
	}
	if !valid[hashOf(got)] {
		t.Fatalf("final content matches no single writer (torn write)")
	}

	// No temp files should be left behind.
	entries, _ := os.ReadDir(filepath.Dir(d.resolve("/race.bin")))
	for _, e := range entries {
		if len(e.Name()) > 5 && e.Name()[:5] == ".tmp-" {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestDirFS_Mkdir_BoundaryAndSemantics(t *testing.T) {
	dir := t.TempDir()
	// Two docset roots: /chan and /team.
	d := NewDirFS(dir, config.FilesConfig{}).WithDocsetRoots([]string{"/chan", "/team"})
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	// Pre-create the docset roots on disk (they are created out-of-band, not via FS).
	for _, r := range []string{"chan", "team"} {
		if err := os.Mkdir(filepath.Join(dir, r), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Cannot create the filesystem/docset root.
	if err := d.Mkdir("/"); err == nil {
		t.Fatal("mkdir / : want error")
	}
	// Cannot create a new docset (path not below a known docset root).
	if err := d.Mkdir("/newdocset"); err == nil {
		t.Fatal("mkdir /newdocset : want error (outside docset)")
	}
	// Cannot create at a docset root itself (that would BE the docset).
	if err := d.Mkdir("/chan"); err == nil {
		t.Fatal("mkdir /chan : want error (is a docset root)")
	}
	// Can create strictly below a docset root.
	if err := d.Mkdir("/chan/sub"); err != nil {
		t.Fatalf("mkdir /chan/sub : %v", err)
	}
	if info, err := d.Stat("/chan/sub"); err != nil || !info.Dir {
		t.Fatalf("stat /chan/sub : info=%+v err=%v", info, err)
	}
	// Plain mkdir semantics: parent must exist.
	if err := d.Mkdir("/chan/missing/leaf"); err == nil {
		t.Fatal("mkdir with missing parent : want error")
	}
}

func TestDirFS_FreezeThaw_Idempotent(t *testing.T) {
	d, _ := newWritableDirFS(t)

	// Idempotent enable.
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("second SetWriteable: %v", err)
	}
	if _, err := d.WriteFileAtomic("/a.md", []byte("1"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("write while writable: %v", err)
	}

	// Freeze, then writes are blocked.
	if err := d.SetReadonly(); err != nil {
		t.Fatalf("SetReadonly: %v", err)
	}
	if err := d.SetReadonly(); err != nil {
		t.Fatalf("idempotent SetReadonly: %v", err)
	}
	if _, err := d.WriteFileAtomic("/a.md", []byte("2"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("write while frozen: want ErrReadOnly, got %v", err)
	}

	// Thaw, writes resume.
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("thaw: %v", err)
	}
	if _, err := d.WriteFileAtomic("/a.md", []byte("3"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("write after thaw: %v", err)
	}
}

func TestDirFS_OversizeRejected(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})
	d.maxWriteBytes = 16
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/big.md", make([]byte, 17), vfs.WriteOpts{}); err == nil {
		t.Fatal("oversize write: want error")
	}
	if _, err := d.WriteFileAtomic("/ok.md", make([]byte, 16), vfs.WriteOpts{}); err != nil {
		t.Fatalf("at-limit write: %v", err)
	}
}

//go:embed testdata_embed
var embedFixture embed.FS

func TestEmbedFS_NotWritable_FailFast(t *testing.T) {
	var _ vfs.FileSystem = (*EmbedFS)(nil)
	e := NewEmbedFS(embedFixture, "testdata_embed", config.FilesConfig{})
	if _, ok := interface{}(e).(vfs.WritableFS); ok {
		t.Fatal("EmbedFS must NOT implement vfs.WritableFS")
	}

	// A MergeFS whose only backend is an EmbedFS must fail fast when asked to
	// become writable (readonly=false with a read-only-only substrate).
	m := NewMergeFS()
	m.SetRoot(e)
	if err := m.SetWriteable(); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("SetWriteable on embed-only merge: want ErrReadOnly, got %v", err)
	}
}

func TestMergeFS_RoutesWrites_AndBlocksDocsetCreation(t *testing.T) {
	dir := t.TempDir()
	mountDir := filepath.Join(dir, "chan")
	if err := os.Mkdir(mountDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(mountDir, config.FilesConfig{})

	m := NewMergeFS()
	m.Mount("chan", d)
	if err := m.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}

	// Write routes to the mount.
	if _, err := m.WriteFileAtomic("/chan/note.md", []byte("hi"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("write to mount: %v", err)
	}
	got, err := m.ReadFile("/chan/note.md")
	if err != nil || string(got) != "hi" {
		t.Fatalf("readback: %q err=%v", got, err)
	}

	// Cannot create a docset at the merge root.
	if err := m.Mkdir("/newdocset"); err == nil {
		t.Fatal("mkdir /newdocset on merge: want error")
	}
	// Cannot create a mount/docset root.
	if err := m.Mkdir("/chan"); err == nil {
		t.Fatal("mkdir /chan on merge: want error (docset root)")
	}
	// Can create a folder inside the mounted docset.
	if err := m.Mkdir("/chan/sub"); err != nil {
		t.Fatalf("mkdir /chan/sub on merge: %v", err)
	}
}
