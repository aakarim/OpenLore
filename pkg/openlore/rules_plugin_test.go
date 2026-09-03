package openlore

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func newTestRulesPlugin(t *testing.T, auth *config.AuthConfig, logger *slog.Logger) *rulesPlugin {
	t.Helper()
	p, err := newRulesPlugin(auth, rules.Defaults{Growth: 1.25}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runRules(p *rulesPlugin, changes ...vfs.Change) (bool, error) {
	called := false
	handler := p.WriteMiddleware()[0](func(context.Context, WriteOp) (WriteResult, error) { called = true; return WriteResult{}, nil })
	_, err := handler(context.Background(), NewWriteOp(Attribution{Principal: "test"}, vfs.ChangeSet{Changes: changes}))
	return called, err
}

func rulesAuth(global map[string]rules.RuleSpec, docsetRules map[string]rules.RuleSpec) *config.AuthConfig {
	return &config.AuthConfig{Rules: global, Docsets: map[string]config.DocsetSpec{
		"backend": {Paths: []config.PathMapping{{Source: "/backend", Display: "/backend"}}, Rules: docsetRules},
		"public":  {Paths: []config.PathMapping{{Source: "/public", Display: "/public"}}},
	}}
}

func TestRulesPluginSizeScopeMessageAndBatch(t *testing.T) {
	p := newTestRulesPlugin(t, rulesAuth(map[string]rules.RuleSpec{"doc-size": {Match: []string{"**/*.md"}, Use: "size/lines", With: map[string]any{"max": 3}}}, nil), nil)
	called, err := runRules(p,
		vfs.Change{Target: "/backend/good.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("1\n2\n3\n")}},
		vfs.Change{Target: "/public/bad.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("1\n2\n3\n4\n")}},
	)
	if called || err == nil {
		t.Fatalf("batch called=%v err=%v", called, err)
	}
	message := err.Error()
	for _, want := range []string{"rules: /public/bad.md: size/lines (doc-size @ lore.json)", "4 lines exceeds the limit of 3 (max: 3)", "cannot grow past 3 lines", "bad-details.md", "add a link to it from bad.md", "override: to raise the limit, edit doc-size in lore.json", "see: lore package doc size/lines"} {
		if !strings.Contains(message, want) {
			t.Errorf("message missing %q:\n%s", want, message)
		}
	}
}

func TestRulesPluginPerDocsetAndNonEnforcing(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	warn := false
	p := newTestRulesPlugin(t, rulesAuth(nil, map[string]rules.RuleSpec{"short": {Match: []string{"**/*.md"}, Use: "size/lines", With: map[string]any{"max": 1}, Enforce: &warn}}), logger)
	called, err := runRules(p,
		vfs.Change{Target: "/public/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("1\n2\n")}},
		vfs.Change{Target: "/backend/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("1\n2\n")}},
	)
	if err != nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
	if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "rule=short") {
		t.Fatalf("missing warning log: %s", got)
	}
}

func TestRulesPluginBundleRulesValidateOnly(t *testing.T) {
	auth := rulesAuth(nil, map[string]rules.RuleSpec{
		"format": {Match: []string{"**/*.md"}, Use: "okf"},
		"links":  {Match: []string{"**/*.md"}, Use: "link/resolves"},
	})
	p := newTestRulesPlugin(t, auth, nil)
	called, err := runRules(p, vfs.Change{Target: "/backend/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("---\ntype: Note\n---\n[missing](b.md)\n")}})
	if err != nil || !called {
		t.Fatalf("bundle rule affected write: called=%v err=%v", called, err)
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend", "a.md"), []byte("---\ntype: Note\n---\n[missing](b.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := NewDirFS(dir, config.FilesConfig{})
	diagnostics, err := validation.Scan(fsys, "/backend", p.Validators()...)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Rule != "openlore/broken-link" {
		t.Fatalf("diagnostics=%#v", diagnostics)
	}
}
