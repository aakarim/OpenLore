package openlore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

func testIssuer(t *testing.T) *esIssuer {
	t.Helper()
	iss, err := newESIssuer("https://openlore.test", "https://openlore.test", filepath.Join(t.TempDir(), "auth", "es256.pem"))
	if err != nil {
		t.Fatalf("newESIssuer: %v", err)
	}
	return iss
}

func TestIssuer_MintVerifyRoundTrip(t *testing.T) {
	iss := testIssuer(t)

	tok, exp, err := iss.Mint(Attribution{Principal: "alice"}, ScopeFull, 30*time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !exp.After(time.Now()) {
		t.Fatalf("expiry not in the future: %v", exp)
	}

	claims, err := iss.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "alice" {
		t.Errorf("sub = %q, want alice", claims.Subject)
	}
	if claims.Scope != ScopeFull {
		t.Errorf("scope = %q, want %q", claims.Scope, ScopeFull)
	}
	if claims.Audience != "https://openlore.test" {
		t.Errorf("aud = %q", claims.Audience)
	}
}

func TestIssuer_RejectsExpired(t *testing.T) {
	iss := testIssuer(t)
	tok, _, err := iss.Mint(Attribution{Principal: "alice"}, ScopeFull, -time.Minute) // already expired
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	_, err = iss.Verify(tok)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for expired token, got %v", err)
	}
}

func TestIssuer_RejectsTampered(t *testing.T) {
	iss := testIssuer(t)
	tok, _, _ := iss.Mint(Attribution{Principal: "alice"}, ScopeFull, 30*time.Minute)
	// Flip the last character of the signature.
	tampered := tok[:len(tok)-1] + string(rune(tok[len(tok)-1]^0x01))
	if _, err := iss.Verify(tampered); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for tampered token, got %v", err)
	}
}

func TestIssuer_RejectsWrongIssuer(t *testing.T) {
	a := testIssuer(t)
	// A different issuer/key must not verify a's token.
	b, err := newESIssuer("https://evil.test", "https://openlore.test", filepath.Join(t.TempDir(), "auth", "es256.pem"))
	if err != nil {
		t.Fatalf("newESIssuer: %v", err)
	}
	tok, _, _ := b.Mint(Attribution{Principal: "alice"}, ScopeFull, 30*time.Minute)
	if _, err := a.Verify(tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for foreign issuer, got %v", err)
	}
}

func TestIssuer_JWKS(t *testing.T) {
	iss := testIssuer(t)
	b, err := iss.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal JWKS: %v", err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(doc.Keys))
	}
	k := doc.Keys[0]
	if k["kty"] != "EC" || k["crv"] != "P-256" || k["alg"] != "ES256" {
		t.Errorf("unexpected JWK params: %+v", k)
	}
	if k["x"] == "" || k["y"] == "" || k["kid"] != iss.kid {
		t.Errorf("JWK missing x/y or kid mismatch: %+v", k)
	}
}
