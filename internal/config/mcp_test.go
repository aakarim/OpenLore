package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPRequireAuthOverridesKeylessPosture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openlore.yml")
	contents := []byte(`
mcp:
  require_auth: true
tokens:
  issuer: https://openlore.test
  audience: https://openlore.test
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := New(WithConfigFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowKeyless {
		t.Fatal("MCP override must not change the SSH keyless posture")
	}
	if !cfg.MCPAuthRequired() {
		t.Fatal("MCPAuthRequired() = false, want true")
	}
}

func TestMCPRequireAuthDefaultsToKeylessPosture(t *testing.T) {
	cfg, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPAuthRequired() {
		t.Fatal("MCPAuthRequired() = true for default keyless config, want false")
	}

	cfg, err = New(WithAllowKeyless(false))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCPAuthRequired() {
		t.Fatal("MCPAuthRequired() = false when keyless is disabled, want true")
	}
}

func TestMCPRequireAuthRequiresTokenIssuer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(path, []byte("mcp:\n  require_auth: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := New(WithConfigFile(path)); err == nil {
		t.Fatal("expected mcp.require_auth without tokens to be rejected")
	}
}
