package openlore

import (
	"context"
	"net/url"
	"time"
)

// OAuthClient is a client registered via Dynamic Client Registration (RFC 7591).
// OpenLore only supports public PKCE clients, so no client_secret is ever
// issued; the client_id is not a credential, it merely selects the registered
// redirect_uris that /authorize will accept (docs/mcp-bearer-auth.md §11 Phase 3).
type OAuthClient struct {
	ClientID                string    `json:"client_id"`
	ClientName              string    `json:"client_name,omitempty"`
	RedirectURIs            []string  `json:"redirect_uris"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	GrantTypes              []string  `json:"grant_types"`
	ResponseTypes           []string  `json:"response_types"`
	Scope                   string    `json:"scope,omitempty"`
	ClientIDIssuedAt        time.Time `json:"client_id_issued_at"`
	LastDelegate            string    `json:"last_delegate,omitempty"`
}

// AllowsRedirect reports whether uri exactly matches one of the client's
// registered redirect URIs. Registered clients get exact-match only (no
// normalization) to prevent redirect smuggling.
func (c OAuthClient) AllowsRedirect(uri string) bool {
	for _, r := range c.RedirectURIs {
		if r == uri {
			return true
		}
	}
	return false
}

// ClientStore persists dynamically registered OAuth clients. The flat-file
// default lives in DataDir; knowledge-backend supplies a SQLite implementation
// so every instance validates the same registered clients (docs/mcp-bearer-auth.md §9).
type ClientStore interface {
	// Save stores a newly registered client.
	Save(ctx context.Context, client OAuthClient) error
	// Lookup returns the client if present.
	Lookup(ctx context.Context, clientID string) (OAuthClient, bool, error)
}

// validNativeRedirectURI accepts redirect targets safe for clients that skip
// Dynamic Client Registration: loopback HTTP(S) callbacks (native/CLI clients
// like the Obsidian plugin) and non-HTTP custom schemes (e.g. obsidian://). It
// rejects remote http(s) origins — those can only be used by a registered
// client whose redirect_uri is bound at registration time.
func validNativeRedirectURI(raw string) bool {
	u, ok := parseRedirectURI(raw)
	if !ok {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		host := u.Hostname()
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	default:
		// Custom application scheme (obsidian://, myapp://, …).
		return true
	}
}

// validRegisteredRedirectURI accepts redirect targets a client may register:
// remote HTTPS, loopback HTTP(S), and non-HTTP custom schemes. A fragment is
// never allowed (RFC 6749 §3.1.2).
func validRegisteredRedirectURI(raw string) bool {
	u, ok := parseRedirectURI(raw)
	if !ok {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		host := u.Hostname()
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	default:
		return false
	}
}

// parseRedirectURI parses a redirect URI and enforces the invariants shared by
// both validators: non-empty, absolute (has a scheme), and no fragment.
func parseRedirectURI(raw string) (*url.URL, bool) {
	if raw == "" {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Fragment != "" {
		return nil, false
	}
	return u, true
}
