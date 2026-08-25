package config

import "testing"

func TestPasskeysDefaultEnabled(t *testing.T) {
	cfg, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Passkeys.Enabled {
		t.Fatal("passkeys must be enabled by default")
	}
	if cfg.Passkeys.RPID != "localhost" || len(cfg.Passkeys.RPOrigins) != 1 || cfg.Passkeys.RPOrigins[0] != "http://localhost:8080" {
		t.Fatalf("passkey defaults = %+v", cfg.Passkeys)
	}
}

func TestPasskeysCanBeDisabled(t *testing.T) {
	cfg, err := New(WithEmbeddedConfig([]byte("passkeys:\n  enabled: false\n"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Passkeys.Enabled {
		t.Fatal("passkeys enabled after explicit disable")
	}
}

func TestPasskeysStayEnabledWhenCustomized(t *testing.T) {
	cfg, err := New(WithEmbeddedConfig([]byte("passkeys:\n  rp_id: lore.example.com\n"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Passkeys.Enabled || cfg.Passkeys.RPID != "lore.example.com" {
		t.Fatalf("custom passkeys = %+v", cfg.Passkeys)
	}
}
