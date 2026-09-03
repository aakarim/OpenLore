package openlore

import (
	"bytes"
	"context"
	"errors"
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
	p, err := newRulesPlugin(auth, rules.Defaults{Growth: 1.25}, nil, logger)
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

func TestInitialSizeBaselinePersistsAndRejectsGrowth(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil)}, map[string]rules.RuleSpec{
		"length": {Match: []string{"**/*.md"}, Use: "size/lines", With: map[string]any{"max": "initial", "growth": 1.5}},
	})
	server, root := bootRulesServer(t, auth, nil)
	id := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Attribution: Attribution{Principal: "alice"}, Scopes: []string{ScopeFull}}
	write := func(lines int) error {
		_, err := server.writeLog.SubmitIdentity(context.Background(), id, writeCSBytes("/docs/a.md", strings.Repeat("x\n", lines)))
		return err
	}
	if err := write(10); err != nil {
		t.Fatal(err)
	}
	if err := write(15); err != nil {
		t.Fatal(err)
	}
	if err := write(16); err == nil || !strings.Contains(err.Error(), "16 lines exceeds the limit of 15 (baseline 10 lines × growth 1.5") || !strings.Contains(err.Error(), "lore size baseline reset /docs/a.md") {
		t.Fatalf("rejection=%v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "docs", ".lore", "size", "a.md.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"op":"baseline"`) != 1 || !strings.Contains(string(raw), `"reason":"create"`) || !strings.Contains(string(raw), `"lines":10`) || !strings.Contains(string(raw), `"actor":"alice"`) {
		t.Fatalf("state=%s", raw)
	}
}

func TestFolderRulesPermissionInheritanceInvalidationAndExemption(t *testing.T) {
	docs := docset("/docs", nil)
	docs.Access = config.DocsetAccess{Allow: map[string]string{"writer": "rw", "editor": "ro"}}
	docs.Config = &config.DirConfigPermission{Edit: []string{"editor"}}
	public := docset("/public", nil)
	public.Access = config.DocsetAccess{Allow: map[string]string{"writer": "rw", "editor": "ro"}}
	auth := &config.AuthConfig{
		Roles:   map[string]config.RoleSpec{"writer": {}, "editor": {}},
		Docsets: map[string]config.DocsetSpec{"docs": docs, "public": public},
		Identities: []config.AuthIdentity{
			{Name: "alice", Roles: []string{"writer", "editor"}},
			{Name: "bob", Roles: []string{"writer"}},
			{Name: "carol", Roles: []string{"editor"}},
		},
	}
	server, root := bootRulesServer(t, auth, nil)
	if err := os.MkdirAll(filepath.Join(root, "docs", "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	identity := func(name string) Identity {
		return Identity{IdentityName: name, Principal: AuthenticatedPrincipal{IdentityName: name}, Attribution: Attribution{Principal: name}, Scopes: []string{ScopeFull}}
	}
	configPath := "/docs/.lore/config.yaml"
	content := "version: 1\nrules:\n  short:\n    match: ['**/*.md']\n    use: size/lines\n    with: {max: 3}\n" + strings.Repeat("# config is exempt from its own rule\n", 500)
	if _, err := server.buildSessionFS(identity("bob")).(vfs.WritableFS).WriteFileAtomic(configPath, []byte(content), vfs.WriteOpts{}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("writer without config.edit = %v", err)
	}
	if _, err := server.buildSessionFS(identity("carol")).(vfs.WritableFS).WriteFileAtomic(configPath, []byte(content), vfs.WriteOpts{}); err == nil {
		t.Fatal("read-only config editor wrote config")
	}
	if _, err := server.buildSessionFS(identity("alice")).(vfs.WritableFS).WriteFileAtomic(configPath, []byte(content), vfs.WriteOpts{}); err != nil {
		t.Fatalf("config editor write: %v", err)
	}
	if err := server.buildSessionFS(identity("bob")).(vfs.WritableFS).Remove(configPath); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("config remove without config.edit = %v", err)
	}
	if _, err := server.AdmitChangeSet(context.Background(), identity("bob"), vfs.ChangeSet{Target: configPath, Action: vfs.ChangeActionRemove}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("AdmitChangeSet config remove without config.edit = %v", err)
	}
	var readOut, readErr bytes.Buffer
	readerShell := server.buildSessionShell(identity("carol"))
	if code := readerShell.ExecPipeline("cat "+configPath+"; tree /docs", &readOut, &readErr, nil); code != 0 || !strings.Contains(readOut.String(), "use: size/lines") || !strings.Contains(readOut.String(), ".lore") || !strings.Contains(readOut.String(), "config.yaml") {
		t.Fatalf("reader config view exit=%d stdout=%s stderr=%s", code, readOut.String(), readErr.String())
	}
	readOut.Reset()
	readErr.Reset()
	if code := readerShell.ExecPipeline("lore validate /docs", &readOut, &readErr, nil); code != 0 || !strings.Contains(readOut.String(), "0 errors, 0 warnings") {
		t.Fatalf("config exemption validate exit=%d stdout=%s stderr=%s", code, readOut.String(), readErr.String())
	}

	deep := server.buildSessionFS(identity("alice")).(vfs.WritableFS)
	if _, err := deep.WriteFileAtomic("/docs/sub/deep/x.md", []byte("1\n2\n3\n4\n"), vfs.WriteOpts{}); err == nil || !strings.Contains(err.Error(), "short @ /docs/.lore/config.yaml") {
		t.Fatalf("inherited rule error = %v", err)
	}
	if _, err := deep.WriteFileAtomic("/public/x.md", []byte("1\n2\n3\n4\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("folder rule escaped docset: %v", err)
	}
	if err := server.buildSessionFS(identity("alice")).(vfs.WritableFS).Remove(configPath); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	if _, err := server.buildSessionFS(identity("alice")).(vfs.WritableFS).WriteFileAtomic("/docs/sub/deep/y.md", []byte("1\n2\n3\n4\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("rule remained after config removal: %v", err)
	}
	mixed := vfs.ChangeSet{Changes: []vfs.Change{
		{Target: configPath, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("version: 1\nrules: {}\n")}},
		{Target: "/docs/mixed.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("content\n")}},
	}}
	if _, err := server.AdmitChangeSet(context.Background(), identity("alice"), mixed); err == nil || !strings.Contains(err.Error(), "folder config mutations must be submitted separately") {
		t.Fatalf("mixed config batch error = %v", err)
	}

	for _, name := range []string{"configured", "plain"} {
		if err := os.Mkdir(filepath.Join(root, "docs", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configuredPath := "/docs/configured/.lore/config.yaml"
	if _, err := server.buildSessionFS(identity("alice")).(vfs.WritableFS).WriteFileAtomic(configuredPath, []byte("version: 1\nrules: {}\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("nested config write: %v", err)
	}
	if _, err := server.writeLog.SubmitIdentity(context.Background(), identity("bob"), vfs.ChangeSet{Target: "/docs/configured", Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{}}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("serialized RemoveAll recheck without config.edit = %v", err)
	}
	if err := server.buildSessionFS(identity("bob")).(vfs.WritableFS).RemoveAll("/docs/configured", vfs.RemoveOpts{}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("RemoveAll config tree without config.edit = %v", err)
	}
	if err := server.buildSessionFS(identity("bob")).(vfs.WritableFS).RemoveAll("/docs/plain", vfs.RemoveOpts{}); err != nil {
		t.Fatalf("RemoveAll plain tree with rw = %v", err)
	}
	if err := server.buildSessionFS(identity("alice")).(vfs.WritableFS).RemoveAll("/docs/configured", vfs.RemoveOpts{}); err != nil {
		t.Fatalf("RemoveAll config tree with config.edit = %v", err)
	}
}

func TestFolderRulesValidateThroughAlias(t *testing.T) {
	docs := docset("/docs", nil)
	docs.Aliases = []string{"/alias"}
	docs.Config = &config.DirConfigPermission{Edit: []string{"writer"}}
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docs}, nil)
	server, root := bootRulesServer(t, auth, nil)
	id := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Attribution: Attribution{Principal: "alice"}, Scopes: []string{ScopeFull}}

	var out, errOut bytes.Buffer
	if code := server.buildSessionShell(id).ExecPipeline("lore validate /alias", &out, &errOut, nil); code != 0 {
		t.Fatalf("initial alias validate exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	configContent := "version: 1\nrules:\n  short:\n    match: ['**/*.md']\n    use: size/lines\n    with: {max: 3}\n"
	if _, err := server.buildSessionFS(id).(vfs.WritableFS).WriteFileAtomic("/alias/.lore/config.yaml", []byte(configContent), vfs.WriteOpts{}); err != nil {
		t.Fatalf("alias config write: %v", err)
	}
	if _, err := server.writeLog.SubmitIdentity(context.Background(), id, writeCSBytes("/docs/preapply.md", "1\n2\n3\n4\n")); err == nil || !strings.Contains(err.Error(), "size/lines") {
		t.Fatalf("serialized rule recheck error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "too-long.md"), []byte("1\n2\n3\n4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := server.buildSessionShell(id).ExecPipeline("lore validate /alias", &out, &errOut, nil); code != 1 || !strings.Contains(out.String(), "too-long.md:1:1: error [size/lines]") {
		t.Fatalf("alias validate exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

func TestFolderRulesUnificationAndDefaultOverride(t *testing.T) {
	newAuth := func(isDefault bool) *config.AuthConfig {
		docs := docset("/docs", nil)
		docs.Config = &config.DirConfigPermission{Edit: []string{"writer"}}
		return serverAuth(map[string]config.DocsetSpec{"docs": docs}, map[string]rules.RuleSpec{
			"limit": {Match: []string{"**/*.md"}, Use: "size/lines", With: map[string]any{"max": 10}, Default: isDefault},
		})
	}
	content := "version: 1\nrules:\n  limit:\n    match: ['**/*.md']\n    use: size/lines\n    with: {max: 3}\n"
	identity := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Attribution: Attribution{Principal: "alice"}, Scopes: []string{ScopeFull}}

	server, _ := bootRulesServer(t, newAuth(false), nil)
	_, err := server.buildSessionFS(identity).(vfs.WritableFS).WriteFileAtomic("/docs/.lore/config.yaml", []byte(content), vfs.WriteOpts{})
	if err == nil || !strings.Contains(err.Error(), "rules.limit: conflicts with limit @ lore.json (max: 10 vs 3); use a new rule name to tighten") {
		t.Fatalf("conflict error = %v", err)
	}

	server, _ = bootRulesServer(t, newAuth(true), nil)
	fsys := server.buildSessionFS(identity).(vfs.WritableFS)
	if _, err := fsys.WriteFileAtomic("/docs/.lore/config.yaml", []byte(content), vfs.WriteOpts{}); err != nil {
		t.Fatalf("default override config: %v", err)
	}
	if _, err := server.buildSessionFS(identity).(vfs.WritableFS).WriteFileAtomic("/docs/too-long.md", []byte("1\n2\n3\n4\n"), vfs.WriteOpts{}); err == nil || !strings.Contains(err.Error(), "limit @ /docs/.lore/config.yaml") {
		t.Fatalf("default override did not govern: %v", err)
	}
}

func TestFolderRulesRejectInvalidConfigAndValidateHostConfig(t *testing.T) {
	docs := docset("/docs", nil)
	docs.Config = &config.DirConfigPermission{Edit: []string{"writer"}}
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docs}, nil)
	server, root := bootRulesServer(t, auth, nil)
	id := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Attribution: Attribution{Principal: "alice"}, Scopes: []string{ScopeFull}}
	fsys := server.buildSessionFS(id).(vfs.WritableFS)
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"version: 1\nhooks: {x: {}}\n", "hooks: not supported yet"},
		{"version: 1\nunknown: true\n", "field unknown not found"},
	} {
		if _, err := fsys.WriteFileAtomic("/docs/.lore/config.yaml", []byte(tc.content), vfs.WriteOpts{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("invalid config error = %v, want %q", err, tc.want)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs", ".lore"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", ".lore", "config.yaml"), []byte("version: 1\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := server.buildSessionShell(id).ExecPipeline("lore validate /docs", &out, &errOut, nil); code != 1 || !strings.Contains(out.String(), ".lore/config.yaml:1:1: error [rules/config]") || !strings.Contains(out.String(), "field unknown not found") {
		t.Fatalf("validate exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
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
