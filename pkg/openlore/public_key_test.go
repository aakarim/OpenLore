package openlore

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestPublicKeyMatchesAuthorizedKeyComment(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gossh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	configured := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key))) + " onboarding@example"

	if !publicKeyMatches(configured, key) {
		t.Fatal("public key with an authorized_keys comment did not match")
	}
}
