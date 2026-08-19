package openlore

import (
	"encoding/json"
	"net/http"
	"strings"
)

// OAuth discovery paths (RFC 9728 + RFC 8414). MCP clients (Claude Desktop,
// Cowork) probe these to learn where to register, authorize, and get tokens.
const (
	protectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
	authServerMetadataPath        = "/.well-known/oauth-authorization-server"
	jwksPath                      = "/.well-known/jwks.json"
	registrationPath              = "/register"
	tokenPath                     = "/oauth/token"
	revocationPath                = "/oauth/revoke"
	authorizePath                 = "/authorize"
	authorizePublicPath           = "/authorize/public"
	authorizeConsentPath          = "/authorize/consent"
)

// oauthRoutes returns the OAuth endpoints mounted whenever token auth is
// configured: the mint step (token + JWKS), the browser-driven authorization
// flow (authorize + public choice), Dynamic Client Registration, and the RFC
// 9728/8414 discovery documents. Discovery/registration/authorize are mounted
// regardless of keyless posture so OAuth-native clients can always find the flow
// (docs/mcp-bearer-auth.md §4, §8.4). None of these are wrapped in
// authMiddleware — only /mcp and /api are.
func (s *Server) oauthRoutes() map[string]http.Handler {
	return map[string]http.Handler{
		tokenPath:                     s.tokens,
		revocationPath:                http.HandlerFunc(s.tokens.revoke),
		jwksPath:                      jwksHandler(s.issuer),
		authorizePath:                 http.HandlerFunc(s.authorizeHandler),
		authorizePublicPath:           http.HandlerFunc(s.authorizePublicHandler),
		authorizeConsentPath:          http.HandlerFunc(s.authorizeConsentHandler),
		registrationPath:              http.HandlerFunc(s.registrationHandler),
		protectedResourceMetadataPath: http.HandlerFunc(s.resourceMetadata),
		authServerMetadataPath:        http.HandlerFunc(s.authServerMetadata),
	}
}

// issuerBaseURL is the external origin OpenLore advertises in OAuth metadata and
// WWW-Authenticate. It is config.Tokens.Issuer (the `iss` claim and JWKS base),
// trimmed of a trailing slash. Phase 3 requires a pathless issuer so the root
// /.well-known/* documents are correct (docs/mcp-bearer-auth.md §11).
func (s *Server) issuerBaseURL() string {
	if s.config.Tokens == nil {
		return ""
	}
	return strings.TrimRight(s.config.Tokens.Issuer, "/")
}

// resourceMetadata serves RFC 9728 Protected Resource Metadata. The `resource`
// identifier equals config.Tokens.Audience — the same value tokens are minted
// for and the middleware verifies — so an MCP client requests a token for the
// exact audience OpenLore accepts (docs/mcp-bearer-auth.md §11 Q4).
func (s *Server) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSONMetadata(w, map[string]any{
		"resource":                 s.config.Tokens.Audience,
		"authorization_servers":    []string{s.issuerBaseURL()},
		"scopes_supported":         []string{ScopeFull},
		"bearer_methods_supported": []string{"header"},
	})
}

// authServerMetadata serves RFC 8414 Authorization Server Metadata: the endpoint
// map an MCP client follows to register (DCR), authorize (PKCE), and exchange
// codes for tokens.
func (s *Server) authServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.issuerBaseURL()
	writeJSONMetadata(w, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + authorizePath,
		"token_endpoint":                        base + tokenPath,
		"revocation_endpoint":                   base + revocationPath,
		"registration_endpoint":                 base + registrationPath,
		"jwks_uri":                              base + jwksPath,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:token-exchange"},
		"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		"client_id_metadata_document_supported": true,
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{ScopeFull},
	})
}

// resourceMetadataURL is the absolute URL of the protected-resource-metadata
// document, emitted in WWW-Authenticate so a challenged client can discover the
// OAuth flow (RFC 9728 §5.1).
func (s *Server) resourceMetadataURL() string {
	return s.issuerBaseURL() + protectedResourceMetadataPath
}

// bearerChallenge builds a WWW-Authenticate: Bearer value carrying the
// resource_metadata pointer and advertised scope. extra is an optional
// auth-param such as `error="invalid_token"`.
func (s *Server) bearerChallenge(extra string) string {
	parts := []string{"Bearer"}
	inner := []string{}
	if extra != "" {
		inner = append(inner, extra)
	}
	inner = append(inner,
		`resource_metadata="`+s.resourceMetadataURL()+`"`,
		`scope="`+ScopeFull+`"`,
	)
	return parts[0] + " " + strings.Join(inner, ", ")
}

func writeJSONMetadata(w http.ResponseWriter, doc map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc)
}
