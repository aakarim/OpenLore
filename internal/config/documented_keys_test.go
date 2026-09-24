package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestZeroPortsInFileDisableServers pins that `metrics_port: 0` and
// `http_port: 0` in openlore.yml disable those servers, as the example config
// documents, instead of being indistinguishable from "unset".
func TestZeroPortsInFileDisableServers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("metrics_port: 0\nhttp_port: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsPort != 0 || cfg.HTTPPort != 0 {
		t.Fatalf("metrics_port=%d http_port=%d, want both 0", cfg.MetricsPort, cfg.HTTPPort)
	}

	unset := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(unset, []byte("port: 2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = New(WithConfigFile(unset))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsPort != 3000 || cfg.HTTPPort != 8080 {
		t.Fatalf("unset ports should keep defaults, got metrics=%d http=%d", cfg.MetricsPort, cfg.HTTPPort)
	}
}

// TestAggregationRefreshIntervalKey pins the documented snake_case key; the
// untagged struct field used to make yaml.v3 look for `refreshinterval`.
func TestAggregationRefreshIntervalKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("analytics:\n  aggregations:\n    refresh_interval: 90s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Analytics.Aggregations.RefreshInterval != 90*time.Second {
		t.Fatalf("refresh_interval = %s, want 90s", cfg.Analytics.Aggregations.RefreshInterval)
	}
}
