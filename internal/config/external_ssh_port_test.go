package config

import "testing"

func TestWithExternalSSHPort(t *testing.T) {
	cfg, err := New(WithExternalSSHPort(31415))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExternalSSHPort != 31415 {
		t.Fatalf("ExternalSSHPort = %d, want 31415", cfg.ExternalSSHPort)
	}
}
