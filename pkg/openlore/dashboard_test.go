package openlore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func (f disappearingDashboardFS) ReadFileBounded(target string, maxBytes int64) ([]byte, error) {
	if vfs.CleanPath(target) == f.vanished {
		return nil, fs.ErrNotExist
	}
	return readFileBounded(f.FileSystem, target, maxBytes)
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

type sizedDashboardFileFS struct {
	size    int64
	content []byte
}

func (f sizedDashboardFileFS) Stat(string) (*vfs.FileInfo, error) {
	return &vfs.FileInfo{FileName: "large.md", FilePath: "/large.md", FileSize: f.size}, nil
}
func (sizedDashboardFileFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, nil }
func (f sizedDashboardFileFS) ReadFile(string) ([]byte, error)      { return f.content, nil }

type growingDashboardFileFS struct{ sizedDashboardFileFS }

func (growingDashboardFileFS) ReadFile(string) ([]byte, error) {
	panic("dashboard used an unbounded read")
}

func (growingDashboardFileFS) ReadFileBounded(string, int64) ([]byte, error) {
	return nil, errFileTooLarge
}

type countingDashboardFS struct {
	vfs.FileSystem
	mu    sync.Mutex
	reads int
}

type toggledDashboardFS struct {
	vfs.FileSystem
	mu     sync.Mutex
	mtime  time.Time
	vanish bool
	reads  int
}

func (f *toggledDashboardFS) Stat(p string) (*vfs.FileInfo, error) {
	info, err := f.FileSystem.Stat(p)
	if err == nil && !info.Dir {
		f.mu.Lock()
		info.FileModTime = f.mtime
		f.mu.Unlock()
	}
	return info, err
}

func (f *toggledDashboardFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	entries, err := f.FileSystem.ReadDir(p)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range entries {
		if !entries[i].Dir {
			entries[i].FileModTime = f.mtime
		}
	}
	return entries, err
}

func (f *toggledDashboardFS) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	vanish := f.vanish
	f.mu.Unlock()
	if vanish {
		return nil, fs.ErrNotExist
	}
	return f.FileSystem.ReadFile(p)
}

func (f *toggledDashboardFS) ReadFileBounded(p string, max int64) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	vanish := f.vanish
	f.mu.Unlock()
	if vanish {
		return nil, fs.ErrNotExist
	}
	return readFileBounded(f.FileSystem, p, max)
}

func (f *toggledDashboardFS) set(mtime time.Time, vanish bool) {
	f.mu.Lock()
	f.mtime, f.vanish, f.reads = mtime, vanish, 0
	f.mu.Unlock()
}

func (f *toggledDashboardFS) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *countingDashboardFS) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	return f.FileSystem.ReadFile(p)
}

func (f *countingDashboardFS) ReadFileBounded(p string, max int64) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	return readFileBounded(f.FileSystem, p, max)
}

func (f *countingDashboardFS) reset() {
	f.mu.Lock()
	f.reads = 0
	f.mu.Unlock()
}

func (f *countingDashboardFS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
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

func TestDashboardLargeFolderAndContextLimits(t *testing.T) {
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
	nodes := 0
	if _, err := s.dashboardContextNode(context.Background(), sizedDashboardFileFS{size: dashboardMaxBytes + 1}, "/large.md", 0, &nodes); err != errDashboardSize {
		t.Fatalf("oversized file was accepted: %v", err)
	}
	nodes = 0
	if _, err := s.dashboardContextNode(context.Background(), growingDashboardFileFS{sizedDashboardFileFS{size: 1}}, "/growing.md", 0, &nodes); err != errDashboardSize {
		t.Fatalf("file that grew during its bounded read was accepted: %v", err)
	}
}

func TestDashboardContextSecondRootRequestReadsNoFiles(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	counted := &countingDashboardFS{FileSystem: NewFSAdapter(fstest.MapFS{
		"one.md": {Data: []byte("one\n")},
		"two.md": {Data: []byte("two words\n")},
	})}
	s.merge.Mount("public", counted)
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	s.analytics = service
	if w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token); w.Code != http.StatusOK {
		t.Fatalf("first request: %d %s", w.Code, w.Body.String())
	}
	if counted.count() != 2 {
		t.Fatalf("first request reads=%d, want 2", counted.count())
	}
	counted.reset()
	if w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token); w.Code != http.StatusOK {
		t.Fatalf("second request: %d %s", w.Code, w.Body.String())
	}
	if counted.count() != 0 {
		t.Fatalf("second root request performed %d ReadFile calls", counted.count())
	}
}

func TestDashboardContextFoldsSameIndexRowsPerIdentity(t *testing.T) {
	s, mux, readerToken := newDashboardTestServer(t)
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	s.analytics = service
	aliceToken := mint(t, s, "alice", ScopeRead)
	readTotal := func(token string) int64 {
		w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token)
		if w.Code != http.StatusOK {
			t.Fatalf("context: %d %s", w.Code, w.Body.String())
		}
		var node dashboardNode
		if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
			t.Fatal(err)
		}
		return node.Bytes
	}
	rootTotal := readTotal(aliceToken)
	restrictedTotal := readTotal(readerToken)
	if restrictedTotal >= rootTotal {
		t.Fatalf("restricted total=%d, root total=%d", restrictedTotal, rootTotal)
	}
}

func TestDashboardRootLimitCountsOnlyAuthorizedOwners(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	dir := t.TempDir()
	service, err := analytics.New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	service.SetKnowledgeScopes([]analytics.KnowledgeScope{{Name: "public", Root: "/public"}, {Name: "private-child", Root: "/public/private"}})
	service.Start(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, status, err := service.IndexedFacts(context.Background(), "/", 1)
		if err == nil && status.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("facts did not warm: %+v err=%v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "aggregations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO files(path,owner,size,mtime_ns,content_hash,computed_at,generation) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10001; i++ {
		if _, err := statement.Exec(fmt.Sprintf("/public/private/denied-%05d.md", i), "private-child", 1, 1, "x", time.Now().UnixNano(), 1); err != nil {
			t.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.analytics = service
	w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token)
	if w.Code != http.StatusOK {
		t.Fatalf("denied rows consumed authorized node limit: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "denied-") || !strings.Contains(w.Body.String(), "restricted docsets omitted") {
		t.Fatalf("response leaked or failed to label restricted coverage: %s", w.Body.String())
	}
}

func TestDashboardPollingPreservesFailedScan(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	// Stat succeeds, but reading this file fails during the background scan.
	fsys := &countingDashboardFS{FileSystem: disappearingDashboardFS{FileSystem: s.merge, vanished: "/public/other.md"}}
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: fsys})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	service.SetKnowledgeScopes([]analytics.KnowledgeScope{{Name: "public", Root: "/public"}})
	service.Start(context.Background())
	s.analytics = service
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, status, err := service.IndexedFacts(context.Background(), "/public", 1)
		if err == nil && status.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not fail: %+v err=%v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
	reads := fsys.count()
	for range 4 {
		w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/public", token)
		var node dashboardNode
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &node) != nil || node.Analytics == nil {
			t.Fatalf("context response: %d %s", w.Code, w.Body.String())
		}
		if node.Analytics.State != "failed" || node.Analytics.Updating || node.Analytics.Error == "" {
			t.Fatalf("poll erased failure or restarted scan: %+v", node.Analytics)
		}
	}
	if fsys.count() != reads {
		t.Fatal("polling retried the failed file")
	}
}

func TestDashboardHidesStaleOwnersImmediatelyAfterDocsetTopologyChange(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	dir := t.TempDir()
	first, err := analytics.New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	first.SetKnowledgeScopes([]analytics.KnowledgeScope{{Name: "public", Root: "/public"}})
	first.Start(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, status, err := first.IndexedFacts(context.Background(), "/public/private", 10)
		if err == nil && status.Complete {
			if len(rows) != 1 || rows[0].Owner != "public" {
				t.Fatalf("precondition stale-owner row=%+v", rows)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial ownership did not complete: %+v err=%v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	disabled := false
	second, err := analytics.New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Enabled: &disabled}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	second.SetKnowledgeScopes([]analytics.KnowledgeScope{{Name: "public", Root: "/public"}, {Name: "private-child", Root: "/public/private"}})
	second.Start(context.Background())
	s.analytics = second
	w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token)
	if w.Code != http.StatusOK {
		t.Fatalf("context during incompatible rebuild: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"path":"/public/private`) || strings.Contains(w.Body.String(), "SENSITIVE") {
		t.Fatalf("stale parent ownership disclosed nested private content: %s", w.Body.String())
	}
	var node dashboardNode
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.Analytics == nil || node.Analytics.State != "disabled" || node.Analytics.Complete || node.Analytics.Updating {
		t.Fatalf("incompatible disabled status=%+v", node.Analytics)
	}
}

func TestDashboardNodeLimitAppliesWithWarmIndex(t *testing.T) {
	s := &Server{merge: NewMergeFS()}
	s.merge.SetRoot(NewFSAdapter(fstest.MapFS{"a.md": {Data: []byte("a")}}))
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	s.analytics = service
	info, err := s.merge.Stat("/a.md")
	if err != nil {
		t.Fatal(err)
	}
	nodes := 0
	if _, err := s.dashboardContextNodeFromInfo(context.Background(), s.merge, "/a.md", info, 0, &nodes); err != nil {
		t.Fatal(err)
	}
	nodes = 10000
	if _, err := s.dashboardContextNodeFromInfo(context.Background(), s.merge, "/a.md", info, 0, &nodes); !errors.Is(err, errDashboardSize) {
		t.Fatalf("warm 10,001st node error=%v", err)
	}
}

func TestDashboardFactsUseRawBytesNotReadTransform(t *testing.T) {
	raw := NewMergeFS()
	raw.SetRoot(NewFSAdapter(fstest.MapFS{"SKILL.md": {Data: []byte("raw")}}))
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: raw})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	s := &Server{merge: raw, analytics: service}
	scoped := &readTransformFS{FileSystem: raw, transforms: []ContentTransform{func(_ string, content []byte) []byte {
		return append(content, []byte("-remote-status")...)
	}}}
	nodes := 0
	node, err := s.dashboardContextNode(context.Background(), scoped, "/SKILL.md", 0, &nodes)
	if err != nil {
		t.Fatal(err)
	}
	if node.Bytes != 3 || node.Characters != 3 {
		t.Fatalf("dashboard facts used transformed SKILL.md: %+v", node)
	}
}

func TestDashboardFactsUseAuthorizedConfigView(t *testing.T) {
	sessionContent := []byte("{\"session\":true}\n")
	authPath := filepath.Join(t.TempDir(), "lore.json")
	if err := os.WriteFile(authPath, sessionContent, 0o600); err != nil {
		t.Fatal(err)
	}
	raw := NewMergeFS()
	raw.SetRoot(NewFSAdapter(fstest.MapFS{
		"opt/openlore/lore.json": {Data: []byte("{\"durable\":true,\"larger\":true}\n")},
	}))
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: raw})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	auth := &config.AuthConfig{
		Roles:      map[string]config.RoleSpec{"admin": {Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}},
		Docsets:    map[string]config.DocsetSpec{},
		Identities: []config.AuthIdentity{{Name: "adil", Roles: []string{"admin"}}},
	}
	s := &Server{
		merge: raw, analytics: service, auth: auth, authEnforced: true,
		grants: newGrantRegistry(), config: config.Config{AuthFile: authPath},
		authorizationStore: fileAuthorizationStore{auth: auth},
	}
	id, ok := s.identityForName("adil")
	if !ok {
		t.Fatal("missing config administrator")
	}
	scoped := s.buildCanonicalSessionFS(id)
	content, err := readFileBounded(scoped, authConfigVFSPath, dashboardMaxBytes)
	if err != nil || !bytes.Equal(content, sessionContent) {
		t.Fatalf("authorized config view = %q, %v", content, err)
	}
	nodes := 0
	node, err := s.dashboardContextNode(context.Background(), scoped, authConfigVFSPath, 0, &nodes)
	if err != nil {
		t.Fatal(err)
	}
	if node.Bytes != int64(len(sessionContent)) || node.Characters != int64(len(sessionContent)) || node.Lines != 1 {
		t.Fatalf("dashboard facts did not prefer authorized config view: %+v", node)
	}
}

func TestDashboardIndexFailureFallsBackAndLogsOnce(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.analytics = service
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(original) })
	for range 2 {
		w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/public/other.md", token)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bytes":11`) || !strings.Contains(w.Body.String(), `"characters":7`) {
			t.Fatalf("fallback response: %d %s", w.Code, w.Body.String())
		}
	}
	if got := strings.Count(logs.String(), "analytics facts index unavailable; computing uncached"); got != 1 {
		t.Fatalf("index failure log count=%d: %s", got, logs.String())
	}
}

func TestDashboardErrNotExistMidWalkDeletesIndexRow(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	files := &toggledDashboardFS{FileSystem: NewFSAdapter(fstest.MapFS{"gone.md": {Data: []byte("same")}}), mtime: time.Unix(1, 0)}
	s.merge.Mount("public", files)
	service, err := analytics.New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{FS: s.merge})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	s.analytics = service
	if w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "gone.md") {
		t.Fatalf("warm request: %d %s", w.Code, w.Body.String())
	}
	files.set(time.Unix(2, 0), true)
	if w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "gone.md") {
		t.Fatalf("mid-walk removal: %d %s", w.Code, w.Body.String())
	}
	files.set(time.Unix(1, 0), false)
	if w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/", token); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "gone.md") {
		t.Fatalf("restored request: %d %s", w.Code, w.Body.String())
	}
	if files.readCount() != 1 {
		t.Fatalf("restored file reads=%d; stale index row was not removed", files.readCount())
	}
}

func TestDashboardContextAllowsCorpusLargerThanSingleFileLimit(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	content := bytes.Repeat([]byte{'a'}, dashboardMaxBytes/2+1)
	s.merge.Mount("public", NewFSAdapter(fstest.MapFS{
		"large/a.md": {Data: content},
		"large/b.md": {Data: content},
	}))

	w := dashboardRequest(mux, "GET", "/dashboard/api/context?path=/public/large", token)
	if w.Code != http.StatusOK {
		t.Fatalf("large corpus context status %d: %s", w.Code, w.Body.String())
	}
	var node dashboardNode
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	want := int64(2 * len(content))
	if node.Bytes != want || node.Characters != want || len(node.Children) != 2 {
		t.Fatalf("large corpus totals = %+v, want bytes and characters %d across two files", node, want)
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

func TestDashboardContextRestoresNodeCounterForRemovedChild(t *testing.T) {
	files := disappearingDashboardFS{
		FileSystem: NewFSAdapter(fstest.MapFS{
			"a-gone.md": {Data: []byte("gone")},
			"z-keep.md": {Data: []byte("12345")},
		}),
		vanished: "/a-gone.md",
	}
	s := &Server{}
	nodes := 9998
	root, err := s.dashboardContextNode(context.Background(), files, "/", 0, &nodes)
	if err != nil {
		t.Fatalf("removed child consumed traversal counter: %v", err)
	}
	if root.Bytes != 5 || len(root.Children) != 1 || root.Children[0].Path != "/z-keep.md" || nodes != 10000 {
		t.Fatalf("removed child affected result: root=%+v nodes=%d", root, nodes)
	}
}

func TestDashboardShellPreservesLinksAndRejectsWriteMethods(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	frontend := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><main id=dashboard></main>")}, "assets/app.js": {Data: []byte("/* built frontend */")}}
	// A fresh mux mirrors Server.Start's route and legacy URL wiring.
	mux = http.NewServeMux()
	s.dashboardRoutes(frontend)(mux)
	mux.Handle("/lore/", s.dashboardLoreHandler(frontend))
	for _, target := range []string{"/dashboard/", "/lore/read%20me.md"} {
		w := dashboardRequest(mux, "GET", target, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "id=dashboard") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "object-src 'none'") {
			t.Fatalf("deep link did not serve safe public shell: %s %d %s", target, w.Code, w.Body.String())
		}
	}
	if w := dashboardRequest(mux, "GET", "/", ""); w.Code != http.StatusNotFound {
		t.Fatalf("dashboard claimed website root: %d", w.Code)
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
	var result analytics.Materialized
	var w *httptest.ResponseRecorder
	deadline := time.Now().Add(2 * time.Second)
	for {
		w = dashboardRequest(mux, "GET", "/analytics/aggregations/tree-size?path=/docs", token)
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status == analytics.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("materialized alias facts did not finish: %d %s", w.Code, w.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.Code != 200 || len(result.Table.Rows) != 3 || result.Table.Rows[0][0] != "/public/other.md" || result.Table.Rows[1][0] != "/public/payload.html" || result.Table.Rows[2][0] != "/public/read me.md" {
		t.Fatalf("alias facts must include three readable files, not the private child: %d %s", w.Code, w.Body.String())
	}
}
