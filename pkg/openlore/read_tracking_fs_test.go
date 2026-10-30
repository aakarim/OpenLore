package openlore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// trackingShell builds a writable shell whose FS tracks read hashes for
// session-level CAS, plus the raw substrate for out-of-band changes.
func trackingShell(t *testing.T) (*shell.Shell, *DirFS) {
	t.Helper()
	d := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	return shell.NewShell(newReadTrackingFS(d)), d
}

// After reading a file, overwriting it succeeds when it hasn't changed.
func TestReadTracking_ReadThenOverwrite_Succeeds(t *testing.T) {
	sh, d := trackingShell(t)
	if _, err := d.WriteFileAtomic("/notes.md", []byte("v1\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, code := run(sh, "cat /notes.md"); code != 0 {
		t.Fatal("read failed")
	}
	if _, errs, code := run(sh, "echo v2 > /notes.md"); code != 0 {
		t.Fatalf("overwrite after read should succeed: %q", errs)
	}
	if got, _ := d.ReadFile("/notes.md"); string(got) != "v2\n" {
		t.Fatalf("got %q", got)
	}
}

// The core ask: if the file changed since the session last read it, a blind
// overwrite is rejected — the caller never named a hash.
func TestReadTracking_OverwriteStaleAfterConcurrentChange_Fails(t *testing.T) {
	sh, d := trackingShell(t)
	if _, err := d.WriteFileAtomic("/notes.md", []byte("v1\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Session reads v1.
	if _, _, code := run(sh, "cat /notes.md"); code != 0 {
		t.Fatal("read failed")
	}
	// Someone else changes it out of band.
	if _, err := d.WriteFileAtomic("/notes.md", []byte("theirs\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("concurrent write: %v", err)
	}
	// The stale overwrite must fail and must not clobber their change.
	_, errs, code := run(sh, "echo mine > /notes.md")
	if code == 0 {
		t.Fatal("overwrite of a file changed since last read must fail")
	}
	if !strings.Contains(errs, "changed concurrently") {
		t.Fatalf("expected concurrency error, got %q", errs)
	}
	if got, _ := d.ReadFile("/notes.md"); string(got) != "theirs\n" {
		t.Fatalf("concurrent change must survive, got %q", got)
	}
}

func TestReferenceScansDoNotAdvanceSessionCAS(t *testing.T) {
	for _, command := range []string{"mv /docs/a.md /docs/b.md", "lore links --to /docs/a.md"} {
		t.Run(command, func(t *testing.T) {
			s, root := folderIndexServer(t)
			folderIndexWrite(t, s, "/docs/a.md", "move me")
			folderIndexWrite(t, s, "/docs/notes.md", "[old](/docs/a.md)")
			sh := s.buildSessionShell(testIdentity("alice"))
			if _, errs, code := run(sh, "cat /docs/notes.md"); code != 0 {
				t.Fatal(errs)
			}
			if err := os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte("unseen concurrent edit"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, errs, code := run(sh, command); code != 0 {
				t.Fatalf("reference scan: %s", errs)
			}
			if _, errs, code := run(sh, "echo mine > /docs/notes.md"); code == 0 || !strings.Contains(errs, "changed concurrently") {
				t.Fatalf("reference scan advanced unseen CAS baseline: code=%d stderr=%s", code, errs)
			}
		})
	}
}

// A successful write updates the tracked hash, so repeated overwrites after a
// single read chain correctly (no spurious rejection on the second write).
func TestReadTracking_RepeatedWritesChain(t *testing.T) {
	sh, d := trackingShell(t)
	if _, err := d.WriteFileAtomic("/notes.md", []byte("v1\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, code := run(sh, "cat /notes.md"); code != 0 {
		t.Fatal("read failed")
	}
	if _, errs, code := run(sh, "echo v2 > /notes.md"); code != 0 {
		t.Fatalf("first overwrite: %q", errs)
	}
	if _, errs, code := run(sh, "echo v3 > /notes.md"); code != 0 {
		t.Fatalf("second overwrite should chain off the first write: %q", errs)
	}
	if got, _ := d.ReadFile("/notes.md"); string(got) != "v3\n" {
		t.Fatalf("got %q", got)
	}
}

// Writing a file that exists but was never read this session falls back to a
// blind overwrite (no baseline to protect) rather than failing.
func TestReadTracking_UnreadExistingFile_FallsBack(t *testing.T) {
	sh, d := trackingShell(t)
	if _, err := d.WriteFileAtomic("/notes.md", []byte("v1\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// No read first.
	if _, errs, code := run(sh, "echo v2 > /notes.md"); code != 0 {
		t.Fatalf("unread overwrite should fall back and succeed: %q", errs)
	}
	if got, _ := d.ReadFile("/notes.md"); string(got) != "v2\n" {
		t.Fatalf("got %q", got)
	}
}

// Creating a brand-new file (never read, does not exist) works.
func TestReadTracking_CreateNewFile(t *testing.T) {
	sh, d := trackingShell(t)
	if _, errs, code := run(sh, "echo hi > /fresh.md"); code != 0 {
		t.Fatalf("create should succeed: %q", errs)
	}
	if got, _ := d.ReadFile("/fresh.md"); string(got) != "hi\n" {
		t.Fatalf("got %q", got)
	}
}
