package openlore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/auth0/go-jwt-middleware/v3/jwks"
	"github.com/auth0/go-jwt-middleware/v3/validator"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// OIDCVerifier verifies an external IdP assertion (a platform-issued JWT) for
// workload identity federation. It is the seam that turns a platform OIDC token
// (GitHub Actions, Kubernetes/SPIFFE, Okta, …) into OpenLore Claims the WIF rule
// engine can match. The default implementation (newOIDCVerifier) pins each
// trusted issuer to its own JWKS and requires OUR audience, so a token minted
// for one service cannot be replayed to another. Injectable so knowledge-backend
// can supply its own verifier. See workload-identity-federation.md.
type OIDCVerifier interface {
	// Verify validates the assertion's signature (against the issuer's JWKS),
	// issuer, audience, and expiry, returning its claims. Failure unwraps to
	// auth.ErrInvalidToken.
	Verify(ctx context.Context, assertion string) (Claims, error)
}

// wifAlgorithms are the asymmetric signature algorithms OpenLore accepts for
// external assertions. Symmetric (HMAC) algorithms are intentionally excluded:
// a shared secret is a long-lived key, which defeats WIF's purpose.
var wifAlgorithms = []validator.SignatureAlgorithm{
	validator.RS256, validator.RS384, validator.RS512,
	validator.ES256, validator.ES384, validator.ES512,
	validator.PS256, validator.PS384, validator.PS512,
	validator.EdDSA,
}

// oidcVerifier holds one auth0 validator per trusted issuer. The validators do
// the heavy lifting (OIDC discovery, JWKS caching, signature + iss/aud/exp +
// algorithm pinning); we pick the right one by the assertion's iss claim so a
// forged token cannot be routed to a different issuer's keys.
type oidcVerifier struct {
	audience   string
	validators map[string]*validator.Validator
}

// newOIDCVerifier builds a verifier that trusts the given OIDC issuers, each
// pinned to OUR audience. Keys are obtained per issuer via OIDC discovery
// (default) or a direct JWKS URL (jwks mode "url"). Returns nil (no error) when
// no issuers are configured — WIF stays disabled.
func newOIDCVerifier(audience string, issuers []config.OIDCIssuer) (*oidcVerifier, error) {
	if len(issuers) == 0 {
		return nil, nil
	}
	if audience == "" {
		return nil, errors.New("token audience is required for WIF verification")
	}
	v := &oidcVerifier{audience: audience, validators: map[string]*validator.Validator{}}
	for _, oi := range issuers {
		if oi.IssuerURL == "" {
			return nil, errors.New("oidc issuer_url is required")
		}
		u, err := url.Parse(oi.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("oidc issuer %q: %w", oi.IssuerURL, err)
		}
		providerOpts := []any{jwks.WithIssuerURL(u)}
		switch oi.JWKS.Mode {
		case "", "discovery":
			if oi.JWKS.URL != "" {
				return nil, fmt.Errorf("oidc issuer %q: jwks.url is only valid with jwks mode \"url\"", oi.IssuerURL)
			}
		case "url":
			if oi.JWKS.URL == "" {
				return nil, fmt.Errorf("oidc issuer %q: jwks mode \"url\" requires jwks.url", oi.IssuerURL)
			}
			jwksURL, err := url.Parse(oi.JWKS.URL)
			if err != nil {
				return nil, fmt.Errorf("oidc issuer %q: jwks.url: %w", oi.IssuerURL, err)
			}
			providerOpts = append(providerOpts, jwks.WithCustomJWKSURI(jwksURL))
		default:
			return nil, fmt.Errorf("oidc issuer %q: unsupported jwks mode %q (supported: \"discovery\", \"url\")", oi.IssuerURL, oi.JWKS.Mode)
		}
		provider, err := jwks.NewCachingProvider(providerOpts...)
		if err != nil {
			return nil, fmt.Errorf("oidc issuer %q: %w", oi.IssuerURL, err)
		}
		val, err := validator.New(
			validator.WithKeyFunc(provider.KeyFunc),
			validator.WithAlgorithms(wifAlgorithms),
			validator.WithIssuer(oi.IssuerURL),
			validator.WithAudience(audience),
			validator.WithAllowedClockSkew(60*time.Second),
		)
		if err != nil {
			return nil, fmt.Errorf("oidc issuer %q: %w", oi.IssuerURL, err)
		}
		v.validators[oi.IssuerURL] = val
	}
	return v, nil
}

func (v *oidcVerifier) Verify(ctx context.Context, assertion string) (Claims, error) {
	// Pick the validator by the assertion's (unverified) iss claim; the chosen
	// validator then re-checks iss == that issuer against its JWKS, so an
	// attacker cannot route a forged token to a different issuer's keys.
	iss, err := unverifiedIssuer(assertion)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	val, ok := v.validators[iss]
	if !ok {
		return Claims{}, fmt.Errorf("%w: untrusted issuer %q", auth.ErrInvalidToken, iss)
	}
	if _, err := val.ValidateToken(ctx, assertion); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	// Signature + registered claims are verified; re-parse the (now trusted)
	// payload into a claim map for the WIF rule engine.
	return claimsFromAssertion(assertion)
}

// unverifiedIssuer reads the iss claim without verification, used only to route
// to the right validator (which then verifies iss cryptographically).
func unverifiedIssuer(assertion string) (string, error) {
	var mc jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(assertion, &mc); err != nil {
		return "", err
	}
	iss, _ := mc["iss"].(string)
	if iss == "" {
		return "", errors.New("assertion has no iss claim")
	}
	return iss, nil
}

// claimsFromAssertion decodes an already-verified assertion into Claims. The
// full claim set is kept in Raw so WIF rules can match arbitrary claims
// (repository, environment, ref, …).
func claimsFromAssertion(assertion string) (Claims, error) {
	var mc jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(assertion, &mc); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	sub, _ := mc["sub"].(string)
	iss, _ := mc["iss"].(string)
	aud, _ := mc["aud"].(string) // single-valued aud; array form lives in Raw
	var exp time.Time
	if t, err := mc.GetExpirationTime(); err == nil && t != nil {
		exp = t.Time
	}
	return Claims{
		Subject:  sub,
		Issuer:   iss,
		Audience: aud,
		Expiry:   exp,
		Raw:      map[string]any(mc),
	}, nil
}
