package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/passkeys"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type disappearingDashboardFS struct {
	vfs.FileSystem
	vanished   string
	listedSize int64
}

func (f disappearingDashboardFS) ReadFile(target string) ([]byte, error) {
	if vfs.CleanPath(target) == f.vanished {
		return nil, fs.ErrNotExist
	}
	return f.FileSystem.ReadFile(target)
}

func (f disappearingDashboardFS) ReadDir(target string) ([]vfs.FileInfo, error) {
	entries, err := f.FileSystem.ReadDir(target)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if vfs.CleanPath(target+"/"+entries[i].Name()) == f.vanished && f.listedSize > 0 {
			entries[i].FileSize = f.listedSize
		}
	}
	return entries, nil
}

func newDashboardTestServer(t *testing.T) (*Server, *http.ServeMux, string) {
	t.Helper()
	s := newTokenTestServer(t, true, "deny")
	s.config.Readonly = true
	s.auth.Roles["reader"] = config.RoleSpec{}
	s.auth.Identities = append(s.auth.Identities, config.AuthIdentity{Name: "reader", Roles: []string{"reader"}})
	ds := s.auth.Docsets["public"]
	ds.Access.Allow["reader"] = "ro"
	ds.Aliases = []string{"/docs"}
	s.auth.Docsets["public"] = ds
	s.auth.Docsets["private-child"] = config.DocsetSpec{
		Paths:  []config.PathMapping{{Source: "/public/private", Display: "/public/private"}},
		Access: config.DocsetAccess{Allow: map[string]string{"alice": "ro"}},
	}
	s.merge.Mount("public", NewFSAdapter(fstest.MapFS{
		"read me.md":        {Data: []byte("# Welcome\n\n[Other](other.md)\n\n| A | B |\n| - | - |\n| 1 | 2 |\n")},
		"other.md":          {Data: []byte("é🙂\nxyz\n")},
		"private/secret.md": {Data: []byte("SENSITIVE_NESTED_CONTENT")},
		"payload.html":      {Data: []byte("<script>alert('xss')</script>")},
	}))
	mux := http.NewServeMux()
	s.dashboardRoutes(nil)(mux)
	return s, mux, mint(t, s, "reader", ScopeRead)
}

func dashboardRequest(h http.Handler, method, endpoint, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, endpoint, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDashboardAuthenticationAndReadOnlyMethods(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	for _, endpoint := range []string{"session", "tree", "context", "file", "raw", "history", "access", "usage"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, supplied := range []string{"", "invalid"} {
				w := dashboardRequest(mux, "GET", "/dashboard/api/"+endpoint+"?path=/public/other.md", supplied)
				if w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("unauthenticated response: %d %s", w.Code, w.Body.String())
				}
			}
			for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
				w := dashboardRequest(mux, method, "/dashboard/api/"+endpoint, token)
				if w.Code != http.StatusMethodNotAllowed {
					t.Fatalf("%s %s returned %d", method, endpoint, w.Code)
				}
			}
		})
	}
	if got := dashboardRequest(mux, "GET", "/dashboard/api/session", token); got.Code != 200 || !strings.Contains(got.Body.String(), `"identity":"reader"`) {
		t.Fatalf("read-scoped token could not view dashboard: %d %s", got.Code, got.Body.String())
	}
	s.authEnforced = false
	if got := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token); got.Code != 404 {
		t.Fatalf("authless dashboard returned %d", got.Code)
	}
}

func TestShellAnalyticsRequiresLiveAdministrativeCapability(t *testing.T) {
	s, _, _ := newDashboardTestServer(t)
	disabled := false
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Pipeline: config.AnalyticsPipelineConfig{Enabled: &disabled}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	s.analytics = service
	id, ok := s.identityForName("reader")
	if !ok {
		t.Fatal("missing reader")
	}
	id.Scopes = []string{ScopeFull}
	sh := s.buildSessionShell(id)
	check := func(want bool) {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := sh.Exec("analytics status", &out, &errOut, nil); (code == 0) != want {
			t.Fatalf("allowed=%v: %d %s %s", want, code, out.String(), errOut.String())
		}
	}
	s.auth.Roles["reader"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"lore:analytics:view"}}}
	check(false)
	s.auth.Roles["reader"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"lore:analytics:admin"}}}
	check(true)
	id.Scopes = []string{ScopeRead}
	readShell := s.buildSessionShell(id)
	if readShell.AnalyticsAdminAllowed() {
		t.Fatal("read token granted global administration")
	}
	s.auth.Roles["reader"] = config.RoleSpec{
		Allow: config.CapabilityRules{Capabilities: []string{"lore:analytics:admin"}},
		Deny:  config.CapabilityRules{Capabilities: []string{"lore:analytics:admin"}},
	}
	check(false)
	s.auth.Roles["reader"] = config.RoleSpec{}
	check(false)
}

func TestDashboardScopesFilesFactsAndHistory(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	for _, endpoint := range []string{"tree", "context", "file", "raw", "history", "access", "usage"} {
		for _, target := range []string{"/secret/top.txt", "/public/private/secret.md", "/docs/private/secret.md"} {
			w := dashboardRequest(mux, "GET", "/dashboard/api/"+endpoint+"?path="+url.QueryEscape(target), token)
			if w.Code != 404 || strings.Contains(w.Body.String(), "SENSITIVE") {
				t.Fatalf("%s exposed %s: %d %s", endpoint, target, w.Code, w.Body.String())
			}
		}
	}
	w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("context leaked a carved-out/sibling path: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"path":"/public/other.md"`) {
		t.Fatalf("private child hid its readable parent docset: %s", w.Body.String())
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/file?path=/docs/other.md", token)
	var result struct {
		Path  string             `json:"path"`
		Facts map[string]float64 `json:"facts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	// UTF-8 byte count differs from Unicode characters; do not count bytes as
	// characters, and count both newline-terminated lines exactly once.
	if w.Code != 200 || result.Path != "/public/other.md" || result.Facts["bytes"] != 11 || result.Facts["characters"] != 7 || result.Facts["lines"] != 2 {
		t.Fatalf("incorrect canonical facts: %d %+v", w.Code, result)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("sensitive response can be cached: %q", got)
	}
	if !strings.Contains(w.Header().Get("Vary"), "Authorization") {
		t.Fatal("response must vary by credentials")
	}
	// The same valid token must not retain removed grants on its next request.
	ds := s.auth.Docsets["public"]
	delete(ds.Access.Allow, "reader")
	s.auth.Docsets["public"] = ds
	if w := dashboardRequest(mux, "GET", "/dashboard/api/file?path=/public/other.md", token); w.Code != 404 {
		t.Fatalf("revoked content remained readable: %d", w.Code)
	}
}

func TestDashboardAccessRequiresCapabilityAndReadableDocset(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	if w := dashboardRequest(mux, "GET", "/dashboard/api/access?path=/public", token); w.Code != 404 {
		t.Fatalf("Access without capability: %d", w.Code)
	}
	s.auth.Roles["reader"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{CapabilityAccessView}}}
	w := dashboardRequest(mux, "GET", "/dashboard/api/access?path=/", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-child") || strings.Contains(w.Body.String(), `"name":"secret"`) {
		t.Fatalf("Access leaked another docset: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"role":"reader","grant":"ro"`) {
		t.Fatalf("Access omitted real role grants: %s", w.Body.String())
	}
	// A capability is not a content grant, including under a nested carve-out.
	if w := dashboardRequest(mux, "GET", "/dashboard/api/access?path=/public/private", token); w.Code != 404 {
		t.Fatalf("Access capability bypassed content policy: %d", w.Code)
	}
	s.auth.Roles["reader"] = config.RoleSpec{
		Allow: config.CapabilityRules{Capabilities: []string{CapabilityAccessView}},
		Deny:  config.CapabilityRules{Capabilities: []string{CapabilityAccessView}},
	}
	if w := dashboardRequest(mux, "GET", "/dashboard/api/access?path=/public", token); w.Code != 404 {
		t.Fatalf("capability deny lost to allow: %d", w.Code)
	}
}

func TestDashboardPasskeyRevalidationAndInvalidBearer(t *testing.T) {
	s, mux, _ := newDashboardTestServer(t)
	key := []byte("test-only-cookie-signing-material")
	pk, err := passkeys.New(passkeys.Config{RPID: "localhost", RPName: "Dashboard tests", RPOrigins: []string{"http://localhost"}, PasskeysFile: t.TempDir() + "/passkeys.json", SessionTTL: time.Hour}, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.passkeys = pk
	cookieResponse := httptest.NewRecorder()
	if _, err := passkeys.NewSessionManager(key, time.Hour).SetCookie(cookieResponse, "reader"); err != nil {
		t.Fatal(err)
	}
	request := func(header string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/dashboard/api/session", nil)
		r.AddCookie(cookieResponse.Result().Cookies()[0])
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != 200 {
		t.Fatalf("valid cookie denied: %d %s", w.Code, w.Body.String())
	}
	for _, badHeader := range []string{"Basic ignored", "Bearer invalid"} {
		if w := request(badHeader); w.Code != 401 {
			t.Fatalf("invalid bearer fell back to cookie: %d", w.Code)
		}
	}
	s.auth.Identities = s.auth.Identities[:1]
	if w := request(""); w.Code != 401 {
		t.Fatalf("removed identity still authenticated: %d", w.Code)
	}
}

func TestDashboardPreviewRawAndTimeline(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	w := dashboardRequest(mux, "GET", "/dashboard/api/file?path=/public/read%20me.md", token)
	var file struct {
		HTML   string `json:"html"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !strings.Contains(file.HTML, "<h1>Welcome</h1>") || !strings.Contains(file.HTML, "<table>") || !strings.HasPrefix(file.Source, "# Welcome") {
		t.Fatalf("not a production GFM preview: %d %+v", w.Code, file)
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/raw?path=/public/payload.html", token)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("raw HTML can execute as same-origin content: %d %v", w.Code, w.Header())
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/history?path=/public/other.md", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Fatalf("unavailable history hidden: %s", w.Body.String())
	}
	history := NewJSONLHistoryStore(t.TempDir())
	s.history = history
	if err := history.Record(context.Background(), []HistoryRecord{{CommitID: "revision-1", FileKey: "/public/other.md", Time: time.Now().UTC(), Attribution: Attribution{Principal: "alice"}, Action: "create", ContentHash: "hash-1"}}); err != nil {
		t.Fatal(err)
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/history?path=/public/other.md", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"action":"create"`) || !strings.Contains(w.Body.String(), `"hash":"hash-1"`) || !strings.Contains(w.Body.String(), `"attribution":"alice"`) {
		t.Fatalf("timeline JSON is missing metadata: %s", w.Body.String())
	}
}

func TestDashboardLargeFolderAndContextBudget(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	files := fstest.MapFS{}
	for i := range 100 {
		files[fmt.Sprintf("nested/record-%03d.md", i)] = &fstest.MapFile{Data: []byte("abcd\nef\n")}
	}
	s.merge.Mount("public", NewFSAdapter(files))
	w := dashboardRequest(mux, "GET", "/dashboard/api/tree?path=/public/nested", token)
	var tree struct{ Entries []dashboardNode }
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(tree.Entries) != 100 || tree.Entries[99].Name != "record-099.md" {
		t.Fatalf("large folder incomplete: %d, %d entries", w.Code, len(tree.Entries))
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/context?path=/public", token)
	if w.Code != 200 {
		t.Fatalf("context status %d: %s", w.Code, w.Body.String())
	}
	var node dashboardNode
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.Bytes != 800 || node.Characters != 800 || node.Lines != 200 || len(node.Children[0].Children) != 100 {
		t.Fatalf("incorrect aggregate context: %+v", node)
	}
	nodes, budget := 0, int64(7)
	if _, err := s.dashboardContextNode(context.Background(), NewFSAdapter(files), "/nested/record-000.md", 0, &nodes, &budget); err != errDashboardSize {
		t.Fatalf("oversized context silently truncated: %v", err)
	}
}

func TestDashboardRootContextToleratesConcurrentlyRemovedDescendant(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	s.merge.Mount("public", disappearingDashboardFS{
		FileSystem: NewFSAdapter(fstest.MapFS{
			"keep.md": {Data: []byte("kept\n")},
			"gone.md": {Data: []byte("removed during traversal\n")},
		}),
		vanished: "/gone.md",
	})

	w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"path":"/public/keep.md"`) || strings.Contains(w.Body.String(), "gone.md") {
		t.Fatalf("root context failed around removed descendant: %d %s", w.Code, w.Body.String())
	}
	var root dashboardNode
	if err := json.Unmarshal(w.Body.Bytes(), &root); err != nil || root.Bytes != 5 || root.Lines != 1 {
		t.Fatalf("root context included removed content: node=%+v err=%v", root, err)
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/context?path=/public/gone.md", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("directly selected removed file returned %d: %s", w.Code, w.Body.String())
	}
}

func TestDashboardRootContextRevalidatesOversizedRemovedDescendant(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	s.merge.Mount("public", disappearingDashboardFS{
		FileSystem: NewFSAdapter(fstest.MapFS{
			"keep.md": {Data: []byte("kept\n")},
			"gone.md": {Data: []byte("stale listing\n")},
		}),
		vanished:   "/gone.md",
		listedSize: dashboardMaxBytes + 1,
	})

	w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"path":"/public/keep.md"`) || strings.Contains(w.Body.String(), "gone.md") {
		t.Fatalf("stale oversized descendant broke root context: %d %s", w.Code, w.Body.String())
	}
}

func TestDashboardContextRestoresCountersForRemovedChild(t *testing.T) {
	files := disappearingDashboardFS{
		FileSystem: NewFSAdapter(fstest.MapFS{
			"a-gone.md": {Data: []byte("gone")},
			"z-keep.md": {Data: []byte("12345")},
		}),
		vanished: "/a-gone.md",
	}
	s := &Server{}
	nodes, budget := 9998, int64(5)
	root, err := s.dashboardContextNode(context.Background(), files, "/", 0, &nodes, &budget)
	if err != nil {
		t.Fatalf("removed child consumed traversal counters: %v", err)
	}
	if root.Bytes != 5 || len(root.Children) != 1 || root.Children[0].Path != "/z-keep.md" || nodes != 10000 || budget != 0 {
		t.Fatalf("removed child affected result: root=%+v nodes=%d budget=%d", root, nodes, budget)
	}
}

func TestDashboardShellPreservesLinksAndRejectsWriteMethods(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	frontend := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><main id=dashboard></main>")}, "assets/app.js": {Data: []byte("/* built frontend */")}}
	// A fresh mux mirrors Server.Start's route and legacy URL wiring.
	mux = http.NewServeMux()
	s.dashboardRoutes(frontend)(mux)
	mux.Handle("/lore/", s.dashboardLoreHandler(frontend))
	for _, target := range []string{"/", "/?view=analytics&path=/public", "/lore/read%20me.md"} {
		w := dashboardRequest(mux, "GET", target, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "id=dashboard") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "object-src 'none'") {
			t.Fatalf("deep link did not serve safe public shell: %s %d %s", target, w.Code, w.Body.String())
		}
	}
	w := dashboardRequest(mux, "GET", "/lore/public/payload.html?raw=1", token)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("legacy raw URL not authenticated download: %d", w.Code)
	}
	if w := dashboardRequest(mux, "GET", "/lore/public/payload.html?raw=1", ""); w.Code != 401 {
		t.Fatalf("anonymous raw URL: %d", w.Code)
	}
	if w := dashboardRequest(mux, "POST", "/dashboard/api/file", token); w.Code != 405 {
		t.Fatalf("SPA captured API mutation: %d", w.Code)
	}
}

func TestDashboardAnalyticsAliasUsesCanonicalScopedFacts(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	s.analytics = service
	register, err := (&analyticsPlugin{service: service}).PrepareHTTPRoutes(s)
	if err != nil {
		t.Fatal(err)
	}
	register(mux)
	w := dashboardRequest(mux, "GET", "/analytics/aggregations/tree-size?path=/docs", token)
	var result analytics.Materialized
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Table.Rows) != 3 || result.Table.Rows[0][0] != "/public/other.md" || result.Table.Rows[1][0] != "/public/payload.html" || result.Table.Rows[2][0] != "/public/read me.md" {
		t.Fatalf("alias facts must include three readable files, not the private child: %d %s", w.Code, w.Body.String())
	}
}
