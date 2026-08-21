package openlore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const delegationGrantType = "urn:openlore:oauth:grant-type:delegation"

// authCodeStore holds short-lived OAuth authorization codes issued by the login
// ceremony (passkey in Phase 2) and consumed once at the token endpoint.
type authCodeStore struct {
	mu    sync.Mutex
	codes map[string]authCode
	ttl   time.Duration
}

type authCode struct {
	Subject string
	Actor   string
	Scope   string
	Expires time.Time

	// ClientID binds the code to the DCR client that requested it (empty for
	// native/debug codes). When set, the token request's client_id must match.
	ClientID string
	Client   OAuthClient
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
	wif          wifExchanger
	delegation   delegationExchanger
	cimd         CIMDResolver
	clientAuth   ClientAuthenticator
	audit        AuditLog
	delegateAuth delegateClientAuthRecorder
	logger       *slog.Logger
}

type delegateClientAuthRecorder interface {
	recordDelegateClientAuth(principal, actor string, level ClientAuthLevel) error
}

// wifExchanger verifies an external IdP assertion and resolves it to the
// subject/scope/TTL of the OpenLore token to mint. The Server implements it.
type wifExchanger interface {
	ExchangeAssertion(ctx context.Context, assertion string) (sub, scope string, ttl time.Duration, err error)
}

type delegationExchanger interface {
	ExchangeDelegation(context.Context, string, string) (Attribution, string, time.Duration, error)
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
	case delegationGrantType:
		t.handleTokenExchange(w, r)
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
	clientAuth := baselineClientAuth(c.Client)
	if c.Client.CIMD != nil {
		if t.clientAuth == nil {
			oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication unavailable")
			return
		}
		var err error
		clientAuth, err = t.clientAuth.Authenticate(r, c.Client.CIMD)
		if err != nil {
			oauthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
			return
		}
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
	if c.Actor != "" && t.delegateAuth != nil {
		if err := t.delegateAuth.recordDelegateClientAuth(c.Subject, c.Actor, clientAuth); err != nil {
			logger := t.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("failed to record delegated client authentication", "principal", c.Subject, "actor", c.Actor, "error", err)
		}
	}
	t.issue(w, Attribution{Principal: c.Subject, Actor: c.Actor, ClientAuth: clientAuth}, c.Scope, randomToken(), c.ClientID)
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
	t.issueAccessOnly(w, Attribution{Principal: sub}, scope, ttl)
}

func (t *tokenEndpoint) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	if t.delegation == nil {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "token exchange is not enabled")
		return
	}
	actorToken, principal := r.Form.Get("actor_token"), r.Form.Get("act_for")
	if actorToken == "" || principal == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "actor_token and act_for are required")
		return
	}
	attribution, scope, ttl, err := t.delegation.ExchangeDelegation(r.Context(), actorToken, principal)
	if err != nil {
		oauthError(w, http.StatusForbidden, "invalid_grant", "delegation is not authorized")
		return
	}
	t.issueAccessOnly(w, attribution, scope, ttl)
}

// issueAccessOnly mints an access token with an explicit TTL and no refresh
// token (used by the WIF exchange).
func (t *tokenEndpoint) issueAccessOnly(w http.ResponseWriter, attribution Attribution, scope string, ttl time.Duration) {
	if scope == "" {
		scope = ScopeFull
	}
	access, exp, err := t.issuer.Mint(attribution, scope, ttl)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "mint failed")
		return
	}
	writeTokenResponse(w, access, "", scope, exp)
}

func (t *tokenEndpoint) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	presented := r.Form.Get("refresh_token")
	if presented == "" {
		t.recordRefresh(r.Context(), "missing_token", RefreshToken{}, nil)
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token required")
		return
	}
	old, ok, err := t.refresh.Lookup(presented)
	if err != nil {
		t.recordRefresh(r.Context(), "lookup_failed", RefreshToken{}, err)
		oauthError(w, http.StatusInternalServerError, "server_error", "lookup failed")
		return
	}
	if !ok {
		t.recordRefresh(r.Context(), "unknown_token", RefreshToken{}, nil)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	clientAuth, err := t.authenticateBoundClient(r, old)
	if err != nil {
		t.recordRefresh(r.Context(), "invalid_client", old, err)
		oauthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	// Subsequent audit events describe the authentication achieved by this
	// request, which may be stronger than the level stored on the old token.
	old.ClientAuth = clientAuth
	// Rotate: mint a new refresh token in the same chain and consume the old.
	newRefresh := RefreshToken{
		Token:      randomToken(),
		Subject:    old.Subject,
		Actor:      old.Actor,
		ClientID:   old.ClientID,
		ClientAuth: clientAuth,
		Scope:      old.Scope,
		ChainID:    old.ChainID,
		ExpiresAt:  time.Now().Add(t.refreshTTL),
	}
	rotation, err := t.refresh.Rotate(presented, newRefresh)
	if err != nil {
		// Reuse or invalid → deny (chain already revoked on reuse).
		t.recordRefresh(r.Context(), "rotation_rejected", old, err)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token rejected")
		return
	}
	if !t.issueRotated(w, Attribution{Principal: old.Subject, Actor: old.Actor, ClientAuth: clientAuth}, old.Scope, rotation.Token.Token) {
		t.recordRefresh(r.Context(), "mint_failed", old, nil)
		return
	}
	outcome := "success"
	if rotation.Retried {
		outcome = "retry"
	}
	t.recordRefresh(r.Context(), outcome, old, nil)
}

func (t *tokenEndpoint) recordRefresh(ctx context.Context, outcome string, token RefreshToken, refreshErr error) {
	details := map[string]any{"outcome": outcome}
	if token.ClientID != "" {
		details["client_id"] = token.ClientID
	}
	if token.ChainID != "" {
		details["chain_id"] = token.ChainID
	}
	if refreshErr != nil {
		details["error"] = refreshErr.Error()
	}
	attribution := Attribution{Principal: token.Subject, Actor: token.Actor, ClientAuth: token.ClientAuth}
	// Only durable-audit attempts tied to a known chain. The endpoint is public;
	// persisting arbitrary missing/unknown-token probes would allow log spam.
	if t.audit != nil && token.ChainID != "" {
		_ = t.audit.Record(ctx, AuditEvent{Type: "token.refresh", Attribution: attribution, Details: details})
	}
	logger := t.logger
	if logger == nil {
		logger = slog.Default()
	}
	args := []any{"outcome", outcome, "principal", token.Subject, "actor", token.Actor, "client_id", token.ClientID}
	if refreshErr != nil {
		args = append(args, "error", refreshErr)
	}
	switch outcome {
	case "success", "retry":
		logger.InfoContext(ctx, "oauth token refresh", args...)
	case "lookup_failed", "mint_failed":
		logger.ErrorContext(ctx, "oauth token refresh", args...)
	default:
		logger.WarnContext(ctx, "oauth token refresh", args...)
	}
}

// issue mints access+refresh for a fresh login (new chain).
func (t *tokenEndpoint) issue(w http.ResponseWriter, attribution Attribution, scope, chainID, clientID string) {
	if scope == "" {
		scope = ScopeFull
	}
	access, exp, err := t.issuer.Mint(attribution, scope, t.accessTTL)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "mint failed")
		return
	}
	refreshTok := RefreshToken{
		Token:      randomToken(),
		Subject:    attribution.Principal,
		Actor:      attribution.Actor,
		ClientID:   clientID,
		ClientAuth: attribution.ClientAuth,
		Scope:      scope,
		ChainID:    chainID,
		ExpiresAt:  time.Now().Add(t.refreshTTL),
	}
	if err := t.refresh.Save(refreshTok); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "refresh save failed")
		return
	}
	writeTokenResponse(w, access, refreshTok.Token, scope, exp)
}

// issueRotated mints a new access token alongside an already-persisted rotated
// refresh token.
func (t *tokenEndpoint) issueRotated(w http.ResponseWriter, attribution Attribution, scope, refreshTok string) bool {
	if scope == "" {
		scope = ScopeFull
	}
	access, exp, err := t.issuer.Mint(attribution, scope, t.accessTTL)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "mint failed")
		return false
	}
	writeTokenResponse(w, access, refreshTok, scope, exp)
	return true
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

// revoke implements RFC 7009 for refresh tokens. The response is deliberately
// 200 for unknown tokens so callers cannot probe the refresh-token store.
func (t *tokenEndpoint) revoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	presented := r.Form.Get("token")
	if presented != "" {
		if token, ok, err := t.refresh.Lookup(presented); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "lookup failed")
			return
		} else if ok {
			clientAuth, authErr := t.authenticateBoundClient(r, token)
			if authErr != nil {
				oauthError(w, http.StatusUnauthorized, "invalid_client", authErr.Error())
				return
			}
			if err := t.refresh.RevokeChain(token.ChainID); err != nil {
				oauthError(w, http.StatusInternalServerError, "server_error", "revoke failed")
				return
			}
			if t.audit != nil {
				_ = t.audit.Record(r.Context(), AuditEvent{Type: "token.revoke", Attribution: Attribution{
					Principal: token.Subject, Actor: token.Actor, ClientAuth: clientAuth,
				}, Details: map[string]any{"chain_id": token.ChainID}})
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (t *tokenEndpoint) authenticateBoundClient(r *http.Request, token RefreshToken) (ClientAuthLevel, error) {
	if token.ClientID == "" {
		return token.ClientAuth, nil
	}
	if r.Form.Get("client_id") != token.ClientID {
		return "", errors.New("client_id mismatch")
	}
	if strings.HasPrefix(token.ClientID, "https://") {
		if t.cimd == nil || t.clientAuth == nil {
			return "", errors.New("client authentication unavailable")
		}
		client, err := t.cimd.Resolve(r.Context(), token.ClientID)
		if err != nil {
			return "", errors.New("client metadata unavailable")
		}
		return t.clientAuth.Authenticate(r, client)
	}
	return token.ClientAuth, nil
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
