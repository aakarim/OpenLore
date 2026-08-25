package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestInvalidValueTypeIsOmitted(t *testing.T) {
	data := []byte("debug: true\nport: 2323\npasskeys: true\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "openlore.yml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		load Option
	}{
		{name: "file", load: WithConfigFile(path)},
		{name: "embedded", load: WithEmbeddedConfig(data, "")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := New(tt.load)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if !cfg.Debug || cfg.Port != 2323 {
				t.Fatalf("valid settings were not retained: debug=%t port=%d", cfg.Debug, cfg.Port)
			}
			if !cfg.Passkeys.Enabled || cfg.Passkeys.RPID != "localhost" {
				t.Fatalf("invalid passkeys setting changed defaults: %+v", cfg.Passkeys)
			}
			warnings := cfg.Warnings()
			if len(warnings) != 1 || !strings.Contains(warnings[0], "cannot unmarshal !!bool `true` into config.passkeysYAML") {
				t.Fatalf("warnings = %q", warnings)
			}
		})
	}
}

func TestInvalidYAMLSyntaxStillFails(t *testing.T) {
	if _, err := New(WithEmbeddedConfig([]byte("passkeys: [\n"), "")); err == nil {
		t.Fatal("invalid YAML syntax was accepted")
	}
}
