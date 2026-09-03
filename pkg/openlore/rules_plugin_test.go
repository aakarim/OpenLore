package openlore

import (
	"bytes"
	"context"
	"io"
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

func bootRulesServer(t *testing.T, auth *config.AuthConfig, logger *slog.Logger) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	for _, docset := range auth.Docsets {
		for _, mapping := range docset.Paths {
			if err := os.MkdirAll(filepath.Join(root, strings.TrimPrefix(mapping.Source, "/")), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	authFile := filepath.Join(t.TempDir(), "lore.json")
	writeAuthFixture(t, authFile, auth)
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server, err := NewServer(root, WithReadonly(false), config.WithDataDir(t.TempDir()), config.WithAuthFile(authFile), WithLogger(logger))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return server, root
}

func serverAuth(docsets map[string]config.DocsetSpec, global map[string]rules.RuleSpec) *config.AuthConfig {
	for name, docset := range docsets {
		docset.Access = config.DocsetAccess{Allow: map[string]string{"writer": "rw"}}
		docsets[name] = docset
	}
	return &config.AuthConfig{Rules: global, Roles: map[string]config.RoleSpec{"writer": {}}, Docsets: docsets, Identities: []config.AuthIdentity{{Name: "alice", Roles: []string{"writer"}}}}
}

func admitServer(server *Server, target, content string) error {
	_, err := server.writeChain()(context.Background(), NewWriteOp(Attribution{Principal: "alice"}, writeCSBytes(target, content)))
	return err
}

func TestRulesServerBootAdmissionAndValidateOutput(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil), "public": docset("/public", nil)}, map[string]rules.RuleSpec{
		"doc-size": {Match: []string{"**/*.md"}, Use: "size/lines", With: map[string]any{"max": 3}},
	})
	server, root := bootRulesServer(t, auth, nil)
	if len(server.validators) != 1 {
		t.Fatalf("validators=%d, want 1", len(server.validators))
	}
	if err := admitServer(server, "/docs/ok.md", "1\n2\n3\n"); err != nil {
		t.Fatalf("three-line write: %v", err)
	}
	if err := admitServer(server, "/public/too-long.md", "1\n2\n3\n4\n"); err == nil || !strings.Contains(err.Error(), "rules: /public/too-long.md: size/lines (doc-size @ lore.json)") {
		t.Fatalf("four-line write error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "too-long.md"), []byte("1\n2\n3\n4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := server.buildSessionShell(Identity{IdentityName: "alice"})
	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline("lore validate /docs", &out, &errOut, nil); code != 1 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"too-long.md:1:1: error [size/lines] 4 lines exceeds the limit of 3 (max: 3)", "see: lore package doc size/lines", "1 error, 0 warnings"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "suggested:") || strings.Contains(out.String(), "override:") {
		t.Fatalf("validate leaked write guidance:\n%s", out.String())
	}
}

func TestRulesServerBootLegacyOKFMessage(t *testing.T) {
	server, _ := bootRulesServer(t, serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", &config.OKFDocsetConfig{})}, nil), nil)
	err := admitServer(server, "/docs/bad.md", invalidDoc)
	want := "okf: /docs/bad.md: missing YAML frontmatter block (a concept must open with a '---' delimited block)\n  see: lore package doc okf"
	if err == nil || err.Error() != want {
		t.Fatalf("error=%q, want %q", err, want)
	}
}

func TestRulesServerPerDocsetAndNonEnforcing(t *testing.T) {
	var logs bytes.Buffer
	warn := false
	backend := docset("/backend", nil)
	backend.Rules = map[string]rules.RuleSpec{
		"enforced": {Use: "size/lines", Match: []string{"**/*.md"}, With: map[string]any{"max": 2}},
		"warning":  {Use: "size/lines", Match: []string{"**/*.md"}, With: map[string]any{"max": 1}, Enforce: &warn},
	}
	server, _ := bootRulesServer(t, serverAuth(map[string]config.DocsetSpec{"backend": backend, "public": docset("/public", nil)}, nil), slog.New(slog.NewTextHandler(&logs, nil)))
	if err := admitServer(server, "/public/a.md", "1\n2\n3\n"); err != nil {
		t.Fatalf("rule escaped docset: %v", err)
	}
	if err := admitServer(server, "/backend/a.md", "1\n2\n3\n"); err == nil || !strings.Contains(err.Error(), "size/lines (enforced @ lore.json#docsets.backend)") {
		t.Fatalf("docset rule did not reject: %v", err)
	}
	if err := admitServer(server, "/backend/warn.md", "1\n2\n"); err != nil {
		t.Fatalf("non-enforcing rule rejected: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "rule=warning") {
		t.Fatalf("missing non-enforcing warning: %s", got)
	}
}

func TestRulesServerBundleAnchor(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{
		"docs": docset("/docs", &config.OKFDocsetConfig{}),
		"wiki": docset("/wiki", &config.OKFDocsetConfig{}),
	}, nil)
	server, root := bootRulesServer(t, auth, nil)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"docs", "wiki"} {
		if err := os.WriteFile(filepath.Join(root, dir, "a.md"), []byte("---\ntype: Note\n---\n[missing](b.md)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sh := server.buildSessionShell(Identity{IdentityName: "alice"})
	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline("lore validate /", &out, &errOut, nil); code != 1 {
		t.Fatalf("root exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if want := "lore validate: / is above docsets docs, wiki with bundle rules; run lore validate per docset"; !strings.Contains(errOut.String(), want) {
		t.Fatalf("stderr missing %q: %s", want, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := sh.ExecPipeline("lore validate /docs", &out, &errOut, nil); code != 1 || !strings.Contains(out.String(), "openlore/broken-link") {
		t.Fatalf("docs exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}
