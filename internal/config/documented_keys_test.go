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

func TestTracingConfiguration(t *testing.T) {
	const body = `tracing:
  enabled: false
  endpoint: https://collector.example.com/v1/traces
  service_name: docs
  sample_ratio: 0
  headers:
    Authorization: Bearer test-only
  resource_attributes:
    deployment.environment.name: staging
`
	for _, opt := range []Option{WithConfigFile(writeTestConfig(t, body)), WithEmbeddedConfig([]byte(body), "")} {
		cfg, err := New(opt)
		if err != nil {
			t.Fatal(err)
		}
		tracing := cfg.Tracing
		if tracing.Enabled == nil || *tracing.Enabled || tracing.Endpoint != "https://collector.example.com/v1/traces" ||
			tracing.ServiceName != "docs" || tracing.SampleRatio == nil || *tracing.SampleRatio != 0 ||
			tracing.Headers["Authorization"] != "Bearer test-only" || tracing.ResourceAttributes["deployment.environment.name"] != "staging" {
			t.Fatalf("tracing settings were not loaded correctly")
		}
	}
	for _, invalid := range []struct{ yaml, key string }{
		{"endpoint: ftp://collector.example.com", "tracing.endpoint"},
		{"endpoint: https:///v1/traces", "tracing.endpoint"},
		{"endpoint: https://user:secret@collector.example.com/v1/traces", "tracing.endpoint"},
		{"sample_ratio: -0.1", "tracing.sample_ratio"},
		{"sample_ratio: 1.1", "tracing.sample_ratio"},
		{"sample_ratio: .nan", "tracing.sample_ratio"},
	} {
		body := "tracing:\n  " + invalid.yaml + "\n"
		for _, opt := range []Option{WithConfigFile(writeTestConfig(t, body)), WithEmbeddedConfig([]byte(body), "")} {
			if _, err := New(opt); err == nil || !strings.Contains(err.Error(), invalid.key) {
				t.Errorf("invalid %s: err = %v, want an error naming %s", invalid.yaml, err, invalid.key)
			}
		}
	}
}

// TestExplicitZeroIsNotTreatedAsOmitted pins that an explicit non-positive
// value (0 or negative) for a setting that must be positive is rejected
// instead of silently replaced by the default.
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
