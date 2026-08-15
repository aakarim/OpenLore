package openlore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

// anonymousSubject is the reserved `sub` for a public token — one that resolves
// to the same anonymous `default` identity a tokenless caller gets. It lets an
// OAuth-only client (Claude) complete the flow without logging in (§8.4).
const anonymousSubject = "anonymous"

// ErrUnknownIdentity signals that a token's claims matched no identity and the
// posture is `unknown_identity: deny` — the caller must be rejected (403).
var ErrUnknownIdentity = errors.New("unknown identity")

// ErrInvalidScope signals that a matched WIF rule carries a scope OpenLore does
// not recognize (or none) — the exchange is denied (fail-closed, never full).
var ErrInvalidScope = errors.New("invalid scope")

// ErrWIFDisabled signals a jwt-bearer exchange arrived but no OIDC issuers are
// configured, so WIF is not enabled on this instance.
var ErrWIFDisabled = errors.New("workload identity federation is not enabled")

// IdentityStore resolves verified token claims to an Identity. It is the single
// seam that makes "permissions change live" work against either backend: the
// go-openlore default reads lore.json + rules; knowledge-backend supplies a
// SQLite-backed Resolve (docs/mcp-bearer-auth.md §7, §9).
type IdentityStore interface {
	Resolve(ctx context.Context, claims Claims) (Identity, error)
}

// initAuth wires the always-on IdentityStore and, when auth.tokens is
// configured, the token issuer/stores/endpoint that enable bearer auth on the
// MCP + HTTP API. Injected implementations (knowledge-backend) are preserved.
func (s *Server) initAuth() error {
	if s.identityStore == nil {
		s.identityStore = serverIdentityStore{s}
	}
	if s.config.Tokens == nil {
		return nil // token auth disabled → endpoints behave as anonymous (Phase 0)
	}

	tc := s.config.Tokens
	dataDir := s.config.DataDir
	if dataDir == "" {
		dataDir = "."
	}

	if s.issuer == nil {
		iss, err := newESIssuer(tc.Issuer, tc.Audience, filepath.Join(dataDir, "auth", "es256.pem"))
		if err != nil {
			return err
		}
		s.issuer = iss
	}
	if s.refreshStore == nil {
		rs, err := newFileRefreshStore(filepath.Join(dataDir, "auth", "refresh_tokens.json"))
		if err != nil {
			return err
		}
		s.refreshStore = rs
	}
	if s.clientStore == nil {
		cs, err := newFileClientStore(filepath.Join(dataDir, "auth", "clients.json"))
		if err != nil {
			return err
		}
		s.clientStore = cs
	}
	// WIF: when external IdP issuers are configured, build the verifier that
	// makes the jwt-bearer grant live. Left nil (grant stays unsupported) when
	// no issuers are configured. An injected verifier is preserved.
	if s.oidc == nil && len(s.config.OIDCIssuers) > 0 {
		ov, err := newOIDCVerifier(tc.Audience, s.config.OIDCIssuers)
		if err != nil {
			return err
		}
		s.oidc = ov
	}
	s.authCodes = newAuthCodeStore()
	s.authorizeReqs = newAuthorizeStore()
	s.tokens = &tokenEndpoint{
		issuer:     s.issuer,
		refresh:    s.refreshStore,
		codes:      s.authCodes,
		accessTTL:  parseDurationDefault(tc.AccessTTL, 30*time.Minute),
		refreshTTL: parseDurationDefault(tc.RefreshTTL, 720*time.Hour),
		audience:   tc.Audience,
	}
	// The token endpoint's jwt-bearer grant delegates the verify+match+narrow
	// exchange back to the server (which holds the auth config + verifier).
	if s.oidc != nil {
		s.tokens.wif = s
	}
	return nil
}

// parseDurationDefault parses a Go duration string, returning def if empty or
// invalid.
func parseDurationDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// serverIdentityStore is the default IdentityStore backed by the server's auth
// config (rules + identity table).
type serverIdentityStore struct{ s *Server }

func (d serverIdentityStore) Resolve(_ context.Context, claims Claims) (Identity, error) {
	return d.s.resolveClaims(claims)
}

// resolveClaims maps verified claims to an Identity via the rule list, falling
// back to a direct `sub`→identity-name lookup, then to the posture default.
func (s *Server) resolveClaims(claims Claims) (Identity, error) {
	sub := claims.Subject

	// A public/anonymous token resolves to the same read-only guest identity a
	// tokenless caller gets (§8.4).
	if sub == "" || sub == anonymousSubject {
		id := s.anonymousIdentity()
		id.Scopes = []string{claims.Scope}
		id.Principal = AuthenticatedPrincipal{Subject: sub, IdentityName: "guest", Source: "token", Claims: claims.Raw, Scope: claims.Scope}
		return id, nil
	}

	name, matched := s.matchRule(sub)
	if !matched {
		// No explicit rule: a `sub` that names an identity resolves to it.
		if _, ok := s.findAuthIdentity(sub); ok {
			name, matched = sub, true
		}
	}
	if !matched {
		if s.config.UnknownIdentity == "deny" {
			return Identity{}, ErrUnknownIdentity
		}
		// Posture allows unknowns: land in the read-only default lore.
		id := s.anonymousIdentity()
		id.Principal = AuthenticatedPrincipal{Subject: sub, IdentityName: "guest", Source: "token", Claims: claims.Raw, Scope: claims.Scope}
		id.Scopes = []string{claims.Scope}
		return id, nil
	}

	id, ok := s.identityForName(name)
	if !ok {
		// A rule pointed at a nonexistent identity — fail closed.
		return Identity{}, ErrUnknownIdentity
	}
	id.Scopes = []string{claims.Scope}
	id.Principal = AuthenticatedPrincipal{Subject: sub, IdentityName: name, Source: "token", Claims: claims.Raw, Scope: claims.Scope}
	return id, nil
}

// ExchangeAssertion implements the jwt-bearer (WIF) exchange: it verifies an
// external IdP assertion, matches its claims to a WIF rule, and returns the
// subject/scope/TTL for the OpenLore token to mint. The verifier already pinned
// the assertion to a trusted issuer and OUR audience, so cross-service and
// cross-issuer replay are ruled out before any rule is consulted.
func (s *Server) ExchangeAssertion(ctx context.Context, assertion string) (sub, scope string, ttl time.Duration, err error) {
	if s.oidc == nil {
		return "", "", 0, ErrWIFDisabled
	}
	claims, err := s.oidc.Verify(ctx, assertion)
	if err != nil {
		return "", "", 0, err
	}
	name, m, ok := s.matchWIFClaims(claims)
	if !ok {
		return "", "", 0, ErrUnknownIdentity
	}
	// The rule must point at an identity that actually exists — fail closed.
	if _, ok := s.findAuthIdentity(name); !ok {
		return "", "", 0, ErrUnknownIdentity
	}
	// A WIF rule must carry a recognized scope; missing/unknown → deny (never
	// full). This is the fail-closed floor from docs/mcp-bearer-auth.md §5.4.
	if !recognizedScope(m.Scope) {
		return "", "", 0, ErrInvalidScope
	}
	return name, m.Scope, s.wifTTL(m.TTL, claims.Expiry), nil
}

// wifTTL caps a brokered token's lifetime: min(rule ttl, 2× remaining assertion
// lifetime, access TTL), with a 60s floor. A short-lived assertion yields a
// short-lived OpenLore token, so WIF stays "no long-lived credentials."
func (s *Server) wifTTL(ruleTTL string, assertionExpiry time.Time) time.Duration {
	ttl := s.tokens.accessTTL
	if d, err := time.ParseDuration(ruleTTL); err == nil && d > 0 && d < ttl {
		ttl = d
	}
	if !assertionExpiry.IsZero() {
		if remaining := 2 * time.Until(assertionExpiry); remaining > 0 && remaining < ttl {
			ttl = remaining
		}
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	return ttl
}

// matchWIFClaims resolves verified external claims to an identity via the WIF
// match predicates on each identity. Exact-`sub` matches take precedence over
// pattern/claim matches so a specific subject can override a broad rule.
func (s *Server) matchWIFClaims(claims Claims) (string, config.IdentityMatch, bool) {
	// First pass: exact sub wins.
	for _, ident := range s.auth.Identities {
		for _, m := range ident.Match {
			if m.Sub != "" && m.Sub == claims.Subject && wifPredicatesMatch(m, claims) {
				return ident.Name, m, true
			}
		}
	}
	// Second pass: prefix/claim matches (a WIF rule must specify sub_prefix,
	// aud, or claims — an empty predicate never matches, so it can't grab every
	// assertion).
	for _, ident := range s.auth.Identities {
		for _, m := range ident.Match {
			if m.Sub != "" {
				continue // handled above
			}
			if !wifRuleIsSpecific(m) {
				continue
			}
			if wifPredicatesMatch(m, claims) {
				return ident.Name, m, true
			}
		}
	}
	return "", config.IdentityMatch{}, false
}

// wifRuleIsSpecific reports whether a match entry constrains the assertion at
// all. An entry with no sub/sub_prefix/aud/claims is not a valid WIF rule and is
// ignored so it cannot match every token.
func wifRuleIsSpecific(m config.IdentityMatch) bool {
	return m.SubPrefix != "" || m.Aud != "" || len(m.Claims) > 0
}

// wifPredicatesMatch checks the sub_prefix/aud/claims predicates of a match
// entry against verified claims. All specified predicates must hold (AND).
func wifPredicatesMatch(m config.IdentityMatch, claims Claims) bool {
	if m.SubPrefix != "" && !strings.HasPrefix(claims.Subject, m.SubPrefix) {
		return false
	}
	if m.Aud != "" && !audienceContains(claims, m.Aud) {
		return false
	}
	for k, v := range m.Claims {
		if !claimEquals(claims.Raw[k], v) {
			return false
		}
	}
	return true
}

// audienceContains reports whether the assertion's aud claim (string or array)
// contains want. The verifier already pinned aud to OUR audience; a rule's `aud`
// is defense-in-depth pinning.
func audienceContains(claims Claims, want string) bool {
	if claims.Audience == want {
		return true
	}
	switch a := claims.Raw["aud"].(type) {
	case string:
		return a == want
	case []any:
		for _, item := range a {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range a {
			if s == want {
				return true
			}
		}
	}
	return false
}

// claimEquals compares a raw claim value to the expected string. Non-string
// claim values are stringified so numeric/bool claims can be matched.
func claimEquals(got any, want string) bool {
	switch v := got.(type) {
	case string:
		return v == want
	case fmt.Stringer:
		return v.String() == want
	case nil:
		return false
	default:
		return fmt.Sprintf("%v", v) == want
	}
}

// matchRule returns the identity whose Match predicates resolve the given sub.
// Match criteria live on each identity (not a separate rule list). Phase 1
// honors only exact `sub` matches; `sub_prefix`/`aud`/`claims` are reserved for
// WIF, where cross-identity precedence will be exact-`sub`-beats-pattern.
func (s *Server) matchRule(sub string) (string, bool) {
	for _, ident := range s.auth.Identities {
		for _, m := range ident.Match {
			if m.Sub != "" && m.Sub == sub {
				return ident.Name, true
			}
		}
	}
	return "", false
}

// findAuthIdentity returns the auth-config identity with the given name.
func (s *Server) findAuthIdentity(name string) (config.AuthIdentity, bool) {
	for _, i := range s.auth.Identities {
		if i.Name == name {
			return i, true
		}
	}
	return config.AuthIdentity{}, false
}

// identityForName builds a full-authority Identity for a named auth-config
// identity. It is shared by SSH public-key resolution and token resolution so
// both transports produce the same Identity for the same name.
func (s *Server) identityForName(name string) (Identity, bool) {
	ident, ok := s.findAuthIdentity(name)
	if !ok {
		return Identity{}, false
	}
	return s.identityFromAuth(ident), true
}

// identityFromAuth builds an Identity from an auth-config entry with full
// authority (ScopeFull). Callers may override Scopes from a token.
func (s *Server) identityFromAuth(ident config.AuthIdentity) Identity {
	return Identity{
		IdentityName: ident.Name,
		Principal:    AuthenticatedPrincipal{Subject: ident.Name, IdentityName: ident.Name, Source: "local"},
		HomeDir:      s.resolveHomeDir(ident.Home),
		HomeDocset:   ident.Home,
		Scopes:       []string{ScopeFull},
		SessionID:    generateSessionID(),
		ConnectedAt:  time.Now(),
	}
}
