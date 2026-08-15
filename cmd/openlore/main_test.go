package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	gossh "golang.org/x/crypto/ssh"
)

func policyFile(t *testing.T, auth config.AuthConfig) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lore.json")
	b, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readPolicy(t *testing.T, p string) config.AuthConfig {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var auth config.AuthConfig
	if err := json.Unmarshal(b, &auth); err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestRoleAndIdentityCommands(t *testing.T) {
	auth := config.AuthConfig{
		Roles:   map[string]config.RoleSpec{"reader": {}, "other": {}},
		Docsets: map[string]config.DocsetSpec{"docs": {Paths: []config.PathMapping{{Source: "/docs"}}}, "home": {Paths: []config.PathMapping{{Source: "/home"}}}},
	}
	p := policyFile(t, auth)
	role := func(args ...string) {
		t.Helper()
		args = append(args, "-auth", p)
		if err := runRoleCommand(args, &bytes.Buffer{}); err != nil {
			t.Fatalf("role %v: %v", args, err)
		}
	}
	identity := func(args ...string) {
		t.Helper()
		args = append(args, "-auth", p)
		if err := runIdentityCommand(args, &bytes.Buffer{}); err != nil {
			t.Fatalf("identity %v: %v", args, err)
		}
	}

	t.Run("role add with comment", func(t *testing.T) {
		role("add", "-name", "editor", "-comment", "Edits docs")
		if got := readPolicy(t, p).Roles["editor"].Comment; got != "Edits docs" {
			t.Fatalf("comment = %q", got)
		}
	})
	t.Run("grant overwrite revoke deny undeny", func(t *testing.T) {
		role("grant", "-role", "editor", "-docset", "docs", "-grant", "ro")
		role("grant", "-role", "editor", "-docset", "docs", "-grant", "rw")
		if got := readPolicy(t, p).Docsets["docs"].Access.Allow["editor"]; got != "rw" {
			t.Fatalf("grant = %q", got)
		}
		role("deny", "-role", "editor", "-docset", "docs")
		if got := readPolicy(t, p).Docsets["docs"].Access.Deny; len(got) != 1 || got[0] != "editor" {
			t.Fatalf("deny = %v", got)
		}
		role("undeny", "-role", "editor", "-docset", "docs")
		role("revoke", "-role", "editor", "-docset", "docs")
		ds := readPolicy(t, p).Docsets["docs"]
		if len(ds.Access.Deny) != 0 || ds.Access.Allow["editor"] != "" {
			t.Fatalf("access = %+v", ds.Access)
		}
	})
	t.Run("capabilities", func(t *testing.T) {
		role("capability", "allow", "-role", "editor", "-capability", "spawn")
		role("capability", "deny", "-role", "editor", "-capability", "spawn")
		r := readPolicy(t, p).Roles["editor"]
		if len(r.Allow.Capabilities) != 1 || len(r.Deny.Capabilities) != 1 {
			t.Fatalf("role = %+v", r)
		}
		role("capability", "remove", "-effect", "allow", "-role", "editor", "-capability", "spawn")
		role("capability", "remove", "-effect", "deny", "-role", "editor", "-capability", "spawn")
		r = readPolicy(t, p).Roles["editor"]
		if len(r.Allow.Capabilities)+len(r.Deny.Capabilities) != 0 {
			t.Fatalf("role = %+v", r)
		}
	})
	t.Run("identity roles", func(t *testing.T) {
		identity("add", "-name", "alice", "-role", "reader", "-role", "editor")
		identity("add", "-name", "nobody")
		a := readPolicy(t, p)
		if len(a.Identities) != 2 || len(a.Identities[0].Roles) != 2 || len(a.Identities[1].Roles) != 0 {
			t.Fatalf("identities = %+v", a.Identities)
		}
		identity("role", "add", "-identity", "nobody", "-role", "other")
		identity("role", "remove", "-identity", "nobody", "-role", "other")
		if got := readPolicy(t, p).Identities[1].Roles; len(got) != 0 {
			t.Fatalf("roles = %v", got)
		}
	})
	t.Run("guest docset", func(t *testing.T) {
		role("grant", "-role", "guest", "-docset", "docs", "-grant", "ro")
		if got := readPolicy(t, p).Docsets["docs"].Access.Allow["guest"]; got != "ro" {
			t.Fatalf("grant = %q", got)
		}
		role("revoke", "-role", "guest", "-docset", "docs")
		if _, ok := readPolicy(t, p).Docsets["docs"].Access.Allow["guest"]; ok {
			t.Fatal("guest grant remains")
		}
	})
}

func TestCommandPolicyValidationFailures(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{7}, ed25519.SeedSize)))
	key, err := gossh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyText := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key)))
	tests := []struct {
		name     string
		auth     config.AuthConfig
		run      func(string) error
		contains string
	}{
		{"duplicate identity", config.AuthConfig{Roles: map[string]config.RoleSpec{}, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "alice"}}}, func(p string) error {
			return runIdentityCommand([]string{"add", "-name", "alice", "-auth", p}, &bytes.Buffer{})
		}, "already exists"},
		{"normalized key", config.AuthConfig{Roles: map[string]config.RoleSpec{}, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "alice", PublicKey: keyText + " old"}}}, func(p string) error {
			return runIdentityCommand([]string{"add", "-name", "bob", "-key", keyText + " new", "-auth", p}, &bytes.Buffer{})
		}, "share public key"},
		{"home deny", config.AuthConfig{Roles: map[string]config.RoleSpec{"r": {}}, Docsets: map[string]config.DocsetSpec{"home": {Paths: []config.PathMapping{{Source: "/home"}}, Access: config.DocsetAccess{Deny: []string{"r"}}}}}, func(p string) error {
			return runIdentityCommand([]string{"add", "-name", "alice", "-home", "home", "-role", "r", "-auth", p}, &bytes.Buffer{})
		}, "denied on its home"},
		{"remove identity reference", config.AuthConfig{Roles: map[string]config.RoleSpec{"r": {}}, Docsets: map[string]config.DocsetSpec{}, Identities: []config.AuthIdentity{{Name: "alice", Roles: []string{"r"}}}}, func(p string) error {
			return runRoleCommand([]string{"remove", "-role", "r", "-auth", p}, &bytes.Buffer{})
		}, "referenced by identity"},
		{"remove docset reference", config.AuthConfig{Roles: map[string]config.RoleSpec{"r": {}}, Docsets: map[string]config.DocsetSpec{"docs": {Paths: []config.PathMapping{{Source: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"r": "ro"}}}}}, func(p string) error {
			return runRoleCommand([]string{"remove", "-role", "r", "-auth", p}, &bytes.Buffer{})
		}, "referenced by docset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := policyFile(t, tt.auth)
			before, _ := os.ReadFile(p)
			err := tt.run(p)
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("error = %v, want %q", err, tt.contains)
			}
			after, _ := os.ReadFile(p)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected command changed JSON")
			}
		})
	}
}
