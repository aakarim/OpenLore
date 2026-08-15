package openlore

import (
	"context"
	"fmt"

	"github.com/aakarim/go-openlore/internal/config"
)

// AuthorizationPolicy is current role membership and home ownership for a
// fully authenticated principal. Policy semantics remain in Server.
type AuthorizationPolicy struct {
	IdentityName string
	Roles        []string
	HomeDocset   string
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
			for _, role := range identity.Roles {
				if role != "guest" {
					if _, ok := f.auth.Roles[role]; !ok {
						return AuthorizationPolicy{}, fmt.Errorf("unknown role %q", role)
					}
				}
			}
			return AuthorizationPolicy{IdentityName: identity.Name, Roles: append([]string(nil), identity.Roles...), HomeDocset: identity.Home}, nil
		}
	}
	return AuthorizationPolicy{}, ErrUnknownIdentity
}
