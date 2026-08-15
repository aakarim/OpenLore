package openlore

import (
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

// Part B — capability gating on the shell. A read-only session must not be able
// to discover or use the write/publish surface; a writable session must.

func TestCapability_ReadOnlySession_RedirectHidden(t *testing.T) {
	sh, _, _ := newWritableShell(t) // substrate is writable…
	sh.SetAllowedActions(nil)       // …but the session is read-only (ActionRead only)

	out, errs, code := run(sh, "echo nope > /a.md")
	if code == 0 {
		t.Fatalf("read-only session should reject redirect; out=%q", out)
	}
	if !strings.Contains(errs, "read-only") {
		t.Fatalf("want read-only error, got %q", errs)
	}
	// And nothing was written.
	if _, _, c := run(sh, "cat /a.md"); c == 0 {
		t.Fatalf("file should not exist after blocked redirect")
	}
}

func TestCapability_ReadOnlySession_WriteCmdsUnknown(t *testing.T) {
	sh, _, _ := newWritableShell(t)
	sh.SetAllowedActions(nil) // read-only

	for _, c := range []string{
		"write /a.md",
		"patch /a.md",
		"tee /a.md",
		"mkdir /dir",
		"mv /a.md /b.md",
		"rm /a.md",
		"publish ds /a.md",
	} {
		_, errs, code := run(sh, c)
		if code != 127 || !strings.Contains(errs, "command not found") {
			t.Fatalf("%q: want 127/command not found, got code=%d err=%q", c, code, errs)
		}
	}

	// A plain read command still works.
	if _, _, code := run(sh, "echo hi"); code != 0 {
		t.Fatalf("read command should still work in a read-only session")
	}
}

func TestCapability_ReadOnlySession_SedInPlaceHidden(t *testing.T) {
	sh, _, _ := newWritableShell(t)
	// Seed a file while writable, then drop to read-only.
	if _, _, c := run(sh, "echo hello > /a.md"); c != 0 {
		t.Fatal("seed write failed")
	}
	sh.SetAllowedActions(nil)

	// Plain sed (read/stream) still works.
	if out, _, code := run(sh, "sed 's/hello/world/' /a.md"); code != 0 || out != "world\n" {
		t.Fatalf("plain sed should work read-only: out=%q code=%d", out, code)
	}
	// sed -i (in-place edit) is a write → hidden.
	_, errs, code := run(sh, "sed -i 's/hello/world/' /a.md")
	if code != 127 || !strings.Contains(errs, "command not found") {
		t.Fatalf("sed -i should be hidden in read-only session, got code=%d err=%q", code, errs)
	}
}

func TestCapability_WritableSession_AllowsWrites(t *testing.T) {
	sh, _, _ := newWritableShell(t)
	sh.SetAllowedActions([]cmds.Action{cmds.ActionWrite, cmds.ActionPublish})

	if _, errs, code := run(sh, "echo hi > /a.md"); code != 0 {
		t.Fatalf("writable session redirect failed: code=%d err=%q", code, errs)
	}
	// tee and patch (write-class commands) must be discoverable (not 127).
	if _, _, code := run(sh, "echo x | tee /b.md"); code == 127 {
		t.Fatalf("tee should be available in a writable session")
	}
	if out, _, _ := run(sh, "cat /b.md"); out != "x\n" {
		t.Fatalf("tee should have written /b.md, got %q", out)
	}
}

func TestCapability_Help_HonorsScopedSet(t *testing.T) {
	sh, _, _ := newWritableShell(t)

	// Read-only: help omits WRITES and PUBLISHING.
	sh.SetAllowedActions(nil)
	out, _, _ := run(sh, "help")
	if strings.Contains(out, "WRITES") || strings.Contains(out, "PUBLISHING") {
		t.Fatalf("read-only help must omit WRITES/PUBLISHING:\n%s", out)
	}

	// Writable+publish: help shows both.
	sh.SetAllowedActions([]cmds.Action{cmds.ActionWrite, cmds.ActionPublish})
	out, _, _ = run(sh, "help")
	if !strings.Contains(out, "WRITES") || !strings.Contains(out, "PUBLISHING") {
		t.Fatalf("writable help must show WRITES and PUBLISHING:\n%s", out)
	}
}

func TestCapability_Unrestricted_DefaultAllowsEverything(t *testing.T) {
	sh, _, _ := newWritableShell(t) // never call SetAllowedActions → unrestricted
	if _, errs, code := run(sh, "echo hi > /a.md"); code != 0 {
		t.Fatalf("unrestricted shell should allow writes: code=%d err=%q", code, errs)
	}
}
