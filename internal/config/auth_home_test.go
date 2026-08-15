package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAuth(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "lore.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	return p
}

func TestLoadAuthConfigHomeValid(t *testing.T) {
	p := writeAuth(t, `{
		"docsets": {
			"public": {"paths": ["/docs/public"]},
			"agent-home": {"paths": [{"published/agent": "/home/agent"}]}
		},
		"identities": [
			{"name": "a1", "docsets": {"public": "ro", "agent-home": "rw"}, "home": "agent-home"}
		]
	}`)

	auth, err := LoadAuthConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := auth.Identities[0].Home; got != "agent-home" {
		t.Errorf("home: got %q, want %q", got, "agent-home")
	}
}

func TestLoadAuthConfigHomeDoesNotRequireLegacyGrant(t *testing.T) {
	p := writeAuth(t, `{
		"docsets": {
			"public": {"paths": ["/docs/public"]},
			"other": {"paths": ["/docs/other"]}
		},
		"identities": [
			{"name": "a1", "docsets": {"public": "ro"}, "home": "other"}
		]
	}`)

	if _, err := LoadAuthConfig(p); err != nil {
		t.Fatalf("home ownership is independent of legacy grants: %v", err)
	}
}

func TestLoadAuthConfigIgnoresLegacyUnknownDocset(t *testing.T) {
	p := writeAuth(t, `{
		"docsets": {"public": {"paths": ["/docs/public"]}},
		"identities": [
			{"name": "a1", "docsets": {"missing": "ro"}}
		]
	}`)

	if _, err := LoadAuthConfig(p); err != nil {
		t.Fatalf("legacy identity grants must be ignored: %v", err)
	}
}

func TestLoadAuthConfigDefault(t *testing.T) {
	p := writeAuth(t, `{
		"docsets": {"public": {"paths": ["/docs/public"]}},
		"default": {"public": "ro"}
	}`)

	auth, err := LoadAuthConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.Default["public"] != "ro" {
		t.Errorf("default grant: got %q, want %q", auth.Default["public"], "ro")
	}
}
