package openlore

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/golang-jwt/jwt/v5"
)

// fakeVerifier is an injectable OIDCVerifier that returns preset claims (or an
// error), standing in for real IdP JWKS verification in unit tests.
type fakeVerifier struct {
	claims Claims
	err    error
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (Claims, error) {
	return f.claims, f.err
}

// newWIFTestServer builds a token-auth server with a human identity (alice) and
// a WIF-mapped identity (ci-indexer, reachable by a repo sub_prefix rule that
// narrows to read). The OIDCVerifier is injected so no network is needed.
func newWIFTestServer(t *testing.T, verifier OIDCVerifier) *Server {
	t.Helper()

	merge := NewMergeFS()
	merge.Mount("public", NewFSAdapter(fstest.MapFS{"hello.txt": {Data: []byte("public\n")}}))
	merge.Mount("secret", NewFSAdapter(fstest.MapFS{"top.txt": {Data: []byte("secret\n")}}))

	keyless := true
	s := &Server{
		merge:        merge,
		authEnforced: true,
		grants:       newGrantRegistry(),
		oidc:         verifier,
		auth: &config.AuthConfig{
			AllowKeyless:    &keyless,
			UnknownIdentity: "allow",
			Roles:           map[string]config.RoleSpec{"alice": {}, "ci-indexer": {}},
			Docsets: map[string]config.DocsetSpec{
				"public": {
					Paths:  []config.PathMapping{{Source: "/public", Display: "/public"}},
					Access: config.DocsetAccess{Allow: map[string]string{"guest": "ro", "alice": "ro", "ci-indexer": "ro"}},
				},
				"secret": {
					Paths:  []config.PathMapping{{Source: "/secret", Display: "/secret"}},
					Access: config.DocsetAccess{Allow: map[string]string{"alice": "rw", "ci-indexer": "rw"}},
				},
			},
			Identities: []config.AuthIdentity{
				{Name: "alice", Roles: []string{"alice"}},
				{
					Name:  "ci-indexer",
					Roles: []string{"ci-indexer"},
					Match: []config.IdentityMatch{{
						SubPrefix: "repo:my-org/my-repo:",
						Aud:       "https://openlore.test",
						Scope:     ScopeRead,
						TTL:       "15m",
					}},
				},
			},
		},
		config: config.Config{
			DataDir:      t.TempDir(),
			AllowKeyless: true,
			Tokens: &config.AuthTokensConfig{
				Issuer:   "https://openlore.test",
				Audience: "https://openlore.test",
			},
		},
	}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	if err := s.initAuth(); err != nil {
		t.Fatalf("initAuth: %v", err)
	}
	return s
}

func ghClaims() Claims {
	return Claims{
		Subject:  "repo:my-org/my-repo:ref:refs/heads/main",
		Issuer:   "https://token.actions.githubusercontent.com",
		Audience: "https://openlore.test",
		Expiry:   time.Now().Add(10 * time.Minute),
		Raw: map[string]any{
			"sub":         "repo:my-org/my-repo:ref:refs/heads/main",
			"aud":         "https://openlore.test",
			"environment": "production",
		},
	}
}

func TestWIF_ExchangeMatchesRuleAndNarrows(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})

	sub, scope, ttl, err := s.ExchangeAssertion(context.Background(), "assertion")
	if err != nil {
		t.Fatalf("ExchangeAssertion: %v", err)
	}
	if sub != "ci-indexer" {
		t.Errorf("sub = %q, want ci-indexer", sub)
	}
	if scope != ScopeRead {
		t.Errorf("scope = %q, want read", scope)
	}
	// ttl capped by rule (15m) and by 2× remaining assertion (~20m) → 15m.
	if ttl <= 0 || ttl > 15*time.Minute {
		t.Errorf("ttl = %v, want in (0, 15m]", ttl)
	}
}

func TestWIF_ExchangeUnknownIdentityDenied(t *testing.T) {
	c := ghClaims()
	c.Subject = "repo:someone-else/repo:ref:main"
	c.Raw["sub"] = c.Subject
	s := newWIFTestServer(t, fakeVerifier{claims: c})

	if _, _, _, err := s.ExchangeAssertion(context.Background(), "assertion"); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("err = %v, want ErrUnknownIdentity", err)
	}
}

func TestWIF_ExchangeInvalidScopeDenied(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})
	// Corrupt the matched rule's scope to an unrecognized value.
	s.auth.Identities[1].Match[0].Scope = "bogus"

	if _, _, _, err := s.ExchangeAssertion(context.Background(), "assertion"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("err = %v, want ErrInvalidScope", err)
	}
}

func TestWIF_ExchangeVerifyFailurePropagates(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{err: errors.New("bad signature")})
	if _, _, _, err := s.ExchangeAssertion(context.Background(), "assertion"); err == nil {
		t.Fatal("expected verification error")
	}
}

// The jwt-bearer grant end-to-end: exchange → minted access token → verify →
// resolve → the resolved identity is narrowed to read (no refresh token issued).
func TestWIF_TokenEndpointMintsNarrowedToken(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})

	rec, resp := postForm(t, s.tokens, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {"external.idp.jwt"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if resp.AccessToken == "" {
		t.Fatal("no access token minted")
	}
	if resp.RefreshToken != "" {
		t.Fatal("WIF exchange must not issue a refresh token")
	}
	if resp.Scope != ScopeRead {
		t.Errorf("scope = %q, want read", resp.Scope)
	}

	claims, err := s.issuer.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("Verify minted token: %v", err)
	}
	id, err := s.identityStore.Resolve(context.Background(), claims)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if id.IdentityName != "ci-indexer" {
		t.Errorf("identity = %q, want ci-indexer", id.IdentityName)
	}
	if scopeGrantsWrite(id.Scopes) {
		t.Errorf("read-scoped identity must not grant write; scopes=%v", id.Scopes)
	}
}

// A read-scoped WIF identity is read-only in the shell even though the same
// identity resolved via a full-scope token would be write-capable.
func TestWIF_ReadScopeShellIsReadOnly(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})

	readID, _ := s.identityForName("ci-indexer")
	readID.Scopes = []string{ScopeRead}
	if sh := s.buildSessionShell(readID); sh.ActionAllowed(cmds.ActionWrite) {
		t.Error("read-scoped session must NOT allow write")
	}

	fullID, _ := s.identityForName("ci-indexer") // defaults to {full}
	if sh := s.buildSessionShell(fullID); !sh.ActionAllowed(cmds.ActionWrite) {
		t.Error("full-scope session should allow write")
	}
}

func TestTokenScopesFailClosed(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})
	for _, scope := range []string{"", "unknown"} {
		t.Run(scope, func(t *testing.T) {
			id, err := s.resolveClaims(Claims{Subject: "ci-indexer", Scope: scope})
			if err != nil {
				t.Fatalf("resolve claims: %v", err)
			}
			if scopeGrantsWrite(id.Scopes) {
				t.Fatalf("scope %q unexpectedly grants write", scope)
			}
		})
	}
}

func TestWIF_ExactSubBeatsPrefix(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})
	// Add an exact-sub rule on alice for the same subject; it must win.
	s.auth.Identities[0].Match = []config.IdentityMatch{{
		Sub:   "repo:my-org/my-repo:ref:refs/heads/main",
		Scope: ScopeRead,
	}}

	name, _, ok := s.matchWIFClaims(ghClaims())
	if !ok || name != "alice" {
		t.Fatalf("exact sub should win: name=%q ok=%v", name, ok)
	}
}

func TestWIF_ClaimPredicateMustMatch(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})
	// Require environment=staging; the assertion carries production → no match.
	s.auth.Identities[1].Match[0].Claims = map[string]string{"environment": "staging"}

	if _, _, ok := s.matchWIFClaims(ghClaims()); ok {
		t.Fatal("claim predicate mismatch must not resolve")
	}
	// Now require the actual value → matches.
	s.auth.Identities[1].Match[0].Claims = map[string]string{"environment": "production"}
	if _, _, ok := s.matchWIFClaims(ghClaims()); !ok {
		t.Fatal("claim predicate match should resolve")
	}
}

func TestWIF_TTLFloorAndAssertionCap(t *testing.T) {
	s := newWIFTestServer(t, fakeVerifier{claims: ghClaims()})

	// Assertion expiring in 10s → 2× = 20s, below the 60s floor → clamps to 60s.
	got := s.wifTTL("15m", time.Now().Add(10*time.Second))
	if got != time.Minute {
		t.Errorf("ttl = %v, want 1m floor", got)
	}
	// A short rule ttl caps below access TTL.
	got = s.wifTTL("2m", time.Now().Add(time.Hour))
	if got != 2*time.Minute {
		t.Errorf("ttl = %v, want 2m (rule cap)", got)
	}
}

func TestOIDCVerifier_RejectsUnsupportedMode(t *testing.T) {
	_, err := newOIDCVerifier("https://openlore.test", []config.OIDCIssuer{
		{IssuerURL: "https://idp.example", JWKS: config.JWKSSpec{Mode: "static"}},
	})
	if err == nil {
		t.Fatal("expected error for unsupported jwks mode")
	}
}

func TestOIDCVerifier_UntrustedIssuerRejected(t *testing.T) {
	v, err := newOIDCVerifier("https://openlore.test", []config.OIDCIssuer{
		{IssuerURL: "https://idp.example"},
	})
	if err != nil {
		t.Fatalf("newOIDCVerifier: %v", err)
	}
	// A token whose iss is not among the trusted issuers is rejected before any
	// network call. Build a minimal unsigned JWT with an untrusted iss.
	assertion := unsignedJWT(t, map[string]any{"iss": "https://evil.example", "sub": "x"})
	if _, err := v.Verify(context.Background(), assertion); err == nil {
		t.Fatal("untrusted issuer must be rejected")
	}
}

// unsignedJWT builds a parseable (alg=none) JWT for tests that only exercise
// the pre-verification routing (issuer selection), never the signature path.
func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	mc := jwt.MapClaims{}
	for k, v := range claims {
		mc[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, mc)
	s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("build unsigned jwt: %v", err)
	}
	return s
}

func TestOIDCVerifier_NoIssuersIsNil(t *testing.T) {
	v, err := newOIDCVerifier("https://openlore.test", nil)
	if err != nil {
		t.Fatalf("newOIDCVerifier: %v", err)
	}
	if v != nil {
		t.Fatal("no issuers should yield a nil verifier (WIF disabled)")
	}
}
