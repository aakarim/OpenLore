package openlore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// rootsAuthz builds a writeAuthorizer that permits writes strictly inside any of
// the given display roots — the classic per-identity write-isolation behavior.
func rootsAuthz(roots ...string) writeAuthorizer {
	return func(_ vfs.ChangeAction, p string) bool {
		clean := vfs.CleanPath(p)
		for _, r := range roots {
			r = vfs.CleanPath(r)
			if r == "/" {
				if clean != "/" {
					return true
				}
				continue
			}
			if strings.HasPrefix(clean, r+"/") {
				return true
			}
		}
		return false
	}
}

// Part B per-identity write isolation. Two agents share a lore (so they can see
// the same docsets), but each session is scoped to the docset roots it may
// publish to — so neither can write the other's docset, and neither can write
// the read-only root.
func TestScopedWriteFS_ConfinesWritesToRoots(t *testing.T) {
	dir := t.TempDir()
	base := NewDirFS(dir, config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	// Pre-create the docset roots so writes land somewhere real.
	for _, d := range []string{"/jared", "/claw"} {
		if err := base.Mkdir(d); err != nil {
			t.Fatalf("seed mkdir %s: %v", d, err)
		}
	}

	// This session may only write /jared.
	fs := newScopedWriteFS(base, rootsAuthz("/jared"))

	if _, err := fs.WriteFileAtomic("/jared/notes.md", []byte("ok"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("write inside own docset should succeed: %v", err)
	}
	if _, err := fs.WriteFileAtomic("/claw/notes.md", []byte("nope"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("write to another agent's docset should be read-only, got %v", err)
	}
	if _, err := fs.WriteFileAtomic("/root.md", []byte("nope"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("write to root should be read-only, got %v", err)
	}
	// Mkdir is gated the same way.
	if err := fs.Mkdir("/claw/sub"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("mkdir in another agent's docset should be read-only, got %v", err)
	}
	if err := fs.Mkdir("/jared/sub"); err != nil {
		t.Fatalf("mkdir in own docset should succeed: %v", err)
	}
}

// An identity with no writable roots (anonymous / unrecognized) gets a fully
// read-only view even though the substrate itself is writable.
func TestScopedWriteFS_NoRoots_FullyReadOnly(t *testing.T) {
	dir := t.TempDir()
	base := NewDirFS(dir, config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	if err := base.Mkdir("/jared"); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}

	fs := newScopedWriteFS(base, nil)
	if _, err := fs.WriteFileAtomic("/jared/x.md", []byte("nope"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("no-roots session must be read-only everywhere, got %v", err)
	}
}

// Reads always pass through, regardless of write scope.
func TestScopedWriteFS_ReadsPassThrough(t *testing.T) {
	dir := t.TempDir()
	base := NewDirFS(dir, config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	if err := base.Mkdir("/claw"); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}
	if _, err := base.WriteFileAtomic("/claw/readme.md", []byte("hi"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// Session scoped to /jared can still read /claw.
	fs := newScopedWriteFS(base, rootsAuthz("/jared"))
	data, err := fs.ReadFile("/claw/readme.md")
	if err != nil {
		t.Fatalf("read should pass through scope: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("read = %q, want %q", data, "hi")
	}
}

// Regression for OPE-42: `ls -l` and `stat` derive write bits from the caller's
// effective grant, including for config administrators whose session adds a
// configViewFS layer over the scoped filesystem.
func TestSessionShellModeBitsFollowGrant(t *testing.T) {
	for _, configAdmin := range []bool{false, true} {
		auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil), "ref": docset("/ref", nil)}, nil)
		ref := auth.Docsets["ref"]
		ref.Access = config.DocsetAccess{Allow: map[string]string{"writer": "ro"}}
		auth.Docsets["ref"] = ref
		if configAdmin {
			auth.Roles["writer"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}
		}
		server, root := bootRulesServer(t, auth, nil)
		for _, f := range []string{"docs/sub/a.md", "ref/sub/b.md"} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, f), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sh := server.buildSessionShell(testIdentity("alice"))
		for cmd, want := range map[string]string{
			"ls -ld /":            "dr-xr-xr-x",
			"ls -ld /docs":        "drwxr-xr-x",
			"ls -l /docs":         "drwxr-xr-x",
			"ls -l /docs/sub":     "-rw-r--r--",
			"stat /docs/sub/a.md": "Access: -rw-r--r--",
			"ls -ld /ref":         "dr-xr-xr-x",
			"ls -l /ref":          "dr-xr-xr-x",
			"ls -l /ref/sub":      "-r--r--r--",
			"stat /ref/sub/b.md":  "Access: -r--r--r--",
		} {
			var out, errOut bytes.Buffer
			if code := sh.ExecPipeline(cmd, &out, &errOut, nil); code != 0 || !strings.Contains(out.String(), want) {
				t.Errorf("configAdmin=%v %s: exit %d, stdout %q, stderr %q; want %q", configAdmin, cmd, code, out.String(), errOut.String(), want)
			}
		}
	}
}
