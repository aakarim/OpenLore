package config

import (
	"os"
	"path/filepath"
	"testing"
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
