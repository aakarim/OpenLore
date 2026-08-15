package openlore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestDirFSXattrsRoundTripFlagsAndPersistence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	value := []byte{0, 0xff, 'x'}
	if err := d.SetXattr("/folder", "user.lore.kind", value, vfs.XattrCreate); err != nil {
		t.Fatal(err)
	}
	value[0] = 9
	got, err := d.GetXattr("/folder", "user.lore.kind")
	if err != nil || !bytes.Equal(got, []byte{0, 0xff, 'x'}) {
		t.Fatalf("got %x, %v", got, err)
	}
	if err := d.SetXattr("/folder", "user.lore.kind", nil, vfs.XattrCreate); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("create: %v", err)
	}
	if err := d.SetXattr("/folder", "user.lore.missing", nil, vfs.XattrReplace); !errors.Is(err, syscall.ENODATA) {
		t.Fatalf("replace: %v", err)
	}
	if err := d.SetXattr("/folder", "user.lore.empty", nil, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := d.GetXattr("/folder", "user.lore.empty"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty value = %#v, %v", got, err)
	}
	if err := d.RemoveXattr("/folder", "user.lore.kind"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "folder", ".lore", "xattrs", "self")); err != nil {
		t.Fatal("empty envelope not persisted:", err)
	}
}

func TestDirFSXattrValidationCorruptionAndLoreHiding(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0755); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{})
	_ = d.SetWriteable()
	for _, tc := range []struct {
		name  string
		value []byte
		flags vfs.XattrFlags
	}{{"other.x", nil, 0}, {"user.lore.x", nil, 3}, {"user.lore.x", make([]byte, 65537), 0}} {
		if err := d.SetXattr("/folder", tc.name, tc.value, tc.flags); !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ERANGE) && !errors.Is(err, syscall.ENOTSUP) {
			t.Fatalf("validation %q: %v", tc.name, err)
		}
	}
	if err := d.SetXattr("/folder", "user.lore.x", []byte("ok"), 0); err != nil {
		t.Fatal(err)
	}
	entries, err := d.ReadDir("/folder")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.FileName == ".lore" {
			t.Fatal(".lore exposed")
		}
	}
	if _, err := d.Stat("/folder/.lore"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat .lore: %v", err)
	}
	self := filepath.Join(root, "folder", ".lore", "xattrs", "self")
	b1, _ := os.ReadFile(self)
	if err := d.SetXattr("/folder", "user.lore.x", []byte("ok"), 0); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(self)
	if !bytes.Equal(b1, b2) {
		t.Fatal("canonical encoding is nondeterministic")
	}
	if err := os.WriteFile(self, []byte{0xff}, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ListXattrs("/folder"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("corrupt envelope: %v", err)
	}
}

func TestDirFSXattrRepairPreservesExactConflictAndIsRetryable(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetXattr("/folder", "user.lore.before", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(root, "folder", ".lore", "xattrs", "self")
	conflict := []byte{0xff, 0x00, 0x01, 0x02}
	if err := os.WriteFile(self, conflict, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := map[string][]byte{"user.lore.plugins.openlore.skills.v1": {}}
	if err := d.PreserveAndRecreateXattrs("/folder", fresh); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(conflict)
	backup := filepath.Join(filepath.Dir(self), "self.conflicted."+fmtHex(sum[:]))
	got, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(got, conflict) {
		t.Fatalf("backup = %x, %v", got, err)
	}
	if got, err := d.GetXattr("/folder", "user.lore.plugins.openlore.skills.v1"); err != nil || len(got) != 0 {
		t.Fatalf("fresh marker = %x, %v", got, err)
	}

	// Simulate interruption after preserving the forensic copy but before
	// replacing self. A retry verifies and reuses the deterministic backup.
	if err := os.WriteFile(self, conflict, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.PreserveAndRecreateXattrs("/folder", fresh); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := os.WriteFile(self, conflict, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(backup); err != nil || string(got) != "different" {
		t.Fatalf("failed to replace forensic fixture: %q, %v", got, err)
	}
	if err := d.PreserveAndRecreateXattrs("/folder", fresh); !errors.Is(err, syscall.EIO) {
		t.Fatalf("mismatched forensic backup = %v", err)
	}
}

func TestDirFSXattrMigrationIsAtomicAndConditional(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{
		"user.lore.plugins.example.skill.v1": []byte("old"),
		"user.lore.unrelated":                []byte("keep"),
	} {
		if err := d.SetXattr("/folder", name, value, 0); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "folder", ".lore", "xattrs", "self"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	migration := vfs.XattrMigration{
		NamespacePrefix:        "user.lore.plugins.example.skill.",
		ExpectedEnvelopeSHA256: digest[:],
		Edits: []vfs.XattrEdit{
			{Name: "user.lore.plugins.example.skill.v1", Remove: true},
			{Name: "user.lore.plugins.example.skill.v2", Value: []byte("new")},
		},
	}
	if err := d.MigrateXattrs("/folder", migration); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetXattr("/folder", "user.lore.plugins.example.skill.v1"); !errors.Is(err, syscall.ENODATA) {
		t.Fatalf("old marker = %v", err)
	}
	if got, err := d.GetXattr("/folder", "user.lore.plugins.example.skill.v2"); err != nil || string(got) != "new" {
		t.Fatalf("new marker = %q, %v", got, err)
	}
	if got, err := d.GetXattr("/folder", "user.lore.unrelated"); err != nil || string(got) != "keep" {
		t.Fatalf("unrelated = %q, %v", got, err)
	}
	if err := d.MigrateXattrs("/folder", migration); err == nil {
		t.Fatalf("stale migration unexpectedly succeeded: %v", err)
	} else {
		var stale *vfs.XattrStaleError
		if !errors.As(err, &stale) {
			t.Fatalf("stale migration error = %T %v", err, err)
		}
	}
	migration.NamespacePrefix = "user.lore.plugins.other."
	migration.ExpectedEnvelopeSHA256 = make([]byte, 32)
	if err := d.MigrateXattrs("/folder", migration); err == nil {
		t.Fatal("out-of-namespace migration accepted")
	}
}

func fmtHex(b []byte) string {
	const digits = "0123456789abcdef"
	var out strings.Builder
	out.Grow(len(b) * 2)
	for _, value := range b {
		out.WriteByte(digits[value>>4])
		out.WriteByte(digits[value&15])
	}
	return out.String()
}
