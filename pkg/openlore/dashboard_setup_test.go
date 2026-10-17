package openlore

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/rules"
)

func newSetupTestServer(t *testing.T) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, mux, token := newDashboardTestServer(t)
	s.merge.Mount("public", NewFSAdapter(fstest.MapFS{
		"read me.md":                       {Data: []byte("# Welcome\n")},
		".lore/config.yaml":                {Data: []byte("version: 1\nrules: {}\n")},
		"guides/.lore/config.yaml":         {Data: []byte("version: 1\n")},
		"guides/intro.md":                  {Data: []byte("intro\n")},
		"private/.lore/config.yaml":        {Data: []byte("version: 1\n")},
		"private/secret.md":                {Data: []byte("SENSITIVE_NESTED_CONTENT")},
		"private/nested/.lore/config.yaml": {Data: []byte("version: 1\n")},
	}))
	return s, mux, token
}

func TestDashboardTreeAndFinderHideFolderConfig(t *testing.T) {
	_, mux, token := newSetupTestServer(t)
	w := dashboardRequest(mux, "GET", "/dashboard/api/tree?path=/public", token)
	var tree struct{ Entries []dashboardNode }
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		names = append(names, entry.Name)
	}
	if w.Code != 200 || strings.Join(names, ",") != "guides,read me.md" {
		t.Fatalf("tree listed configuration: %d %v", w.Code, names)
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/files?path=/", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), ".lore") || !strings.Contains(w.Body.String(), "/public/guides/intro.md") {
		t.Fatalf("finder listed configuration: %d %s", w.Code, w.Body.String())
	}
	// The file itself stays readable at its path; only the listing hides it.
	if w := dashboardRequest(mux, "GET", "/dashboard/api/file?path=/public/guides/.lore/config.yaml", token); w.Code != 200 || !strings.Contains(w.Body.String(), "version: 1") {
		t.Fatalf("config file unreadable: %d %s", w.Code, w.Body.String())
	}
}

func TestDashboardSetupListsReadableConfigOnly(t *testing.T) {
	s, mux, token := newSetupTestServer(t)
	w := dashboardRequest(mux, "GET", "/dashboard/api/setup", token)
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	var setup dashboardSetupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if setup.Identity != "reader" || strings.Join(setup.Roles, ",") != "reader" || setup.Access || setup.ConfigEdit {
		t.Fatalf("unexpected session summary: %+v", setup)
	}
	paths := make([]string, 0, len(setup.ConfigFiles))
	for _, file := range setup.ConfigFiles {
		paths = append(paths, file.Kind+":"+file.Docset+":"+file.Path)
	}
	// The nested private-child docset carve-out and its descendants are not
	// readable by this identity, so their config never appears.
	if strings.Join(paths, " ") != "folder:public:/public/.lore/config.yaml folder:public:/public/guides/.lore/config.yaml" {
		t.Fatalf("config files = %v", paths)
	}
	if setup.Truncated || len(setup.Docsets) != 1 {
		t.Fatalf("docsets = %+v truncated=%v", setup.Docsets, setup.Truncated)
	}
	ds := setup.Docsets[0]
	if ds.Name != "public" || strings.Join(ds.Roots, ",") != "/public" || strings.Join(ds.Aliases, ",") != "/docs" || strings.Join(ds.Grants, ",") != "ro" || ds.Readonly || ds.Writable {
		t.Fatalf("docset = %+v", ds)
	}
	if strings.Join(ds.ConfigFiles, ",") != "/public/.lore/config.yaml,/public/guides/.lore/config.yaml" {
		t.Fatalf("docset config files = %v", ds.ConfigFiles)
	}
	if len(ds.Roles) != 0 || ds.Rules != nil || setup.Rules != nil || strings.Contains(w.Body.String(), "alice") {
		t.Fatalf("operator detail leaked without lore:access:view: %s", w.Body.String())
	}
	if !strings.HasPrefix(setup.Server.SSH, "ssh ") || setup.Server.HTTPURL == "" || setup.Server.LorePath != "/lore" || !setup.Server.Readonly {
		t.Fatalf("server summary = %+v", setup.Server)
	}

	s.auth.Roles["reader"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{CapabilityAccessView}}}
	w = dashboardRequest(mux, "GET", "/dashboard/api/setup", token)
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if !setup.Access || len(setup.Docsets) != 1 || len(setup.Docsets[0].Roles) == 0 {
		t.Fatalf("operator detail missing with lore:access:view: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-child") {
		t.Fatalf("access view leaked an unreadable docset: %s", w.Body.String())
	}

	// OKF counts when enabled by a rule, not only by the legacy docset block.
	if setup.Docsets[0].OKF {
		t.Fatalf("okf reported without any OKF configuration: %+v", setup.Docsets[0])
	}
	s.auth.Rules = map[string]rules.RuleSpec{"format": {Match: []string{"**/*.md"}, Use: "okf"}}
	w = dashboardRequest(mux, "GET", "/dashboard/api/setup", token)
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if !setup.Docsets[0].OKF {
		t.Fatalf("okf not reported for a server-wide okf rule: %+v", setup.Docsets[0])
	}
}

func TestDashboardSetupListsAuthConfigForEditorsAndHidesItFromTree(t *testing.T) {
	s, mux, _ := newSetupTestServer(t)
	authPath := filepath.Join(t.TempDir(), "lore.json")
	if err := os.WriteFile(authPath, []byte("{\"docsets\":{}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.config.AuthFile = authPath
	s.auth.Roles["admin"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}
	s.auth.Identities = append(s.auth.Identities, config.AuthIdentity{Name: "admin", Roles: []string{"admin"}})
	ds := s.auth.Docsets["public"]
	ds.Access.Allow["admin"] = "ro"
	s.auth.Docsets["public"] = ds
	token := mint(t, s, "admin", ScopeFull)

	w := dashboardRequest(mux, "GET", "/dashboard/api/tree?path=/", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"name":"opt"`) || !strings.Contains(w.Body.String(), `"name":"public"`) {
		t.Fatalf("tree exposed the config view: %d %s", w.Code, w.Body.String())
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/files?path=/", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), "lore.json") {
		t.Fatalf("finder exposed the config view: %d %s", w.Code, w.Body.String())
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/setup", token)
	var setup dashboardSetupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !setup.ConfigEdit || len(setup.ConfigFiles) == 0 || setup.ConfigFiles[0].Kind != "auth" || setup.ConfigFiles[0].Path != authConfigVFSPath {
		t.Fatalf("setup omitted the live lore.json: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), serverConfigVFSPath) {
		t.Fatalf("setup listed a server config the server never loaded: %s", w.Body.String())
	}
	if w := dashboardRequest(mux, "GET", "/dashboard/api/file?path="+authConfigVFSPath, token); w.Code != 200 || !strings.Contains(w.Body.String(), `docsets`) {
		t.Fatalf("lore.json unreadable through the viewer: %d %s", w.Code, w.Body.String())
	}

	// Once the server runs from an openlore.yml, the same sessions see it
	// beside lore.json, read-only.
	s.config.ConfigFile = filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(s.config.ConfigFile, []byte("http_port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/setup", token)
	setup = dashboardSetupResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(setup.ConfigFiles) < 2 || setup.ConfigFiles[1].Kind != "server" || setup.ConfigFiles[1].Path != serverConfigVFSPath || setup.ConfigFiles[1].Scope != "/" {
		t.Fatalf("setup omitted openlore.yml: %d %s", w.Code, w.Body.String())
	}
	w = dashboardRequest(mux, "GET", "/dashboard/api/file?path="+serverConfigVFSPath, token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "http_port: 8080") {
		t.Fatalf("openlore.yml unreadable through the viewer: %d %s", w.Code, w.Body.String())
	}
	if w := dashboardRequest(mux, "GET", "/dashboard/api/files?path=/", token); w.Code != 200 || strings.Contains(w.Body.String(), "openlore.yml") {
		t.Fatalf("finder exposed openlore.yml: %d %s", w.Code, w.Body.String())
	}
	if w := dashboardRequest(mux, "GET", "/dashboard/api/setup", mint(t, s, "reader", ScopeRead)); w.Code != 200 || strings.Contains(w.Body.String(), "/opt/") {
		t.Fatalf("reader saw the config view: %d %s", w.Code, w.Body.String())
	}
}
