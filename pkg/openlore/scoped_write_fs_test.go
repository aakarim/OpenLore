package openlore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
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

func TestScopedWriteFSMoveFailureNamesCause(t *testing.T) {
	for _, tc := range []struct {
		name     string
		roots    []string
		readOnly bool
		want     string
	}{
		{name: "unsupported atomic move", roots: []string{"/"}, want: "does not support atomic moves"},
		{name: "destination outside scope", roots: []string{"/source"}, want: "write scope denies write on /dest/b.md"},
		{name: "source outside scope", roots: []string{"/dest"}, want: "write scope denies remove_all on /source/a.md"},
		{name: "read-only backend", roots: []string{"/"}, readOnly: true, want: "read-only filesystem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := newWritableDirFS(t)
			for _, dir := range []string{"/source", "/dest"} {
				if err := base.Mkdir(dir); err != nil {
					t.Fatal(err)
				}
			}
			var backing vfs.FileSystem = base
			if tc.readOnly {
				backing = struct{ vfs.FileSystem }{base}
			}
			sh := shell.NewShell(newScopedWriteFS(backing, rootsAuthz(tc.roots...)))
			if !tc.readOnly {
				// The same session can write and delete within its grant; lacking
				// atomic move support must not be mistaken for a read-only folder.
				probe := tc.roots[0] + "/probe.md"
				if code := sh.ExecPipeline("echo probe > "+probe+" && rm "+probe, io.Discard, io.Discard, nil); code != 0 {
					t.Fatalf("write/delete probe exit = %d", code)
				}
			}
			for path, body := range map[string]string{"/source/a.md": "source\n", "/dest/b.md": "destination\n"} {
				if _, err := base.WriteFileAtomic(path, []byte(body), vfs.WriteOpts{}); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			code := sh.ExecPipeline("mv /source/a.md /dest/b.md && echo continued", &out, &errOut, nil)
			if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("move: exit=%d stdout=%q stderr=%q; want %q", code, out.String(), errOut.String(), tc.want)
			}
			if !tc.readOnly && strings.Contains(errOut.String(), "read-only filesystem") {
				t.Fatalf("writable backend misreported as read-only: %s", errOut.String())
			}
			for path, want := range map[string]string{"/source/a.md": "source\n", "/dest/b.md": "destination\n"} {
				if got, err := base.ReadFile(path); err != nil || string(got) != want {
					t.Fatalf("failed move changed %s: got %q, err %v", path, got, err)
				}
			}
		})
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
		server.config.ConfigFile = filepath.Join(t.TempDir(), "openlore.yml")
		if err := os.WriteFile(server.config.ConfigFile, []byte("http_port: 8080\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sh := server.buildSessionShell(testIdentity("alice"))
		cases := map[string]string{
			"ls -ld /":            "dr-xr-xr-x",
			"ls -ld /docs":        "drwxr-xr-x",
			"ls -l /docs":         "drwxr-xr-x",
			"ls -l /docs/sub":     "-rw-r--r--",
			"stat /docs/sub/a.md": "Access: -rw-r--r--",
			"ls -ld /ref":         "dr-xr-xr-x",
			"ls -l /ref":          "dr-xr-xr-x",
			"ls -l /ref/sub":      "-r--r--r--",
			"stat /ref/sub/b.md":  "Access: -r--r--r--",
		}
		if configAdmin {
			// The synthetic /opt tree: only the live lore.json is writable;
			// openlore.yml is mounted read-only.
			cases["stat /opt/openlore/lore.json"] = "Access: -rw-r--r--"
			cases["stat /opt/openlore/openlore.yml"] = "Access: -r--r--r--"
			cases["cat /opt/openlore/openlore.yml"] = "http_port: 8080"
			cases["ls -l /opt/openlore"] = "-rw-r--r--"
			cases["ls -ld /opt"] = "dr-xr-xr-x"
			cases["ls -ld /opt/openlore"] = "dr-xr-xr-x"
		} else if code := sh.ExecPipeline("ls -ld /opt/openlore", io.Discard, io.Discard, nil); code == 0 {
			t.Error("non-admin session can see /opt/openlore")
		}
		for cmd, want := range cases {
			var out, errOut bytes.Buffer
			if code := sh.ExecPipeline(cmd, &out, &errOut, nil); code != 0 || !strings.Contains(out.String(), want) {
				t.Errorf("configAdmin=%v %s: exit %d, stdout %q, stderr %q; want %q", configAdmin, cmd, code, out.String(), errOut.String(), want)
			}
		}
	}
}
