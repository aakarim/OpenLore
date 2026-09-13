package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnalyticsConfigAndExperimentalEnv(t *testing.T) {
	t.Setenv("OPENLORE_EXPERIMENTAL", "other,analytics")
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("analytics:\n  pipeline:\n    enabled: false\n  shutdown_timeout: 3s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ExperimentalEnabled("analytics") || cfg.Analytics.PipelineEnabled() || cfg.Analytics.ShutdownTimeout != 3*time.Second {
		t.Fatalf("unexpected config: %#v", cfg.Analytics)
	}
}
