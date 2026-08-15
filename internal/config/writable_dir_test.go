package config

import "testing"

func TestEmbeddedConfigParsesWritableDir(t *testing.T) {
	cfg, err := New(WithEmbeddedConfig([]byte("writable_dir: ./published\n"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WritableDir != "./published" {
		t.Fatalf("WritableDir = %q", cfg.WritableDir)
	}
}
