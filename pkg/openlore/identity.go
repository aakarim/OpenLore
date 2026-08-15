package openlore

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// ScopeFull is the sentinel scope granting an identity its full authority
// (no narrowing). SSH key/cert logins resolve to this today; future WIF tokens
// will instead carry narrowing scopes. Missing/empty/unrecognized scopes are
// fail-closed (never full) — see docs/mcp-bearer-auth.md §5.4.
const ScopeFull = "full"

// ScopeRead narrows a token to read-only authority. WIF rules use narrowing
// scopes like this to grant less than an identity's full authority.
const ScopeRead = "read"

// scopeGrantsWrite reports whether a token's scopes permit write/publish/approve
// actions. Only the full sentinel grants write; every other scope (read, empty,
// unknown) is read-only — fail-closed, never elevating (docs/mcp-bearer-auth.md
// §5.4).
func scopeGrantsWrite(scopes []string) bool {
	return len(scopes) == 1 && scopes[0] == ScopeFull
}

// recognizedScope reports whether s is a scope OpenLore knows how to enforce. A
// WIF exchange whose rule carries an unrecognized (or empty) scope is denied —
// fail-closed, never full.
func recognizedScope(s string) bool {
	switch s {
	case ScopeFull, ScopeRead:
		return true
	default:
		return false
	}
}

// Identity represents a connected caller (SSH session or MCP/HTTP request).
type Identity struct {
	RemoteAddr     string
	User           string
	PublicKey      ssh.PublicKey
	SessionID      string
	ConnectedAt    time.Time
	IdentityName   string // matched identity name from auth config
	Principal      AuthenticatedPrincipal
	policySnapshot *AuthorizationPolicy
	HomeDir        string   // display path of the identity's home docset ($HOME); empty = none
	HomeDocset     string   // name of the identity's home docset; empty = none
	Scopes         []string // token scopes narrowing authority; {ScopeFull} = full authority
}

// AuthenticatedPrincipal is the stable, transport-neutral authentication
// result passed to authorization. Subject is the original authenticated
// subject; IdentityName is the claim-resolved local identity.
type AuthenticatedPrincipal struct {
	Subject      string
	IdentityName string
	Source       string
	Claims       map[string]any
	Scope        string
}

// OnConnectFunc is called when a new SSH session is established.
type OnConnectFunc func(Identity)

// OnDisconnectFunc is called when an SSH session ends.
type OnDisconnectFunc func(Identity)
