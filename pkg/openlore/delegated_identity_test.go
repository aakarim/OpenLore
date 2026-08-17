package openlore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestIssuerDelegatedAttributionRoundTrip(t *testing.T) {
	issuer := testIssuer(t)
	token, _, err := issuer.Mint(Attribution{Principal: "adil", Actor: "claude@claude.ai"}, ScopeFull, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "adil" || claims.Actor != "claude@claude.ai" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestTokenExchangeMintsCappedDelegatedToken(t *testing.T) {
	s := newTokenTestServer(t, true, "deny")
	s.auth.Identities[0].Delegates = []config.DelegateEntry{{Identity: "build-agent", DenyDocsets: []string{"secret"}}}
	s.auth.Identities = append(s.auth.Identities, config.AuthIdentity{Name: "build-agent"})
	actorToken, _, err := s.issuer.Mint(Attribution{Principal: "build-agent"}, ScopeFull, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rec, response := postForm(t, s.tokens, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"actor_token": {actorToken}, "act_for": {"alice"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	claims, err := s.issuer.Verify(response.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.resolveClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Attribution.Principal != "alice" || identity.Attribution.Actor != "build-agent" {
		t.Fatalf("attribution=%+v", identity.Attribution)
	}
	if _, ok := s.effectiveGrantNames(identity, "secret"); ok {
		t.Fatal("delegated token bypassed denied docset")
	}
	if _, ok := s.effectiveGrantNames(identity, "public"); !ok {
		t.Fatal("delegated token did not inherit principal authority")
	}
}

func TestOAuthConsentCreatesDelegatedIdentity(t *testing.T) {
	s := newTokenTestServer(t, true, "deny")
	authPath := filepath.Join(t.TempDir(), "lore.json")
	s.config.AuthFile = authPath
	writeAuthFixture(t, authPath, s.auth)
	s.audit = nil
	registration := postJSON(t, s, `{"client_name":"Claude Desktop","redirect_uris":["https://claude.ai/callback"]}`)
	var registered map[string]any
	if err := json.Unmarshal(registration.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	clientID := registered["client_id"].(string)
	_, challenge := pkcePair()
	_, authz := runAuthorize(t, s, url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://claude.ai/callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	})
	consentURL, ok := s.CompleteAuthorize(authz, "alice")
	if !ok || !strings.HasPrefix(consentURL, authorizeConsentPath+"?") {
		t.Fatalf("consent URL=%q ok=%v", consentURL, ok)
	}
	u, _ := url.Parse(consentURL)
	form := url.Values{"consent": {u.Query().Get("consent")}, "delegate": {"new"}, "name": {"claude"}, "docset": {"public", "secret"}}
	req := httptest.NewRequest(http.MethodPost, authorizeConsentPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.authorizeConsentHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	principal, _ := s.findAuthIdentity("alice")
	if delegate, ok := findDelegate(principal, "claude@claude.ai"); !ok || len(delegate.DenyDocsets) != 0 {
		t.Fatalf("delegate=%+v ok=%v", delegate, ok)
	}
	created, ok := s.findAuthIdentity("claude@claude.ai")
	if !ok || created.CreatedBy != "oauth" || len(created.Roles) != 0 || created.Home != "" {
		t.Fatalf("created identity=%+v", created)
	}
	callback, _ := url.Parse(rec.Header().Get("Location"))
	code, ok := s.authCodes.Consume(callback.Query().Get("code"))
	if !ok || code.Subject != "alice" || code.Actor != "claude@claude.ai" {
		t.Fatalf("code=%+v ok=%v", code, ok)
	}
}

func TestWriteLogPersistsStructuredAttribution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commits.jsonl")
	log := newWriteLog(&wlRecordingFS{}, nil, nil, 1)
	log.SetCommitRecorder(NewJSONLCommitRecorder(path))
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	_, err := log.Submit(context.Background(), Attribution{Principal: "adil", Actor: "claude@claude.ai"}, writeCS("/plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record CommitRecord
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record.Attribution.Principal != "adil" || record.Attribution.Actor != "claude@claude.ai" || record.ChangeSet.Target != "/plan.md" {
		t.Fatalf("record=%+v", record)
	}
}

func TestConfigViewValidatesBeforePersistAndReloadsExplicitly(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "lore.json")
	auth := &config.AuthConfig{Roles: map[string]config.RoleSpec{"admin": {}}, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "adil", Roles: []string{"admin"}}}}
	writeAuthFixture(t, authPath, auth)
	s := &Server{auth: auth, config: config.Config{AuthFile: authPath}, authorizationStore: fileAuthorizationStore{auth: auth}}
	fsys := &configViewFS{WritableFS: &wlRecordingFS{}, server: s, attribution: Attribution{Principal: "adil"}}
	if _, err := fsys.WriteFileAtomic(authConfigVFSPath, []byte(`{"identities":[{"name":"bad/name"}]}`), vfs.WriteOpts{}); err == nil {
		t.Fatal("invalid config persisted")
	}
	next := &config.AuthConfig{Roles: map[string]config.RoleSpec{"admin": {}}, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "adil", Roles: []string{"admin"}}, {Name: "bot"}}}
	b, _ := json.Marshal(next)
	if _, err := fsys.WriteFileAtomic(authConfigVFSPath, b, vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.findAuthIdentity("bot"); ok {
		t.Fatal("config edit changed runtime state before reload")
	}
	if err := s.Reload("adil", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.findAuthIdentity("bot"); !ok {
		t.Fatal("reload did not activate persisted config")
	}
}

func writeAuthFixture(t *testing.T, path string, auth *config.AuthConfig) {
	t.Helper()
	b, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
