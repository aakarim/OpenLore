package openlore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/passkeys"
)

type captureAuditLog struct{ events []AuditEvent }

func (l *captureAuditLog) Record(_ context.Context, event AuditEvent) error {
	l.events = append(l.events, event)
	return nil
}

type actorFailingAuthorizationStore struct{ AuthorizationStore }

func (s actorFailingAuthorizationStore) ResolveAuthorization(ctx context.Context, principal AuthenticatedPrincipal) (AuthorizationPolicy, error) {
	if actor, _ := principal.Claims["actor"].(string); actor != "" {
		return AuthorizationPolicy{}, errors.New("delegate policy references an unknown role")
	}
	return s.AuthorizationStore.ResolveAuthorization(ctx, principal)
}

type failingRevokeStore struct{ RefreshTokenStore }

func (s failingRevokeStore) RevokeDelegation(string, string) (int, error) {
	return 0, errors.New("refresh store unavailable")
}

func newPermissionsTestServer(t *testing.T) (*Server, *passkeys.SessionManager, *captureAuditLog) {
	t.Helper()
	s := newTokenTestServer(t, true, "deny")
	s.auth.Roles["alice"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"lore:publish"}}}
	s.auth.Identities[0].Delegates = []config.DelegateEntry{{Identity: "claude@claude.ai", DenyDocsets: []string{"secret"}, ClientAuth: string(AuthCIMD)}}
	s.auth.Identities = append(s.auth.Identities,
		config.AuthIdentity{Name: "claude@claude.ai", Comment: "Display: Claude", CreatedBy: "oauth"},
		config.AuthIdentity{Name: "bob", Roles: []string{"alice"}, Delegates: []config.DelegateEntry{{Identity: "chatgpt@chatgpt.com"}}},
		config.AuthIdentity{Name: "chatgpt@chatgpt.com", Comment: "Display: ChatGPT", CreatedBy: "oauth"},
	)
	s.config.AuthFile = filepath.Join(t.TempDir(), "lore.json")
	writeAuthFixture(t, s.config.AuthFile, s.auth)
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	audit := &captureAuditLog{}
	s.audit = audit
	key := []byte("settings-test-key")
	pk, err := passkeys.New(passkeys.Config{
		Enabled: true, RPID: "localhost", RPName: "OpenLore", RPOrigins: []string{"http://localhost"},
		PasskeysFile: filepath.Join(t.TempDir(), "passkeys.json"), SessionTTL: time.Hour,
	}, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pk.Shutdown)
	pk.SetAuthConfig(s.auth)
	s.passkeys = pk
	return s, passkeys.NewSessionManager(key, time.Hour), audit
}

func authenticatedPermissionsRequest(t *testing.T, manager *passkeys.SessionManager, method, target string, form url.Values) *http.Request {
	t.Helper()
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	if err := manager.SetCookie(rec, "alice"); err != nil {
		t.Fatal(err)
	}
	req.AddCookie(rec.Result().Cookies()[0])
	return req
}

func csrfForRequest(t *testing.T, s *Server, req *http.Request) string {
	t.Helper()
	session, ok := s.passkeys.Session(req)
	if !ok {
		t.Fatal("session did not validate")
	}
	return s.passkeys.CSRFToken(session)
}

func authenticatedFormRequest(t *testing.T, s *Server, manager *passkeys.SessionManager, target string, form url.Values) *http.Request {
	t.Helper()
	cookieReq := authenticatedPermissionsRequest(t, manager, http.MethodPost, target, nil)
	form.Set("csrf", csrfForRequest(t, s, cookieReq))
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookieReq.Cookies()[0])
	return req
}

func TestPermissionsPageRequiresSessionAndShowsEffectiveAccess(t *testing.T) {
	s, manager, _ := newPermissionsTestServer(t)

	unauthenticated := httptest.NewRecorder()
	s.handlePermissionsPage(unauthenticated, httptest.NewRequest(http.MethodGet, permissionsPath, nil))
	if unauthenticated.Code != http.StatusFound || !strings.HasPrefix(unauthenticated.Header().Get("Location"), "/passkey/login") {
		t.Fatalf("unauthenticated response: status=%d location=%q", unauthenticated.Code, unauthenticated.Header().Get("Location"))
	}

	req := authenticatedPermissionsRequest(t, manager, http.MethodGet, permissionsPath, nil)
	rec := httptest.NewRecorder()
	s.handlePermissionsPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"claude@claude.ai", "Claude", ">CIMD<", "Delegates inherit your access, minus these denials", "public", "secret", "lore:publish"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if !strings.Contains(body, `name="allowed_docsets" value="public" checked`) || strings.Contains(body, `name="allowed_docsets" value="secret" checked`) {
		t.Errorf("page did not render effective docset toggles: %s", body)
	}
}

func TestPermissionsPageKeepsUnresolvableDelegateDisconnectable(t *testing.T) {
	s, manager, _ := newPermissionsTestServer(t)
	s.authorizationStore = actorFailingAuthorizationStore{AuthorizationStore: s.authorizationStore}
	s.currentAuth().Identities[1].Comment = "cron agent for reports"
	s.currentAuth().Identities[1].CreatedBy = ""

	req := authenticatedPermissionsRequest(t, manager, http.MethodGet, permissionsPath, nil)
	rec := httptest.NewRecorder()
	s.handlePermissionsPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"claude@claude.ai", "Permissions unavailable", "unknown role", "Disconnect"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "cron agent for reports") {
		t.Error("arbitrary identity comment was rendered as a display name")
	}
	if strings.Contains(body, "Save permissions") {
		t.Error("page offered to edit permissions that could not be resolved")
	}
}

func TestDelegateUpdateIsCSRFProtectedAndSelfService(t *testing.T) {
	s, manager, audit := newPermissionsTestServer(t)

	badReq := authenticatedPermissionsRequest(t, manager, http.MethodPost, permissionsUpdatePath, url.Values{"actor": {"claude@claude.ai"}})
	badRec := httptest.NewRecorder()
	s.handleDelegateUpdate(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", badRec.Code)
	}

	form := url.Values{
		"actor":                {"claude@claude.ai"},
		"allowed_docsets":      {"public", "secret"},
		"allowed_capabilities": {"lore:publish"},
	}
	req := authenticatedFormRequest(t, s, manager, permissionsUpdatePath, form)
	rec := httptest.NewRecorder()
	s.handleDelegateUpdate(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	principal, _ := s.findAuthIdentity("alice")
	delegate, _ := findDelegate(principal, "claude@claude.ai")
	if len(delegate.DenyDocsets) != 0 || len(delegate.DenyCapabilities) != 0 {
		t.Fatalf("delegate denials=%+v", delegate)
	}
	if len(audit.events) != 1 || audit.events[0].Type != "delegate.update" {
		t.Fatalf("audit events=%+v", audit.events)
	}

	foreignReq := authenticatedFormRequest(t, s, manager, permissionsUpdatePath, url.Values{"actor": {"chatgpt@chatgpt.com"}})
	foreignRec := httptest.NewRecorder()
	s.handleDelegateUpdate(foreignRec, foreignReq)
	if foreignRec.Code != http.StatusBadRequest {
		t.Fatalf("foreign delegate update status=%d", foreignRec.Code)
	}
}

func TestDelegateRemoveRevokesRefreshChainsAndAuditsCount(t *testing.T) {
	s, manager, audit := newPermissionsTestServer(t)
	expires := time.Now().Add(time.Hour)
	for _, token := range []RefreshToken{
		{Token: "one", Subject: "alice", Actor: "claude@claude.ai", ChainID: "chain-1", ExpiresAt: expires},
		{Token: "two", Subject: "alice", Actor: "claude@claude.ai", ChainID: "chain-2", ExpiresAt: expires},
		{Token: "other", Subject: "bob", Actor: "claude@claude.ai", ChainID: "chain-3", ExpiresAt: expires},
	} {
		if err := s.refreshStore.Save(token); err != nil {
			t.Fatal(err)
		}
	}
	req := authenticatedFormRequest(t, s, manager, permissionsRemovePath, url.Values{"actor": {"claude@claude.ai"}})
	rec := httptest.NewRecorder()
	s.handleDelegateRemove(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	principal, _ := s.findAuthIdentity("alice")
	if _, ok := findDelegate(principal, "claude@claude.ai"); ok {
		t.Fatal("delegate was not removed")
	}
	for _, token := range []string{"one", "two"} {
		if _, ok, _ := s.refreshStore.Lookup(token); ok {
			t.Errorf("refresh token %q was not revoked", token)
		}
	}
	if _, ok, _ := s.refreshStore.Lookup("other"); !ok {
		t.Fatal("unrelated refresh token was revoked")
	}
	if len(audit.events) != 1 || audit.events[0].Type != "delegate.remove" || audit.events[0].Details["revoked_refresh_chains"] != 2 {
		t.Fatalf("audit events=%+v", audit.events)
	}
}

func TestDelegateRemoveRedirectsWhenRefreshRevocationFails(t *testing.T) {
	s, manager, audit := newPermissionsTestServer(t)
	s.refreshStore = failingRevokeStore{RefreshTokenStore: s.refreshStore}
	req := authenticatedFormRequest(t, s, manager, permissionsRemovePath, url.Values{"actor": {"claude@claude.ai"}})
	rec := httptest.NewRecorder()
	s.handleDelegateRemove(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	principal, _ := s.findAuthIdentity("alice")
	if _, ok := findDelegate(principal, "claude@claude.ai"); ok {
		t.Fatal("delegate was not removed")
	}
	if len(audit.events) != 1 || audit.events[0].Details["error"] != "refresh store unavailable" {
		t.Fatalf("audit events=%+v", audit.events)
	}
}

func TestRecordDelegateClientAuthKeepsStrongestVerification(t *testing.T) {
	s, _, audit := newPermissionsTestServer(t)
	if err := s.recordDelegateClientAuth("alice", "claude@claude.ai", AuthPrivateKeyJWT); err != nil {
		t.Fatal(err)
	}
	if err := s.recordDelegateClientAuth("alice", "claude@claude.ai", AuthCIMD); err != nil {
		t.Fatal(err)
	}
	principal, _ := s.findAuthIdentity("alice")
	delegate, _ := findDelegate(principal, "claude@claude.ai")
	if delegate.ClientAuth != string(AuthPrivateKeyJWT) || clientAuthLabel(ClientAuthLevel(delegate.ClientAuth)) != "JWKS" {
		t.Fatalf("client auth=%q", delegate.ClientAuth)
	}
	if len(audit.events) != 1 || audit.events[0].Type != "delegate.client_auth" {
		t.Fatalf("audit events=%+v", audit.events)
	}
}
