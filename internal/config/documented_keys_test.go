package config

import (
	"os"
	"path/filepath"
	"strings"
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

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestExplicitZeroIsNotTreatedAsOmitted pins that an explicit 0 for a setting
// that cannot be zero is rejected instead of silently replaced by the default.
func TestExplicitZeroIsNotTreatedAsOmitted(t *testing.T) {
	for _, tc := range []struct{ yaml, key string }{
		{"port: 0\n", "port"},
		{"max_jobs: 0\n", "max_jobs"},
		{"analytics:\n  pipeline:\n    buffer: 0\n", "analytics.pipeline.buffer"},
		{"analytics:\n  index:\n    workers: -1\n", "analytics.index.workers"},
	} {
		file := writeTestConfig(t, tc.yaml)
		if _, err := New(WithConfigFile(file)); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%q: err = %v, want an error naming %s", tc.yaml, err, tc.key)
		}
		if _, err := New(WithEmbeddedConfig([]byte(tc.yaml), "")); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("embedded %q: err = %v, want an error naming %s", tc.yaml, err, tc.key)
		}
	}

	cfg, err := New(WithConfigFile(writeTestConfig(t, "port: 2200\nmax_jobs: 3\n")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 2200 || cfg.MaxJobs != 3 {
		t.Fatalf("port=%d max_jobs=%d, want 2200 and 3", cfg.Port, cfg.MaxJobs)
	}
}

func TestPortRangesValidated(t *testing.T) {
	for _, opt := range []Option{WithPort(0), WithPort(70000), WithMetricsPort(-1), WithHTTPPort(65536)} {
		if _, err := New(opt); err == nil {
			t.Error("expected out-of-range port to be rejected")
		}
	}
	if _, err := New(WithMetricsPort(0), WithHTTPPort(0)); err != nil {
		t.Fatalf("0 must disable metrics and HTTP: %v", err)
	}
}
