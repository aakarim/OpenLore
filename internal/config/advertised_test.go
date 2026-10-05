package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAdvertisedAddressFollowsEffectivePorts pins that generated connection
// details derive from the configured ports, not the built-in 2222/8080.
func TestAdvertisedAddressFollowsEffectivePorts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("port: 2223\nhttp_port: 8081\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SSHTarget(); got != "-p 2223 localhost" {
		t.Errorf("SSHTarget = %q", got)
	}
	if got := cfg.HTTPBaseURL(); got != "http://localhost:8081" {
		t.Errorf("HTTPBaseURL = %q", got)
	}
	if cfg.Passkeys.RPID != "localhost" || len(cfg.Passkeys.RPOrigins) != 1 || cfg.Passkeys.RPOrigins[0] != "http://localhost:8081" {
		t.Errorf("passkeys = %+v", cfg.Passkeys)
	}

	cfg, err = New(WithConfigFile(file), WithPort(2224), WithHTTPPort(8082))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSHTarget() != "-p 2224 localhost" || cfg.HTTPBaseURL() != "http://localhost:8082" {
		t.Errorf("flag overrides: ssh=%q http=%q", cfg.SSHTarget(), cfg.HTTPBaseURL())
	}
}

func TestExternalURLIsAdvertised(t *testing.T) {
	cfg, err := New(WithEmbeddedConfig([]byte("port: 2222\nexternal_ssh_port: 22\nexternal_url: https://docs.example.com/\n"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SSHTarget(); got != "docs.example.com" {
		t.Errorf("SSHTarget = %q", got)
	}
	if got := cfg.LocalSSHTarget(); got != "-p 2222 localhost" {
		t.Errorf("LocalSSHTarget = %q", got)
	}
	if got := cfg.HTTPBaseURL(); got != "https://docs.example.com" {
		t.Errorf("HTTPBaseURL = %q", got)
	}
	if cfg.Passkeys.RPID != "docs.example.com" || cfg.Passkeys.RPOrigins[0] != "https://docs.example.com" {
		t.Errorf("passkeys = %+v", cfg.Passkeys)
	}

	// Explicit passkey settings win over the derived defaults.
	cfg, err = New(WithEmbeddedConfig([]byte("external_url: https://docs.example.com\npasskeys:\n  rp_id: example.com\n  rp_origins: [\"https://example.com\"]\n"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Passkeys.RPID != "example.com" || cfg.Passkeys.RPOrigins[0] != "https://example.com" || cfg.HTTPBaseURL() != "https://docs.example.com" {
		t.Errorf("explicit passkeys = %+v base=%q", cfg.Passkeys, cfg.HTTPBaseURL())
	}

	if _, err := New(WithEmbeddedConfig([]byte("external_url: docs.example.com\n"), "")); err == nil {
		t.Error("expected error for external_url without scheme")
	}
}
