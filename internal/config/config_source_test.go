package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigSourcePrecedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("debug: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	embedded := []byte("port: 3333\n")

	t.Run("file replaces embedded config", func(t *testing.T) {
		cfg, err := New(WithConfigFile(file), WithEmbeddedConfig(embedded, "embedded motd"))
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Debug {
			t.Fatal("file config was not applied")
		}
		if cfg.Port != 2222 {
			t.Fatalf("port = %d, want default 2222 (embedded config must be ignored)", cfg.Port)
		}
		if cfg.MOTD != "" {
			t.Fatalf("motd = %q, want empty (embedded fallback must be ignored)", cfg.MOTD)
		}
	})

	t.Run("embedded config applies without file", func(t *testing.T) {
		cfg, err := New(WithConfigFile(filepath.Join(t.TempDir(), "missing.yml")), WithEmbeddedConfig(embedded, ""))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != 3333 {
			t.Fatalf("port = %d, want embedded value 3333", cfg.Port)
		}
	})

	t.Run("defaults apply without file or embedded config", func(t *testing.T) {
		cfg, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != 2222 {
			t.Fatalf("port = %d, want default 2222", cfg.Port)
		}
	})
}
