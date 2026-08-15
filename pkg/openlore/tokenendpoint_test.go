package openlore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postForm(t *testing.T, h http.Handler, form url.Values) (*httptest.ResponseRecorder, tokenResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp tokenResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode token response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, resp
}

func TestTokenEndpoint_AuthorizationCodeGrant(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")

	code, ok := s.IssueAuthCode("alice", ScopeFull)
	if !ok {
		t.Fatal("IssueAuthCode returned false")
	}

	rec, resp := postForm(t, s.tokens, url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		t.Fatalf("missing tokens in response: %+v", resp)
	}
	// The minted access token verifies and carries the right subject.
	claims, err := s.issuer.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("Verify minted token: %v", err)
	}
	if claims.Subject != "alice" {
		t.Errorf("sub = %q, want alice", claims.Subject)
	}
}

func TestTokenEndpoint_AcceptsEquivalentRootResource(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	code := s.authCodes.Issue(authCode{
		Subject:  "alice",
		Scope:    ScopeFull,
		Resource: s.config.Tokens.Audience + "/",
	})

	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
		"resource":   {s.config.Tokens.Audience},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenEndpoint_CodeIsSingleUse(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	code, _ := s.IssueAuthCode("alice", ScopeFull)

	if rec, _ := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}}); rec.Code != http.StatusOK {
		t.Fatalf("first exchange status = %d, want 200", rec.Code)
	}
	// Re-using the same code fails.
	if rec, _ := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}}); rec.Code == http.StatusOK {
		t.Fatalf("reused code should fail, got 200")
	}
}

func TestTokenEndpoint_RefreshRotation(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	code, _ := s.IssueAuthCode("alice", ScopeFull)
	_, first := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}})

	rec, second := postForm(t, s.tokens, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {first.RefreshToken},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh token must rotate")
	}
	if second.AccessToken == "" {
		t.Fatalf("refresh must yield a new access token")
	}
}

func TestTokenEndpoint_RefreshReuseRejected(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	code, _ := s.IssueAuthCode("alice", ScopeFull)
	_, first := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}})

	// First refresh succeeds (rotates).
	postForm(t, s.tokens, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}})
	// Re-using the now-consumed refresh token is rejected.
	rec, _ := postForm(t, s.tokens, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}})
	if rec.Code == http.StatusOK {
		t.Fatalf("reused refresh token should be rejected, got 200")
	}
}

// With no OIDC issuers configured, WIF is disabled and the jwt-bearer grant is
// rejected as unsupported (400), not accepted.
func TestTokenEndpoint_JWTBearerDisabled(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {"external.idp.jwt"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("jwt-bearer (WIF disabled) status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenEndpoint_JWKSHandler(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	rec := httptest.NewRecorder()
	jwksHandler(s.issuer).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks status = %d, want 200", rec.Code)
	}
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Keys) != 1 {
		t.Fatalf("unexpected JWKS: err=%v body=%s", err, rec.Body.String())
	}
}
