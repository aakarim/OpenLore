package openlore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type failingDelegateClientAuthRecorder struct{}

func (failingDelegateClientAuthRecorder) recordDelegateClientAuth(string, string, ClientAuthLevel) error {
	return errors.New("lore.json is read-only")
}

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
	if resp.ExpiresIn < 3590 || resp.ExpiresIn > 3600 {
		t.Fatalf("expires_in = %d, want approximately one hour", resp.ExpiresIn)
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

func TestTokenEndpoint_ClientAuthRecordingFailureDoesNotBlockToken(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	s.tokens.delegateAuth = failingDelegateClientAuthRecorder{}
	code := s.authCodes.Issue(authCode{Subject: "alice", Actor: "claude@claude.ai", Scope: ScopeFull})

	rec, resp := postForm(t, s.tokens, url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
	})
	if rec.Code != http.StatusOK || resp.AccessToken == "" {
		t.Fatalf("status=%d response=%+v body=%s", rec.Code, resp, rec.Body.String())
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

func TestTokenEndpoint_RefreshRetryReturnsSameSuccessor(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	audit := &captureRefreshAudit{}
	s.tokens.audit = audit
	code, _ := s.IssueAuthCode("alice", ScopeFull)
	_, first := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}})

	// First refresh succeeds (rotates).
	_, second := postForm(t, s.tokens, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}})
	// An immediate retry succeeds with the already-issued successor rather than
	// revoking the chain or creating another branch.
	rec, retry := postForm(t, s.tokens, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}})
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if retry.RefreshToken != second.RefreshToken {
		t.Fatalf("retry refresh token = %q, want original successor %q", retry.RefreshToken, second.RefreshToken)
	}
	if len(audit.events) != 2 || audit.events[0].Details["outcome"] != "success" || audit.events[1].Details["outcome"] != "retry" {
		t.Fatalf("refresh audit events = %+v", audit.events)
	}
}

func TestTokenEndpoint_ConcurrentRefreshReturnsUsableSuccessor(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	code, _ := s.IssueAuthCode("alice", ScopeFull)
	_, first := postForm(t, s.tokens, url.Values{"grant_type": {"authorization_code"}, "code": {code}})

	type result struct {
		status int
		body   string
		token  tokenResponse
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}}
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ready.Done()
			<-start
			rec := httptest.NewRecorder()
			s.tokens.ServeHTTP(rec, req)
			var token tokenResponse
			err := json.Unmarshal(rec.Body.Bytes(), &token)
			results <- result{status: rec.Code, body: rec.Body.String(), token: token, err: err}
		}()
	}
	ready.Wait()
	close(start)

	firstResult, secondResult := <-results, <-results
	for i, got := range []result{firstResult, secondResult} {
		if got.status != http.StatusOK || got.err != nil {
			t.Fatalf("concurrent refresh %d: status=%d err=%v body=%s", i, got.status, got.err, got.body)
		}
	}
	if firstResult.token.RefreshToken != secondResult.token.RefreshToken {
		t.Fatalf("concurrent successors differ: %q != %q", firstResult.token.RefreshToken, secondResult.token.RefreshToken)
	}

	// The shared successor is active, not merely repeated in both responses.
	rec, next := postForm(t, s.tokens, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {firstResult.token.RefreshToken},
	})
	if rec.Code != http.StatusOK || next.RefreshToken == firstResult.token.RefreshToken {
		t.Fatalf("successor was not usable: status=%d response=%+v body=%s", rec.Code, next, rec.Body.String())
	}
}

func TestTokenEndpoint_RefreshAuditUsesCurrentClientAuth(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	const clientID = "https://chatgpt.com/oauth/client.json"
	s.tokens.cimd = &fixedCIMDResolver{client: &CIMDClient{ClientID: clientID}}
	s.tokens.clientAuth = fixedClientAuthenticator{level: AuthPrivateKeyJWTMTLS}
	audit := &captureRefreshAudit{}
	s.tokens.audit = audit
	if err := s.refreshStore.Save(RefreshToken{
		Token: "refresh", Subject: "alice", ClientID: clientID,
		ClientAuth: AuthCIMD, Scope: ScopeFull, ChainID: "chain",
	}); err != nil {
		t.Fatal(err)
	}

	rec, _ := postForm(t, s.tokens, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"refresh"},
		"client_id":     {clientID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(audit.events) != 1 || audit.events[0].Attribution.ClientAuth != AuthPrivateKeyJWTMTLS {
		t.Fatalf("refresh audit events = %+v", audit.events)
	}
}

type fixedClientAuthenticator struct{ level ClientAuthLevel }

func (a fixedClientAuthenticator) Authenticate(*http.Request, *CIMDClient) (ClientAuthLevel, error) {
	return a.level, nil
}

type captureRefreshAudit struct{ events []AuditEvent }

func (a *captureRefreshAudit) Record(_ context.Context, event AuditEvent) error {
	a.events = append(a.events, event)
	return nil
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
