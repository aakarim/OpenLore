package cmds_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

// runLore executes a command line against a shell seeded with the given docsets.
func runLore(t *testing.T, docsets []cmds.DocsetInfo, cmd string) (string, string, int) {
	t.Helper()
	sh := shell.NewShell(testFS())
	sh.SetDocsets(docsets)
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline(cmd, &out, &errOut, nil)
	return out.String(), errOut.String(), code
}

func TestLore_BareShowsUsageExitsZero(t *testing.T) {
	out, _, code := runLore(t, nil, "lore")
	if code != 0 {
		t.Fatalf("bare lore exit = %d, want 0", code)
	}
	if !strings.Contains(out, "Usage: lore <command>") || !strings.Contains(out, "docsets") {
		t.Fatalf("bare lore output missing usage/subcommand:\n%s", out)
	}
}

func TestLore_UnknownSubcommandExitsOne(t *testing.T) {
	out, errOut, code := runLore(t, nil, "lore bogus")
	if code != 1 {
		t.Fatalf("unknown subcommand exit = %d, want 1", code)
	}
	if out != "" {
		t.Fatalf("unknown subcommand should not write stdout, got %q", out)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("stderr missing error:\n%s", errOut)
	}
}

func TestLoreDocsets_Table(t *testing.T) {
	docsets := []cmds.DocsetInfo{
		{Name: "public", Paths: []string{"/docs/public"}, Grant: "ro"},
		{Name: "public", Paths: []string{"/public"}, AliasTarget: "/docs/public", Grant: "ro"},
		{Name: "backend", Paths: []string{"/docs/backend", "/docs/api"}, Grant: "rw", Writable: true, AgentSkills: true},
		{Name: "home", Paths: []string{"/home/backend"}, Grant: "rw", Writable: true, Home: true, Inbox: true},
	}
	out, _, code := runLore(t, docsets, "lore docsets")
	if code != 0 {
		t.Fatalf("lore docsets exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("want header + 4 rows, got %d lines:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "DOCSET") {
		t.Fatalf("first line should be header, got %q", lines[0])
	}
	// Fields are grep/field-splittable.
	assertRow := func(line, name, access, attrs, mount, target string) {
		t.Helper()
		f := strings.Fields(line)
		if len(f) != 5 || f[0] != name || f[1] != access || f[2] != attrs || f[3] != mount || f[4] != target {
			t.Fatalf("row %q: got %v; want %q %q %q %q %q", line, f, name, access, attrs, mount, target)
		}
	}
	assertRow(lines[1], "public", "ro", "-", "/docs/public", "-")
	assertRow(lines[2], "public", "ro", "alias", "/public", "/docs/public")
	assertRow(lines[3], "backend", "rw", "agent-skills", "/docs/backend,/docs/api", "-")
	assertRow(lines[4], "home", "rw", "home,inbox", "/home/backend", "-")
}

func TestLoreDocsets_EmptyShowsHeaderOnly(t *testing.T) {
	out, _, code := runLore(t, nil, "lore docsets")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.TrimRight(out, "\n") != "DOCSET  GRANTS  ATTRIBUTES  PATH  TARGET" {
		t.Fatalf("empty docsets should print header only, got:\n%q", out)
	}
}

func TestLoreDocsets_GrepByAttribute(t *testing.T) {
	docsets := []cmds.DocsetInfo{
		{Name: "public", Paths: []string{"/docs"}, Grant: "ro"},
		{Name: "backend", Paths: []string{"/docs/backend"}, Grant: "rw", Writable: true, Inbox: true},
	}
	out, _, code := runLore(t, docsets, "lore docsets | grep inbox")
	if code != 0 {
		t.Fatalf("piped grep exit = %d, want 0", code)
	}
	if !strings.Contains(out, "backend") || strings.Contains(out, "public ") {
		t.Fatalf("grep inbox should return only the backend row, got:\n%s", out)
	}
}

func TestLoreValidate_RequiresEnabledValidator(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("lore validate /docs", &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out.String() != "" || !strings.Contains(errOut.String(), "no validators enabled") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestLoreValidate_RunsEnabledValidator(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetValidators([]validation.Validator{func(validation.Bundle) []validation.Diagnostic {
		return nil
	}})
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("lore validate /docs", &out, &errOut, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errOut.String())
	}
	if out.String() != "0 errors, 0 warnings\n" {
		t.Fatalf("stdout=%q", out.String())
	}
}
