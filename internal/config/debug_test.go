package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDebugConfig(t *testing.T) {
	cfg, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Debug {
		t.Fatal("debug logging must be disabled by default")
	}

	data := []byte("debug: true\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "openlore.yml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	fileCfg, err := New(WithConfigFile(path))
	if err != nil || !fileCfg.Debug {
		t.Fatalf("file debug = %t, err = %v", fileCfg.Debug, err)
	}
	embeddedCfg, err := New(WithEmbeddedConfig(data, ""))
	if err != nil || !embeddedCfg.Debug {
		t.Fatalf("embedded debug = %t, err = %v", embeddedCfg.Debug, err)
	}
}
