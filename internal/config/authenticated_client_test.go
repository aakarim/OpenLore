package config

import "testing"

func TestMTLSConfig(t *testing.T) {
	cfg, err := New(WithEmbeddedConfig([]byte(`
auth:
  mtls:
    ca_bundle: /etc/openlore/client-ca.pem
`), ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MTLS.CABundle != "/etc/openlore/client-ca.pem" {
		t.Fatalf("ca_bundle=%q", cfg.MTLS.CABundle)
	}
}
