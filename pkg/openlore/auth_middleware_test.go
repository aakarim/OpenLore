package openlore

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

// newTokenTestServer builds a Server with token auth enabled: two docsets
// (public in the default lore, secret only in eng), an identity "alice" in eng,
// and an ES256 issuer keyed under a temp DataDir.
func newTokenTestServer(t *testing.T, allowKeyless bool, unknownIdentity string) *Server {
	t.Helper()

	merge := NewMergeFS()
	merge.Mount("public", NewFSAdapter(fstest.MapFS{"hello.txt": {Data: []byte("public\n")}}))
	merge.Mount("secret", NewFSAdapter(fstest.MapFS{"top.txt": {Data: []byte("secret\n")}}))

	keyless := allowKeyless
	s := &Server{
		merge:        merge,
		authEnforced: true,
		grants:       newGrantRegistry(),
		auth: &config.AuthConfig{
			AllowKeyless:    &keyless,
			UnknownIdentity: unknownIdentity,
			Roles:           map[string]config.RoleSpec{"alice": {}},
			Docsets: map[string]config.DocsetSpec{
				"public": {Paths: []config.PathMapping{{Source: "/public", Display: "/public"}}, Access: config.DocsetAccess{Allow: map[string]string{"guest": "ro", "alice": "ro"}}},
				"secret": {Paths: []config.PathMapping{{Source: "/secret", Display: "/secret"}}, Access: config.DocsetAccess{Allow: map[string]string{"alice": "rw"}}},
			},
			Identities: []config.AuthIdentity{
				{Name: "alice", Roles: []string{"alice"}},
			},
		},
		config: config.Config{
			DataDir:         t.TempDir(),
			AllowKeyless:    allowKeyless,
			UnknownIdentity: unknownIdentity,
			// Token config is server infrastructure (openlore.yml), not lore.json.
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

// identityEcho is a handler that reports the resolved identity from context.
func (s *Server) identityEcho() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.identityFromContext(r.Context())
		name := id.IdentityName
		if name == "" {
			name = "anonymous"
		}
		resolved := map[string]string{}
		for docset := range s.auth.Docsets {
			if grants, ok := s.effectiveGrantNames(id, docset); ok {
				resolved[docset] = grants[len(grants)-1]
			}
		}
		w.Write([]byte(name + " " + grantsString(resolved)))
	})
}

// grantsString renders a grant map as a stable "docset:grant,…" string.
func grantsString(grants map[string]string) string {
	keys := make([]string, 0, len(grants))
	for k := range grants {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+grants[k])
	}
	return strings.Join(parts, ",")
}

func mint(t *testing.T, s *Server, sub, scope string) string {
	t.Helper()
	tok, _, err := s.issuer.Mint(sub, scope, 30*time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	return tok
}

func TestAuthMiddleware_KeylessNoTokenIsAnonymous(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	h := s.authMiddleware(s.identityEcho(), false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "guest public:ro" {
		t.Fatalf("body = %q, want %q", got, "guest public:ro")
	}
}

func TestAuthMiddleware_RequiredOverrideChallengesKeylessCaller(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	h := s.authMiddleware(s.identityEcho(), true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if challenge := rec.Header().Get("WWW-Authenticate"); !strings.Contains(challenge, "resource_metadata=") {
		t.Fatalf("WWW-Authenticate = %q, want OAuth resource metadata challenge", challenge)
	}
}

func TestAuthMiddleware_ValidTokenResolvesIdentity(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	h := s.authMiddleware(s.identityEcho(), false)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+mint(t, s, "alice", ScopeFull))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "alice public:ro,secret:rw" {
		t.Fatalf("body = %q, want %q", got, "alice public:ro,secret:rw")
	}
}

func TestAuthMiddleware_InvalidTokenRejectedEvenKeyless(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	h := s.authMiddleware(s.identityEcho(), false)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_RequiredPostureRejectsMissingToken(t *testing.T) {
	s := newTokenTestServer(t, false, "deny")
	h := s.authMiddleware(s.identityEcho(), true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	challenge := rec.Header().Get("WWW-Authenticate")
	if challenge == "" {
		t.Fatalf("expected WWW-Authenticate challenge header")
	}
	// The challenge must point OAuth-native clients at the resource metadata so
	// they can discover the flow (RFC 9728 §5.1).
	if !strings.Contains(challenge, `resource_metadata="`+s.resourceMetadataURL()+`"`) {
		t.Errorf("challenge = %q, want resource_metadata pointing at %q", challenge, s.resourceMetadataURL())
	}
}

func TestAuthMiddleware_UnknownSubDenyIs403(t *testing.T) {
	s := newTokenTestServer(t, false, "deny")
	h := s.authMiddleware(s.identityEcho(), true)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+mint(t, s, "nobody", ScopeFull))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddleware_AnonymousTokenResolvesToDefault(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	h := s.authMiddleware(s.identityEcho(), false)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+mint(t, s, anonymousSubject, ScopeFull))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != "guest public:ro" {
		t.Fatalf("public token body = %q, want %q", got, "guest public:ro")
	}
}
