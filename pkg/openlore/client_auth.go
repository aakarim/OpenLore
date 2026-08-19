package openlore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/auth0/go-jwt-middleware/v3/core"
	"github.com/auth0/go-jwt-middleware/v3/jwks"
	"github.com/auth0/go-jwt-middleware/v3/validator"
	"github.com/golang-jwt/jwt/v5"
)

const clientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

type ClientAuthenticator interface {
	Authenticate(*http.Request, *CIMDClient) (ClientAuthLevel, error)
}

type clientAuthenticator struct {
	resolver      CIMDResolver
	httpClient    *http.Client
	tokenEndpoint string
	audit         AuditLog
	mu            sync.Mutex
	validators    map[string]*validator.Validator
	usedJTI       map[string]time.Time
}

func newClientAuthenticator(resolver *cimdResolver, tokenEndpoint string, audit AuditLog) *clientAuthenticator {
	return &clientAuthenticator{
		resolver: resolver, httpClient: resolver.client, tokenEndpoint: tokenEndpoint, audit: audit,
		validators: map[string]*validator.Validator{}, usedJTI: map[string]time.Time{},
	}
}

func (a *clientAuthenticator) Authenticate(r *http.Request, client *CIMDClient) (ClientAuthLevel, error) {
	if client == nil {
		return "", errors.New("CIMD client required")
	}
	assertion := r.Form.Get("client_assertion")
	if !client.Offers("private_key_jwt") {
		if assertion != "" {
			return "", errors.New("client does not offer private_key_jwt")
		}
		return AuthCIMD, nil
	}
	if r.Form.Get("client_assertion_type") != clientAssertionType || assertion == "" {
		if a.audit != nil {
			_ = a.audit.Record(r.Context(), AuditEvent{Type: "client.auth_downgrade_rejected", Details: map[string]any{"client_id": client.ClientID}})
		}
		return "", errors.New("private_key_jwt is required for this client")
	}
	if err := a.verifyAssertion(r.Context(), client, assertion); err != nil {
		if !assertionKeyFailure(err) {
			return "", fmt.Errorf("invalid client assertion: %w", err)
		}
		// Refetch metadata and rebuild the key provider once. This handles a new
		// vendor kid immediately rather than waiting for cache expiry.
		if resolver, ok := a.resolver.(*cimdResolver); ok {
			resolver.Invalidate(client.ClientID)
		}
		a.mu.Lock()
		delete(a.validators, client.ClientID)
		a.mu.Unlock()
		fresh, resolveErr := a.resolver.Resolve(r.Context(), client.ClientID)
		if resolveErr != nil || a.verifyAssertion(r.Context(), fresh, assertion) != nil {
			return "", fmt.Errorf("invalid client assertion: %w", err)
		}
	}
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.PeerCertificates) > 0 {
		return AuthPrivateKeyJWTMTLS, nil
	}
	return AuthPrivateKeyJWT, nil
}

func assertionKeyFailure(err error) bool {
	var validationErr *core.ValidationError
	if !errors.As(err, &validationErr) {
		return false
	}
	return validationErr.Code == core.ErrorCodeInvalidSignature || validationErr.Code == core.ErrorCodeJWKSKeyNotFound
}

func (a *clientAuthenticator) verifyAssertion(ctx context.Context, client *CIMDClient, assertion string) error {
	val, err := a.validator(client)
	if err != nil {
		return err
	}
	if _, err := val.ValidateToken(ctx, assertion); err != nil {
		return err
	}
	var claims jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(assertion, &claims); err != nil {
		return err
	}
	iss, _ := claims["iss"].(string)
	sub, _ := claims["sub"].(string)
	jti, _ := claims["jti"].(string)
	issuedAt, iatErr := claims.GetIssuedAt()
	expires, expErr := claims.GetExpirationTime()
	if iss != client.ClientID || sub != client.ClientID || jti == "" || iatErr != nil || issuedAt == nil || expErr != nil || expires == nil {
		return errors.New("client assertion requires iss=sub=client_id, iat, exp, and jti")
	}
	now := time.Now()
	if issuedAt.After(now.Add(time.Minute)) || issuedAt.Before(now.Add(-5*time.Minute)) || expires.Before(now) || expires.After(issuedAt.Add(5*time.Minute)) {
		return errors.New("client assertion is not fresh")
	}
	key := client.ClientID + "\x00" + jti
	a.mu.Lock()
	defer a.mu.Unlock()
	for seen, expiry := range a.usedJTI {
		if expiry.Before(now) {
			delete(a.usedJTI, seen)
		}
	}
	if _, used := a.usedJTI[key]; used {
		return errors.New("client assertion jti was already used")
	}
	a.usedJTI[key] = expires.Time
	return nil
}

func (a *clientAuthenticator) validator(client *CIMDClient) (*validator.Validator, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.validators[client.ClientID]; existing != nil {
		return existing, nil
	}
	issuerURL, err := url.Parse(client.ClientID)
	if err != nil {
		return nil, err
	}
	jwksURL, err := url.Parse(client.JWKSURI)
	if err != nil {
		return nil, err
	}
	provider, err := jwks.NewCachingProvider(
		jwks.WithIssuerURL(issuerURL), jwks.WithCustomJWKSURI(jwksURL), jwks.WithCustomClient(a.httpClient),
	)
	if err != nil {
		return nil, err
	}
	val, err := validator.New(
		validator.WithKeyFunc(provider.KeyFunc), validator.WithAlgorithms(wifAlgorithms),
		validator.WithIssuer(client.ClientID), validator.WithAudience(a.tokenEndpoint),
		validator.WithAllowedClockSkew(time.Minute),
	)
	if err != nil {
		return nil, err
	}
	a.validators[client.ClientID] = val
	return val, nil
}
