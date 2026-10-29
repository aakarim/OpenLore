package openlore

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Step 1 of Part C: `publish` no longer writes straight to disk — it commits
// through the session VFS, so it inherits per-identity write scoping (and, in
// later steps, CAS + approval gating). These tests pin that behavior: a publish
// to an in-scope docset commits and is readable back through the same FS, and a
// publish to a docset the session may *see* but not *write* is denied without
// touching disk.
func TestPublish_GoesThroughScopedVFS(t *testing.T) {
	dir := t.TempDir()
	base := NewDirFS(dir, config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	for _, d := range []string{"/jared", "/claw"} {
		if err := base.Mkdir(d); err != nil {
			t.Fatalf("seed mkdir %s: %v", d, err)
		}
	}

	// Session can see both docsets but may only write /jared.
	authz := func(_ vfs.ChangeAction, p string) error {
		clean := vfs.CleanPath(p)
		if clean == "/jared" || strings.HasPrefix(clean, "/jared/") {
			return nil
		}
		return fmt.Errorf("%w: outside /jared grant", vfs.ErrReadOnly)
	}
	fs := newScopedWriteFS(base, authz)
	sh := shell.NewShell(fs)
	sh.SetPublishTargets([]cmds.PublishTarget{
		{Name: "jared", InboxPath: "/jared"},
		{Name: "claw", InboxPath: "/claw"},
	})

	// In-scope publish commits and reads back through the VFS.
	if out, errs, code := run(sh, "echo hello | publish /jared/topic.md"); code != 0 {
		t.Fatalf("in-scope publish failed: code=%d out=%q err=%q", code, out, errs)
	}
	if out, _, _ := run(sh, "cat /jared/topic.md"); out != "hello\n" {
		t.Fatalf("published content = %q, want %q", out, "hello\n")
	}

	// Out-of-scope publish is denied by the scoped VFS, not committed.
	out, errs, code := run(sh, "echo nope | publish /claw/topic.md")
	if code == 0 {
		t.Fatalf("out-of-scope publish should fail; out=%q", out)
	}
	if !strings.Contains(errs, "no access") {
		t.Fatalf("want 'no access' error, got %q", errs)
	}
	if _, _, c := run(sh, "cat /claw/topic.md"); c == 0 {
		t.Fatalf("out-of-scope publish must not write /claw/topic.md")
	}
}

// TestPublish_RoutesIntoInbox pins that `publish /<docset>/<rest>` writes into
// the docset's configured inbox (InboxPath/<rest>), not the docset root — the
// only place a publish grant may create files.
func TestPublish_RoutesIntoInbox(t *testing.T) {
	dir := t.TempDir()
	base := NewDirFS(dir, config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	if err := base.Mkdir("/alfie"); err != nil {
		t.Fatalf("seed mkdir /alfie: %v", err)
	}
	if err := base.Mkdir("/alfie/inbox"); err != nil {
		t.Fatalf("seed mkdir /alfie/inbox: %v", err)
	}

	// Publish grant semantics: create/edit only inside /alfie/inbox, no deletes.
	authz := func(_ vfs.ChangeAction, p string) error {
		clean := vfs.CleanPath(p)
		if clean == "/alfie/inbox" || strings.HasPrefix(clean, "/alfie/inbox/") {
			return nil
		}
		return fmt.Errorf("%w: outside /alfie/inbox grant", vfs.ErrReadOnly)
	}
	fs := newScopedWriteFS(base, authz)
	sh := shell.NewShell(fs)
	sh.SetPublishTargets([]cmds.PublishTarget{{Name: "alfie", InboxPath: "/alfie/inbox"}})

	// publish /alfie/from-miles.md lands in /alfie/inbox/from-miles.md.
	if out, errs, code := run(sh, "echo hi | publish /alfie/from-miles.md"); code != 0 {
		t.Fatalf("publish into inbox failed: code=%d out=%q err=%q", code, out, errs)
	}
	if out, _, _ := run(sh, "cat /alfie/inbox/from-miles.md"); out != "hi\n" {
		t.Fatalf("published content = %q, want %q", out, "hi\n")
	}
	// It must NOT have written the docset root path.
	if _, _, c := run(sh, "cat /alfie/from-miles.md"); c == 0 {
		t.Fatalf("publish must not write to the docset root")
	}

	// Nested path is preserved under the inbox.
	if _, errs, code := run(sh, "echo x | publish /alfie/sub/deep.md"); code != 0 {
		t.Fatalf("nested publish failed: err=%q", errs)
	}
	if out, _, _ := run(sh, "cat /alfie/inbox/sub/deep.md"); out != "x\n" {
		t.Fatalf("nested published content = %q, want %q", out, "x\n")
	}

	// Extra arguments are rejected, not ignored: neither the
	// `publish <docset> <file>` form nor a path plus a stray argument writes.
	for _, tc := range []struct{ cmd, hint, file string }{
		{"echo x | publish alfie two.md", "e.g. publish /alfie/two.md", "/alfie/inbox/two.md"},
		{"echo x | publish /alfie/three.md extra.md", `got 2 arguments "/alfie/three.md" "extra.md"`, "/alfie/inbox/three.md"},
	} {
		out, errs, _ := run(sh, tc.cmd+`; echo "exit=$?"`)
		if out != "exit=1\n" || !strings.Contains(errs, tc.hint) {
			t.Fatalf("%s: out=%q err=%q, want exit=1 and %q", tc.cmd, out, errs, tc.hint)
		}
		if _, _, c := run(sh, "cat "+tc.file); c == 0 {
			t.Fatalf("%s must not write %s", tc.cmd, tc.file)
		}
	}
}
