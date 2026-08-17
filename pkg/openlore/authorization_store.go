package openlore

import (
	"context"
	"fmt"

	"github.com/aakarim/go-openlore/internal/config"
)

// AuthorizationPolicy is current role membership and home ownership for a
// fully authenticated principal. Policy semantics remain in Server.
type AuthorizationPolicy struct {
	IdentityName     string
	Roles            []string
	HomeDocset       string
	DenyDocsets      []string
	DenyCapabilities []string
}

// AuthorizationStore separates authentication from current authorization.
type AuthorizationStore interface {
	ResolveAuthorization(context.Context, AuthenticatedPrincipal) (AuthorizationPolicy, error)
}

type fileAuthorizationStore struct{ auth *config.AuthConfig }

func (f fileAuthorizationStore) ResolveAuthorization(_ context.Context, p AuthenticatedPrincipal) (AuthorizationPolicy, error) {
	if p.IdentityName == "guest" || p.IdentityName == "" {
		return AuthorizationPolicy{IdentityName: "guest", Roles: []string{"guest"}}, nil
	}
	for _, identity := range f.auth.Identities {
		if identity.Name == p.IdentityName {
			roles := append([]string(nil), identity.Roles...)
			var denyDocsets, denyCapabilities []string
			if actor, _ := p.Claims["actor"].(string); actor != "" {
				delegate, ok := findDelegate(identity, actor)
				if !ok {
					return AuthorizationPolicy{}, ErrUnknownIdentity
				}
				if delegate.Roles != nil {
					roles = intersectStrings(roles, delegate.Roles)
				}
				denyDocsets = append([]string(nil), delegate.DenyDocsets...)
				denyCapabilities = append([]string(nil), delegate.DenyCapabilities...)
			}
			for _, role := range roles {
				if role != "guest" {
					if _, ok := f.auth.Roles[role]; !ok {
						return AuthorizationPolicy{}, fmt.Errorf("unknown role %q", role)
					}
				}
			}
			return AuthorizationPolicy{IdentityName: identity.Name, Roles: roles, HomeDocset: identity.Home, DenyDocsets: denyDocsets, DenyCapabilities: denyCapabilities}, nil
		}
	}
	return AuthorizationPolicy{}, ErrUnknownIdentity
}

func findDelegate(principal config.AuthIdentity, actor string) (config.DelegateEntry, bool) {
	for _, delegate := range principal.Delegates {
		if delegate.Identity == actor {
			return delegate, true
		}
	}
	return config.DelegateEntry{}, false
}

func intersectStrings(a, b []string) []string {
	wanted := map[string]bool{}
	for _, value := range b {
		wanted[value] = true
	}
	var out []string
	for _, value := range a {
		if wanted[value] {
			out = append(out, value)
		}
	}
	return out
}
