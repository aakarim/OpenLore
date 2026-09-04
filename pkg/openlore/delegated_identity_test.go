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
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
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

type captureReload struct{ got cmds.JobAttribution }

func (r *captureReload) Reload(got cmds.JobAttribution) error { r.got = got; return nil }

func TestConfigReloadUsesPerShellBackendAndImmutableAttribution(t *testing.T) {
	backend := &captureReload{}
	sh := shell.NewShell(&wlRecordingFS{})
	sh.SetAllowedActions([]cmds.Action{cmds.ActionAdmin})
	sh.SetConfigReloadBackend(backend)
	sh.SetCommandAttribution(cmds.JobAttribution{Principal: "alice", Actor: "client@vendor.example"})
	sh.SetEnv("OPENLORE_IDENTITY", "forged")
	sh.SetEnv("OPENLORE_ACTOR", "forged-actor")
	if _, stderr, code := run(sh, "lore config reload"); code != 0 {
		t.Fatalf("reload failed: %s", stderr)
	}
	if backend.got.Principal != "alice" || backend.got.Actor != "client@vendor.example" {
		t.Fatalf("reload attribution=%+v", backend.got)
	}

	other := shell.NewShell(&wlRecordingFS{})
	other.SetAllowedActions([]cmds.Action{cmds.ActionAdmin})
	if _, _, code := run(other, "lore config reload"); code == 0 {
		t.Fatal("shell without a bound backend reused another server's reloader")
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
		"grant_type":  {delegationGrantType},
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

func TestDelegationDoesNotAdvertiseTheRFC8693Contract(t *testing.T) {
	s := newTokenTestServer(t, true, "deny")
	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported_grant_type") {
		t.Fatalf("standard token-exchange URN was accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOAuthConsentCreatesDelegatedIdentity(t *testing.T) {
	s := newTokenTestServer(t, true, "deny")
	s.auth.Identities = append(s.auth.Identities, config.AuthIdentity{Name: "bob", Roles: append([]string(nil), s.auth.Identities[0].Roles...)})
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

	_, secondChallenge := pkcePair()
	_, secondAuthz := runAuthorize(t, s, url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://claude.ai/callback"},
		"code_challenge": {secondChallenge}, "code_challenge_method": {"S256"},
	})
	secondConsent, ok := s.CompleteAuthorize(secondAuthz, "bob")
	if !ok {
		t.Fatal("second principal did not reach consent")
	}
	secondURL, _ := url.Parse(secondConsent)
	secondForm := url.Values{"consent": {secondURL.Query().Get("consent")}, "delegate": {"new"}, "name": {"claude"}, "docset": {"public"}}
	secondReq := httptest.NewRequest(http.MethodPost, authorizeConsentPath, strings.NewReader(secondForm.Encode()))
	secondReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secondRec := httptest.NewRecorder()
	s.authorizeConsentHandler(secondRec, secondReq)
	if secondRec.Code != http.StatusFound {
		t.Fatalf("second principal status=%d body=%s", secondRec.Code, secondRec.Body.String())
	}
	bob, _ := s.findAuthIdentity("bob")
	if _, ok := findDelegate(bob, "claude@claude.ai"); !ok {
		t.Fatal("second principal did not reuse the OAuth identity")
	}
	count := 0
	for _, identity := range s.currentAuth().Identities {
		if identity.Name == "claude@claude.ai" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("OAuth identity count=%d", count)
	}
}

func TestConfigAdminWithoutWritableDocsetCanUseConfigWriteVerbs(t *testing.T) {
	s := newTokenTestServer(t, true, "deny")
	s.config.AuthFile = filepath.Join(t.TempDir(), "lore.json")
	s.auth.Roles["config-admin"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}
	s.auth.Identities[0].Roles = []string{"config-admin"}
	id, _ := s.identityForName("alice")
	sh := s.buildSessionShell(id)
	if !sh.ActionAllowed(cmds.ActionAdmin) || !sh.ActionAllowed(cmds.ActionWrite) {
		t.Fatal("config administrator cannot invoke config write path")
	}
	if sh.ActionAllowed(cmds.ActionPublish) {
		t.Fatal("config-only administrator gained publish authority")
	}
}

func TestWriteLogPersistsStructuredAttribution(t *testing.T) {
	dir := t.TempDir()
	store := NewJSONLHistoryStore(dir)
	log := newWriteLog(&wlRecordingFS{}, nil, nil, 1)
	log.SetHistoryRecorder(store)
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	_, err := log.Submit(context.Background(), Attribution{Principal: "adil", Actor: "claude@claude.ai"}, writeCS("/plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record HistoryRecord
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record.Attribution.Principal != "adil" || record.Attribution.Actor != "claude@claude.ai" || record.FileKey != "/plan.md" {
		t.Fatalf("record=%+v", record)
	}
}

func TestConfigViewValidatesBeforePersistAndReloadsExplicitly(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "lore.json")
	auth := &config.AuthConfig{Roles: map[string]config.RoleSpec{"admin": {Allow: config.CapabilityRules{Capabilities: []string{"lore:config:edit"}}}}, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "adil", Roles: []string{"admin"}}}}
	writeAuthFixture(t, authPath, auth)
	s := &Server{auth: auth, authEnforced: true, grants: newGrantRegistry(), config: config.Config{AuthFile: authPath}, authorizationStore: fileAuthorizationStore{auth: auth}}
	id := Identity{IdentityName: "adil", Principal: AuthenticatedPrincipal{IdentityName: "adil"}, Scopes: []string{ScopeFull}}
	fsys := &configViewFS{WritableFS: &wlRecordingFS{}, server: s, identity: id, attribution: Attribution{Principal: "adil"}}
	if _, err := fsys.WriteFileAtomic(authConfigVFSPath, []byte(`{"identities":[{"name":"bad/name"}]}`), vfs.WriteOpts{}); err == nil {
		t.Fatal("invalid config persisted")
	}
	next := &config.AuthConfig{Roles: auth.Roles, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "adil", Roles: []string{"admin"}}, {Name: "bot"}}}
	b, _ := json.Marshal(next)
	if _, err := fsys.WriteFileAtomic(authConfigVFSPath, b, vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.findAuthIdentity("bot"); ok {
		t.Fatal("config edit changed runtime state before reload")
	}
	if err := s.Reload(cmds.JobAttribution{Principal: "adil"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.findAuthIdentity("bot"); !ok {
		t.Fatal("reload did not activate persisted config")
	}
	revoked := *next
	revoked.Roles = map[string]config.RoleSpec{"admin": {}}
	writeAuthFixture(t, authPath, &revoked)
	if err := s.Reload(cmds.JobAttribution{Principal: "adil"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.ReadFile(authConfigVFSPath); !os.IsPermission(err) {
		t.Fatalf("revoked session retained config read access: %v", err)
	}
	if _, err := fsys.WriteFileAtomic(authConfigVFSPath, b, vfs.WriteOpts{}); !os.IsPermission(err) {
		t.Fatalf("revoked session retained config write access: %v", err)
	}
}

func TestLiveAuthRejectsServerDerivedPolicyChanges(t *testing.T) {
	auth := &config.AuthConfig{Docsets: map[string]config.DocsetSpec{"docs": {Paths: []config.PathMapping{{Source: "/docs", Display: "/docs"}}}}}
	s := &Server{auth: auth, authEnforced: true, grants: newGrantRegistry()}
	next := *auth
	next.Docsets = map[string]config.DocsetSpec{"other": {Paths: []config.PathMapping{{Source: "/other", Display: "/other"}}}}
	if err := s.validateLiveAuthCandidate(&next); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("docset change accepted: %v", err)
	}
	keyless := true
	next = *auth
	next.AllowKeyless = &keyless
	if err := s.validateLiveAuthCandidate(&next); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("posture change accepted: %v", err)
	}
	next = *auth
	next.Rules = map[string]rules.RuleSpec{"limit": {Use: "size/lines", Match: []string{"**"}, With: map[string]any{"max": 10}}}
	if err := s.validateLiveAuthCandidate(&next); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("top-level rule change accepted: %v", err)
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
