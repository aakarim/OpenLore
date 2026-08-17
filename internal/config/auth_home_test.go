package config

import (
	"encoding/json"
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

func TestLoadAuthConfigDelegates(t *testing.T) {
	p := writeAuth(t, `{
		"roles": {"engineer": {}},
		"docsets": {"docs": {"paths": ["/docs"]}},
		"identities": [
			{"name":"adil","roles":["engineer"],"delegates":[{"identity":"claude@claude.ai","deny_docsets":["docs"],"deny_capabilities":["lore:config:edit"]}]},
			{"name":"claude@claude.ai","created_by":"oauth"}
		]
	}`)
	auth, err := LoadAuthConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := auth.Identities[0].Delegates[0].Identity; got != "claude@claude.ai" {
		t.Fatalf("delegate=%q", got)
	}
}

func TestDelegateEntryRoundTripPreservesExplicitEmptyRoles(t *testing.T) {
	original := DelegateEntry{Identity: "agent", Roles: []string{}}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped DelegateEntry
	if err := json.Unmarshal(b, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.Roles == nil {
		t.Fatalf("explicit empty roles collapsed to inheritance: %s", b)
	}
}

func TestLoadAuthConfigRejectsReservedIdentityNamesAndUnknownDelegates(t *testing.T) {
	for _, body := range []string{
		`{"identities":[{"name":"hand@written"}]}`,
		`{"identities":[{"name":"has/slash"}]}`,
		`{"identities":[{"name":"adil","delegates":[{"identity":"missing"}]}]}`,
	} {
		if _, err := LoadAuthConfig(writeAuth(t, body)); err == nil {
			t.Fatalf("accepted invalid config: %s", body)
		}
	}
}
