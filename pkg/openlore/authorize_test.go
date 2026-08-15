package openlore

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
)

// pkcePair returns a (verifier, S256 challenge) pair for tests.
func pkcePair() (verifier, challenge string) {
	verifier = "test-verifier-abc123abc123abc123abc123abc123abc123"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// authzIDRe extracts the hidden authz request id from the rendered choice page.
var authzIDRe = regexp.MustCompile(`name="authz" value="([^"]+)"`)

// runAuthorize drives GET /authorize and returns the authz request id extracted
// from the rendered public-vs-login choice page.
func runAuthorize(t *testing.T, s *Server, q url.Values) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.authorizeHandler(rec, req)
	if rec.Code != http.StatusOK {
		return rec, ""
	}
	m := authzIDRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		return rec, ""
	}
	return rec, m[1]
}

func TestAuthorize_RendersChoicePageWithRequestID(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	_, challenge := pkcePair()

	rec, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"client_id":             {"obsidian"},
		"redirect_uri":          {"http://127.0.0.1:52123/callback"},
		"state":                 {"xyz"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The public-access form is always present (login works for everyone, §8.4).
	if !strings.Contains(body, `action="`+authorizePublicPath+`"`) {
		t.Errorf("choice page missing public-access form; body=%s", body)
	}
	if authz == "" {
		t.Fatal("no authz request id on choice page")
	}
}

func TestAuthorize_AcceptsRootSlashResourceCanonicalization(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	_, challenge := pkcePair()

	rec, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {"http://127.0.0.1:52123/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {s.config.Tokens.Audience + "/"},
	})
	if rec.Code != http.StatusOK || authz == "" {
		t.Fatalf("status = %d, authz = %q; want accepted canonical resource; body=%s", rec.Code, authz, rec.Body.String())
	}
}

func TestAuthorize_RejectsDifferentResourcePath(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	_, challenge := pkcePair()

	rec, _ := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {"http://127.0.0.1:52123/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {s.config.Tokens.Audience + "/other"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a different resource path", rec.Code)
	}
}

// TestAuthorize_PublicChoiceMintsAnonymousToken exercises the "continue with
// public access" button: POST /authorize/public → redirect with code → token
// exchange yields an anonymous, read-only token (§8.4).
func TestAuthorize_PublicChoiceMintsAnonymousToken(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	verifier, challenge := pkcePair()
	redirectURI := "http://127.0.0.1:52123/callback"

	_, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {"pub"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})
	if authz == "" {
		t.Fatal("no authz id")
	}

	form := url.Values{"authz": {authz}}
	req := httptest.NewRequest(http.MethodPost, authorizePublicPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.authorizePublicHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("public choice status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.HasPrefix(rec.Header().Get("Location"), redirectURI) {
		t.Fatalf("redirect = %q, want prefix %q", rec.Header().Get("Location"), redirectURI)
	}
	if u.Query().Get("state") != "pub" {
		t.Errorf("state = %q, want pub", u.Query().Get("state"))
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}

	_, resp := postForm(t, s.tokens, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	})
	claims, err := s.issuer.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != anonymousSubject {
		t.Errorf("sub = %q, want %q", claims.Subject, anonymousSubject)
	}
	id, err := s.resolveClaims(claims)
	if err != nil {
		t.Fatalf("resolveClaims: %v", err)
	}
	if id.IdentityName != "guest" || id.Principal.IdentityName != "guest" {
		t.Errorf("public token resolved to %+v, want guest principal", id)
	}
}

func TestAuthorize_RejectsMissingPKCE(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	req := httptest.NewRequest(http.MethodGet, "/authorize?"+url.Values{
		"response_type": {"code"},
		"redirect_uri":  {"http://127.0.0.1:9000/cb"},
	}.Encode(), nil)
	rec := httptest.NewRecorder()
	s.authorizeHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing code_challenge)", rec.Code)
	}
}

func TestAuthorize_RejectsBadParams(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	cases := []url.Values{
		{"response_type": {"token"}, "redirect_uri": {"http://127.0.0.1:1/cb"}}, // wrong response_type
		{"response_type": {"code"}}, // missing redirect_uri
		{"response_type": {"code"}, "redirect_uri": {"https://evil.example/cb"}}, // remote origin
	}
	for i, q := range cases {
		req := httptest.NewRequest(http.MethodGet, "/authorize?"+q.Encode(), nil)
		rec := httptest.NewRecorder()
		s.authorizeHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400", i, rec.Code)
		}
	}
}

func TestAuthorize_LoopbackAndCustomSchemeAllowed(t *testing.T) {
	for _, uri := range []string{
		"http://127.0.0.1:9000/cb",
		"http://localhost:9000/cb",
		"obsidian://openlore/callback",
	} {
		if !validNativeRedirectURI(uri) {
			t.Errorf("validNativeRedirectURI(%q) = false, want true", uri)
		}
	}
	for _, uri := range []string{
		"",
		"https://evil.example/cb",
		"http://192.168.1.5/cb",
	} {
		if validNativeRedirectURI(uri) {
			t.Errorf("validNativeRedirectURI(%q) = true, want false", uri)
		}
	}
}

// TestAuthorizeCodeFlow_EndToEnd exercises the full PKCE authorization-code flow:
// /authorize → CompleteAuthorize (as the passkey finish hook would) → token
// exchange with a code_verifier.
func TestAuthorizeCodeFlow_EndToEnd(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	verifier, challenge := pkcePair()
	redirectURI := "http://127.0.0.1:52123/callback"

	_, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {"xyz"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})
	if authz == "" {
		t.Fatal("no authz id")
	}

	// The passkey finish hook resolves the caller to identity "alice".
	redirectURL, ok := s.CompleteAuthorize(authz, "alice")
	if !ok {
		t.Fatal("CompleteAuthorize returned false")
	}
	u, _ := url.Parse(redirectURL)
	if !strings.HasPrefix(redirectURL, redirectURI) {
		t.Fatalf("redirect = %q, want prefix %q", redirectURL, redirectURI)
	}
	if u.Query().Get("state") != "xyz" {
		t.Errorf("state = %q, want xyz", u.Query().Get("state"))
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}

	// Exchange the code with the correct verifier + redirect_uri.
	rec, resp := postForm(t, s.tokens, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("token exchange status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	claims, err := s.issuer.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "alice" {
		t.Errorf("sub = %q, want alice", claims.Subject)
	}
}

func TestAuthorizeCodeFlow_WrongVerifierRejected(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	_, challenge := pkcePair()
	redirectURI := "http://127.0.0.1:52123/callback"

	_, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})
	redirectURL, _ := s.CompleteAuthorize(authz, "alice")
	u, _ := url.Parse(redirectURL)
	code := u.Query().Get("code")

	// Wrong verifier → rejected.
	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {"the-wrong-verifier-000000000000000000000000000"},
		"redirect_uri":  {redirectURI},
	})
	if rec.Code == http.StatusOK {
		t.Fatal("expected rejection for wrong PKCE verifier")
	}
}

func TestAuthorizeCodeFlow_MissingVerifierRejected(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	_, challenge := pkcePair()
	redirectURI := "http://127.0.0.1:52123/callback"

	_, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})
	redirectURL, _ := s.CompleteAuthorize(authz, "alice")
	u, _ := url.Parse(redirectURL)
	code := u.Query().Get("code")

	// No verifier on a PKCE-bound code → rejected.
	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	})
	if rec.Code == http.StatusOK {
		t.Fatal("expected rejection when code_verifier is missing")
	}
}

func TestAuthorizeCodeFlow_RedirectMismatchRejected(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	verifier, challenge := pkcePair()
	redirectURI := "http://127.0.0.1:52123/callback"

	_, authz := runAuthorize(t, s, url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	})
	redirectURL, _ := s.CompleteAuthorize(authz, "alice")
	u, _ := url.Parse(redirectURL)
	code := u.Query().Get("code")

	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {"http://127.0.0.1:52123/different"},
	})
	if rec.Code == http.StatusOK {
		t.Fatal("expected rejection for redirect_uri mismatch")
	}
}

func TestCompleteAuthorize_UnknownRequestFails(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	if _, ok := s.CompleteAuthorize("nonexistent", "alice"); ok {
		t.Fatal("expected CompleteAuthorize to fail for unknown request id")
	}
}

// TestMatchResolvesToIdentity verifies that a token whose `sub` is an alias
// listed in an identity's Match predicates resolves to that identity — the
// rules-on-identity model (matches live on the identity they select).
func TestMatchResolvesToIdentity(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	// Attach an alias-sub match to alice, as a WIF-style rule would.
	for i := range s.auth.Identities {
		if s.auth.Identities[i].Name == "alice" {
			s.auth.Identities[i].Match = []config.IdentityMatch{{Sub: "alias-for-alice"}}
		}
	}

	id, err := s.resolveClaims(Claims{Subject: "alias-for-alice", Scope: ScopeFull})
	if err != nil {
		t.Fatalf("resolveClaims: %v", err)
	}
	if id.IdentityName != "alice" {
		t.Fatalf("IdentityName = %q, want alice (via Match alias)", id.IdentityName)
	}
	if id.Principal.IdentityName != "alice" {
		t.Errorf("principal = %+v, want alice", id.Principal)
	}
}

func TestIdentityExists(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	if !s.IdentityExists("alice") {
		t.Error("IdentityExists(alice) = false, want true")
	}
	if s.IdentityExists("nobody") {
		t.Error("IdentityExists(nobody) = true, want false")
	}
}
