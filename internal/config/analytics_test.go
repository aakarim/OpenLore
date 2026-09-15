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

func TestAnalyticsRetentionAcceptsDays(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("analytics:\n  log:\n    retention: 90d\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Analytics.Log.Retention != 90*24*time.Hour {
		t.Fatalf("retention = %s", cfg.Analytics.Log.Retention)
	}
}

func TestAnalyticsPhaseThreeDriversConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	contents := "analytics:\n  ship:\n    remote: s3\n    s3:\n      endpoint: https://objects.example.test\n      region: eu-west-2\n      bucket: metrics\n      prefix: prod/openlore\n      path_style: true\n  aggregations:\n    store: sqlite\n  history:\n    retention: 365d\n"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Analytics.Ship.Remote != "s3" || cfg.Analytics.Ship.S3.Bucket != "metrics" || cfg.Analytics.Ship.S3.Region != "eu-west-2" || !cfg.Analytics.Ship.S3.PathStyle || cfg.Analytics.Aggregations.Store != "sqlite" || cfg.Analytics.History.Retention != 365*24*time.Hour {
		t.Fatalf("unexpected phase 3 analytics config: %#v", cfg.Analytics)
	}
}
