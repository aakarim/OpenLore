package httpserver

import (
	"os"
	"testing"
)

func TestMTLSRequiresLocalTLSTermination(t *testing.T) {
	if _, err := New(os.DirFS("."), Config{ClientCABundle: "client-ca.pem"}); err == nil {
		t.Fatal("accepted mTLS CA bundle without a TLS listener")
	}
}
