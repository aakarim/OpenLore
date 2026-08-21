package openlore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// Claims are the identity-bearing fields extracted from a verified access token.
// Nothing about authority is frozen in — resolution to an Identity happens live
// per request via the IdentityStore (see docs/mcp-bearer-auth.md §5.1).
type Claims struct {
	Subject    string
	Actor      string
	ClientAuth ClientAuthLevel
	Scope      string
	Issuer     string
	Audience   string
	Expiry     time.Time
	// Raw is the full claim set, used by the rule engine (WIF matches on
	// arbitrary claims). Standard claims are also present here.
	Raw map[string]any
}

// Issuer signs and verifies OpenLore access tokens and publishes its public
// keys as JWKS. The default implementation is ES256 with a keypair persisted in
// DataDir; knowledge-backend injects a DB-backed keypair for multi-instance
// deployments (docs/mcp-bearer-auth.md §5.3, §9).
type Issuer interface {
	// Mint signs a short-lived access token for the subject with the given
	// scope and TTL, returning the token and its expiry.
	Mint(attribution Attribution, scope string, ttl time.Duration) (token string, exp time.Time, err error)
	// Verify parses and validates a token (signature, iss, aud, exp) and
	// returns its claims. A verification failure unwraps to auth.ErrInvalidToken.
	Verify(token string) (Claims, error)
	// JWKS returns the public JSON Web Key Set for this issuer.
	JWKS() ([]byte, error)
}

type SigningKeyStore interface {
	ActiveKeyID() string
	Rotate(revokePrevious bool) (string, error)
}

type persistedSigningKey struct {
	PEM      string    `json:"pem"`
	Created  time.Time `json:"created"`
	RetireAt time.Time `json:"retire_at,omitempty"`
}

// NewIssuerFromConfig builds the default ES256 Issuer from the server config's
// token settings, using the ES256 keypair under DataDir (generated on first
// use). It returns an error if tokens are not configured. Token config is
// server infrastructure and lives in openlore.yml, so this reads Config, not
// the lore.json AuthConfig. Used by the CLI `token` command and embedders.
func NewIssuerFromConfig(cfg config.Config) (Issuer, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("tokens are not configured (set `tokens:` in openlore.yml)")
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "."
	}
	issuer, err := newESIssuer(cfg.Tokens.Issuer, cfg.Tokens.Audience, filepath.Join(dataDir, "auth", "es256.pem"))
	if err == nil {
		issuer.retireGrace = parseDurationDefault(cfg.Tokens.AccessTTL, time.Hour)
	}
	return issuer, err
}

// esIssuer is the default ES256 Issuer, backed by a keypair on disk.
type esIssuer struct {
	mu          sync.RWMutex
	issuer      string
	audience    string
	key         *ecdsa.PrivateKey
	kid         string
	keys        map[string]persistedSigningKey
	keyPath     string
	keyModTime  time.Time
	retireGrace time.Duration
}

// newESIssuer loads the ES256 keypair at keyPath, generating and persisting one
// on first use. keyPath is typically <DataDir>/auth/es256.pem.
func newESIssuer(issuer, audience, keyPath string) (*esIssuer, error) {
	if issuer == "" {
		return nil, errors.New("token issuer (auth.tokens.issuer) is required")
	}
	if audience == "" {
		return nil, errors.New("token audience (auth.tokens.audience) is required")
	}
	key, err := loadOrCreateECKey(keyPath)
	if err != nil {
		return nil, err
	}
	e := &esIssuer{
		issuer:   issuer,
		audience: audience,
		key:      key,
		kid:      keyID(&key.PublicKey),
		keyPath:  keyPath + ".keys.json", retireGrace: time.Hour,
	}
	if err := e.loadKeySet(); err != nil {
		return nil, err
	}
	return e, nil
}

func encodeECKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

func decodeECKey(value string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("not PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func (e *esIssuer) loadKeySet() error {
	e.keys = map[string]persistedSigningKey{}
	if b, err := os.ReadFile(e.keyPath); err == nil {
		if err := json.Unmarshal(b, &e.keys); err != nil {
			return fmt.Errorf("parsing signing key store: %w", err)
		}
		var activeID string
		var active persistedSigningKey
		for kid, candidate := range e.keys {
			if candidate.RetireAt.IsZero() && (activeID == "" || candidate.Created.After(active.Created)) {
				activeID, active = kid, candidate
			}
		}
		if activeID == "" {
			return errors.New("signing key store has no active key")
		}
		key, err := decodeECKey(active.PEM)
		if err != nil {
			return err
		}
		e.key, e.kid = key, activeID
		if info, statErr := os.Stat(e.keyPath); statErr == nil {
			e.keyModTime = info.ModTime()
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoded, err := encodeECKey(e.key)
	if err != nil {
		return err
	}
	e.keys[e.kid] = persistedSigningKey{PEM: encoded, Created: time.Now().UTC()}
	return e.persistKeySet()
}

func (e *esIssuer) ActiveKeyID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.kid
}

func (e *esIssuer) persistKeySet() error {
	b, err := json.MarshalIndent(e.keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.keyPath), 0o700); err != nil {
		return err
	}
	tmp := e.keyPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.keyPath); err != nil {
		return err
	}
	if info, err := os.Stat(e.keyPath); err == nil {
		e.keyModTime = info.ModTime()
	}
	return nil
}

func (e *esIssuer) Rotate(revokePrevious bool) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.syncKeySet(); err != nil {
		return "", err
	}
	next, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	for kid, stored := range e.keys {
		if revokePrevious {
			delete(e.keys, kid)
		} else if stored.RetireAt.IsZero() {
			stored.RetireAt = now.Add(e.retireGrace)
			e.keys[kid] = stored
		}
	}
	encoded, err := encodeECKey(next)
	if err != nil {
		return "", err
	}
	e.key, e.kid = next, keyID(&next.PublicKey)
	e.keys[e.kid] = persistedSigningKey{PEM: encoded, Created: now}
	if err := e.persistKeySet(); err != nil {
		return "", err
	}
	return e.kid, nil
}

func loadOrCreateECKey(keyPath string) (*ecdsa.PrivateKey, error) {
	if b, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("parsing %s: not PEM", keyPath)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing EC key %s: %w", keyPath, err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", keyPath, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating EC key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshaling EC key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating key dir: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", keyPath, err)
	}
	return key, nil
}

// keyID derives a stable JWK `kid` from the public key.
func keyID(pub *ecdsa.PublicKey) string {
	sum := sha256.Sum256(elliptic.Marshal(pub.Curve, pub.X, pub.Y))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func (e *esIssuer) Mint(attribution Attribution, scope string, ttl time.Duration) (string, time.Time, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.syncKeySet(); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	exp := now.Add(ttl)
	claims := jwt.MapClaims{
		"iss":   e.issuer,
		"sub":   attribution.Principal,
		"aud":   e.audience,
		"iat":   now.Unix(),
		"exp":   exp.Unix(),
		"scope": scope,
	}
	if attribution.Actor != "" {
		claims["act"] = map[string]string{"sub": attribution.Actor}
	}
	if attribution.ClientAuth != "" {
		claims["client_auth"] = attribution.ClientAuth
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = e.kid
	signed, err := tok.SignedString(e.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing token: %w", err)
	}
	return signed, exp, nil
}

func (e *esIssuer) Verify(token string) (Claims, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.syncKeySet(); err != nil {
		return Claims{}, err
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer(e.issuer),
		jwt.WithAudience(e.audience),
		jwt.WithExpirationRequired(),
	)
	var mc jwt.MapClaims
	_, err := parser.ParseWithClaims(token, &mc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		stored, ok := e.keys[kid]
		if !ok || (!stored.RetireAt.IsZero() && stored.RetireAt.Before(time.Now())) {
			return nil, errors.New("unknown signing key")
		}
		key, err := decodeECKey(stored.PEM)
		if err != nil {
			return nil, err
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	sub, _ := mc["sub"].(string)
	actor := ""
	if act, ok := mc["act"].(map[string]any); ok {
		actor, _ = act["sub"].(string)
	}
	clientAuth, _ := mc["client_auth"].(string)
	scope, _ := mc["scope"].(string)
	aud, _ := mc["aud"].(string)
	iss, _ := mc["iss"].(string)
	var exp time.Time
	if t, err := mc.GetExpirationTime(); err == nil && t != nil {
		exp = t.Time
	}
	return Claims{
		Subject:    sub,
		Actor:      actor,
		ClientAuth: ClientAuthLevel(clientAuth),
		Scope:      scope,
		Issuer:     iss,
		Audience:   aud,
		Expiry:     exp,
		Raw:        map[string]any(mc),
	}, nil
}

func (e *esIssuer) JWKS() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.syncKeySet(); err != nil {
		return nil, err
	}
	keys := []map[string]string{}
	for kid, stored := range e.keys {
		if !stored.RetireAt.IsZero() && stored.RetireAt.Before(time.Now()) {
			continue
		}
		key, err := decodeECKey(stored.PEM)
		if err != nil {
			return nil, err
		}
		pub := &key.PublicKey
		size := (pub.Curve.Params().BitSize + 7) / 8
		keys = append(keys, map[string]string{
			"kty": "EC", "crv": "P-256", "x": base64.RawURLEncoding.EncodeToString(leftPad(pub.X, size)),
			"y": base64.RawURLEncoding.EncodeToString(leftPad(pub.Y, size)), "kid": kid, "use": "sig", "alg": "ES256",
		})
	}
	jwks := map[string]any{
		"keys": keys,
	}
	return json.Marshal(jwks)
}

// syncKeySet picks up rotations performed by the administrative CLI while the
// server is running. The active process therefore changes signing keys without
// a restart, while retaining retiring keys for verification and JWKS clients.
// Caller must hold e.mu.
func (e *esIssuer) syncKeySet() error {
	info, err := os.Stat(e.keyPath)
	if err != nil {
		return err
	}
	if !info.ModTime().After(e.keyModTime) {
		return nil
	}
	return e.loadKeySet()
}

// leftPad returns the big-endian bytes of n, left-padded with zeros to size.
func leftPad(n *big.Int, size int) []byte {
	b := n.Bytes()
	if len(b) >= size {
		return b
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out
}
