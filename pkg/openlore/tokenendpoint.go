package openlore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// authCodeStore holds short-lived OAuth authorization codes issued by the login
// ceremony (passkey in Phase 2) and consumed once at the token endpoint.
type authCodeStore struct {
	mu    sync.Mutex
	codes map[string]authCode
	ttl   time.Duration
}

type authCode struct {
	Subject string
	Scope   string
	Expires time.Time

	// ClientID binds the code to the DCR client that requested it (empty for
	// native/debug codes). When set, the token request's client_id must match.
	ClientID string
	// Resource is the RFC 8707 resource indicator carried from /authorize; when
	// set, a resource on the token request must match it.
	Resource string

	// PKCE + redirect binding for the OAuth authorization-code flow (RFC 7636).
	// Empty RedirectURI/CodeChallenge means a non-PKCE code (debug/test mint via
	// IssueAuthCode); such codes skip the PKCE/redirect checks at the token
	// endpoint.
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
}

func newAuthCodeStore() *authCodeStore {
	return &authCodeStore{codes: map[string]authCode{}, ttl: 60 * time.Second}
}

// Issue mints a single-use authorization code from a prepared authCode, setting
// its expiry. The caller supplies Subject/Scope and any PKCE/redirect binding.
func (a *authCodeStore) Issue(c authCode) string {
	code := randomToken()
	c.Expires = time.Now().Add(a.ttl)
	a.mu.Lock()
	a.codes[code] = c
	a.mu.Unlock()
	return code
}

// Consume validates and removes a code, returning its subject/scope.
func (a *authCodeStore) Consume(code string) (authCode, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.codes[code]
	if !ok {
		return authCode{}, false
	}
	delete(a.codes, code)
	if c.Expires.Before(time.Now()) {
		return authCode{}, false
	}
	return c, true
}

// IssueAuthCode mints a single-use authorization code for the identity (`sub`),
// to be exchanged for tokens at /oauth/token. Returns false if token auth is
// disabled. The passkey login-success hook (Phase 2) calls this; tests use it
// to drive the authorization_code grant.
func (s *Server) IssueAuthCode(sub, scope string) (string, bool) {
	if s.authCodes == nil {
		return "", false
	}
	if scope == "" {
		scope = ScopeFull
	}
	return s.authCodes.Issue(authCode{Subject: sub, Scope: scope}), true
}

// tokenEndpoint serves POST /oauth/token (authorization_code + refresh_token
// grants) and GET the JWKS. It is the single mint step both human login and
// (later) WIF exchange feed into (docs/mcp-bearer-auth.md §8.1).
type tokenEndpoint struct {
	issuer     Issuer
	refresh    RefreshTokenStore
	codes      *authCodeStore
	accessTTL  time.Duration
	refreshTTL time.Duration
	audience   string
	// wif performs the jwt-bearer (WIF) exchange: verify an external IdP
	// assertion, match it to a rule, and return the subject/scope/TTL to mint.
	// nil when no OIDC issuers are configured (grant stays unsupported).
	wif wifExchanger
}

// wifExchanger verifies an external IdP assertion and resolves it to the
// subject/scope/TTL of the OpenLore token to mint. The Server implements it.
type wifExchanger interface {
	ExchangeAssertion(ctx context.Context, assertion string) (sub, scope string, ttl time.Duration, err error)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// ServeHTTP dispatches on grant_type.
func (t *tokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		t.handleAuthorizationCode(w, r)
	case "refresh_token":
		t.handleRefreshToken(w, r)
	case "urn:ietf:params:oauth:grant-type:jwt-bearer":
		t.handleJWTBearer(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func (t *tokenEndpoint) handleAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.Form.Get("code")
	if code == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code required")
		return
	}
	c, ok := t.codes.Consume(code)
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired code")
		return
	}
	// Bind the code to the registered client that requested it (client
	// substitution protection). Native/debug codes carry no ClientID and skip it.
	if c.ClientID != "" && r.Form.Get("client_id") != c.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	// A resource indicator on the token request must match the one bound at
	// /authorize (RFC 8707).
	if reqResource := r.Form.Get("resource"); reqResource != "" && c.Resource != "" && !sameResourceIdentifier(reqResource, c.Resource) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource mismatch")
		return
	}
	// Enforce PKCE + redirect_uri binding for codes minted through /authorize
	// (RFC 7636). Non-PKCE codes (debug/test mint) carry no binding and skip it.
	if c.CodeChallenge != "" {
		if r.Form.Get("redirect_uri") != c.RedirectURI {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
			return
		}
		verifier := r.Form.Get("code_verifier")
		if verifier == "" {
			oauthError(w, http.StatusBadRequest, "invalid_request", "code_verifier required")
			return
		}
		if !verifyPKCE(c.CodeChallengeMethod, verifier, c.CodeChallenge) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
	}
	t.issue(w, c.Subject, c.Scope, randomToken())
}

// verifyPKCE checks a code_verifier against a stored code_challenge per RFC 7636.
// method "S256" (default) compares base64url(sha256(verifier)); "plain" compares
// the verifier directly. Any other method fails closed.
func verifyPKCE(method, verifier, challenge string) bool {
	switch method {
	case "", "S256":
		sum := sha256.Sum256([]byte(verifier))
		return subtle.ConstantTimeCompare(
			[]byte(base64.RawURLEncoding.EncodeToString(sum[:])),
			[]byte(challenge),
		) == 1
	case "plain":
		return subtle.ConstantTimeCompare([]byte(verifier), []byte(challenge)) == 1
	default:
		return false
	}
}

// handleJWTBearer implements the RFC 7523 jwt-bearer grant (WIF): it verifies an
// external IdP assertion, matches its claims to a rule, and mints a short-lived
// OpenLore access token for the resolved identity. It issues NO refresh token —
// workloads re-exchange a fresh assertion, keeping WIF free of long-lived
// credentials. See docs/mcp-bearer-auth.md §8.1 and workload-identity-federation.md.
func (t *tokenEndpoint) handleJWTBearer(w http.ResponseWriter, r *http.Request) {
	if t.wif == nil {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"jwt-bearer (workload identity federation) is not enabled on this instance")
		return
	}
	assertion := r.Form.Get("assertion")
	if assertion == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "assertion required")
		return
	}
	sub, scope, ttl, err := t.wif.ExchangeAssertion(r.Context(), assertion)
	if err != nil {
		switch {
		case errors.Is(err, ErrWIFDisabled):
			oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
				"jwt-bearer (workload identity federation) is not enabled on this instance")
		case errors.Is(err, ErrUnknownIdentity):
			oauthError(w, http.StatusForbidden, "invalid_grant", "assertion matched no identity")
		case errors.Is(err, ErrInvalidScope):
			oauthError(w, http.StatusBadRequest, "invalid_scope", "matched rule has no valid scope")
		default:
			// Signature / issuer / audience / expiry failure.
			oauthError(w, http.StatusBadRequest, "invalid_grant", "assertion verification failed")
		}
		return
	}
	t.issueAccessOnly(w, sub, scope, ttl)
}

// issueAccessOnly mints an access token with an explicit TTL and no refresh
// token (used by the WIF exchange).
func (t *tokenEndpoint) issueAccessOnly(w http.ResponseWriter, sub, scope string, ttl time.Duration) {
	if scope == "" {
		scope = ScopeFull
	}
	access, exp, err := t.issuer.Mint(sub, scope, ttl)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "mint failed")
		return
	}
	writeTokenResponse(w, access, "", scope, exp)
}

func (t *tokenEndpoint) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	presented := r.Form.Get("refresh_token")
	if presented == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token required")
		return
	}
	old, ok, err := t.refresh.Lookup(presented)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "lookup failed")
		return
	}
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	// Rotate: mint a new refresh token in the same chain and consume the old.
	newRefresh := RefreshToken{
		Token:     randomToken(),
		Subject:   old.Subject,
		Scope:     old.Scope,
		ChainID:   old.ChainID,
		ExpiresAt: time.Now().Add(t.refreshTTL),
	}
	if err := t.refresh.Rotate(presented, newRefresh); err != nil {
		// Reuse or invalid → deny (chain already revoked on reuse).
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token rejected")
		return
	}
	t.issueRotated(w, old.Subject, old.Scope, newRefresh.Token)
}

// issue mints access+refresh for a fresh login (new chain).
func (t *tokenEndpoint) issue(w http.ResponseWriter, sub, scope, chainID string) {
	if scope == "" {
		scope = ScopeFull
	}
	access, exp, err := t.issuer.Mint(sub, scope, t.accessTTL)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "mint failed")
		return
	}
	refreshTok := RefreshToken{
		Token:     randomToken(),
		Subject:   sub,
		Scope:     scope,
		ChainID:   chainID,
		ExpiresAt: time.Now().Add(t.refreshTTL),
	}
	if err := t.refresh.Save(refreshTok); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "refresh save failed")
		return
	}
	writeTokenResponse(w, access, refreshTok.Token, scope, exp)
}

// issueRotated mints a new access token alongside an already-persisted rotated
// refresh token.
func (t *tokenEndpoint) issueRotated(w http.ResponseWriter, sub, scope, refreshTok string) {
	if scope == "" {
		scope = ScopeFull
	}
	access, exp, err := t.issuer.Mint(sub, scope, t.accessTTL)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "mint failed")
		return
	}
	writeTokenResponse(w, access, refreshTok, scope, exp)
}

func writeTokenResponse(w http.ResponseWriter, access, refresh, scope string, exp time.Time) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(tokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int(time.Until(exp).Seconds()),
		RefreshToken: refresh,
		Scope:        scope,
	})
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

// jwksHandler serves the issuer's public JWKS.
func jwksHandler(issuer Issuer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := issuer.JWKS()
		if err != nil {
			http.Error(w, "jwks error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("openlore: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
