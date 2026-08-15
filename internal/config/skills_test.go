package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillsPluginConfigDefaultFileAndEmbedded(t *testing.T) {
	cfg, err := New()
	if err != nil || cfg.Plugins.Skills.Enabled {
		t.Fatalf("default skills config = %+v, %v", cfg.Plugins.Skills, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "openlore.yml")
	data := []byte("plugins:\n  skills:\n    enabled: true\n")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileCfg, err := New(WithConfigFile(p))
	if err != nil || !fileCfg.Plugins.Skills.Enabled {
		t.Fatalf("file config = %+v, %v", fileCfg.Plugins.Skills, err)
	}
	embeddedCfg, err := New(WithEmbeddedConfig(data, ""))
	if err != nil || !embeddedCfg.Plugins.Skills.Enabled {
		t.Fatalf("embedded config = %+v, %v", embeddedCfg.Plugins.Skills, err)
	}
}

func TestSkillsPluginEqualRemoteDurationsBothApply(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "openlore.yml")
	data := []byte("plugins:\n  skills:\n    enabled: true\n    remote_check_ttl: 5s\n    remote_timeout: 5s\n")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(p))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins.Skills.RemoteCheckTTL != 5*time.Second || cfg.Plugins.Skills.RemoteTimeout != 5*time.Second {
		t.Fatalf("skills config = %+v", cfg.Plugins.Skills)
	}
}
