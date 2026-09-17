package openlore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
)

func TestOpenLoreMetadataDescribesEnabledPublicRoutes(t *testing.T) {
	s := &Server{config: config.Config{
		ExternalSSHPort: 22,
		HostKeyPath:     "/tmp/openlore-host-key",
		MCPEnabled:      true,
		MCPPath:         "/mcp",
		APIEnabled:      true,
		APIPath:         "/api",
	}}
	response := httptest.NewRecorder()
	s.openLoreMetadata(response, httptest.NewRequest(http.MethodGet, "/.well-known/openlore", nil))

	var metadata struct {
		SSH struct {
			Port int `json:"port"`
		} `json:"ssh"`
		Routes map[string]string `json:"routes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || metadata.SSH.Port != 22 {
		t.Fatalf("metadata response = %d %+v", response.Code, metadata)
	}
	for route, want := range map[string]string{"mcp": "/mcp", "api": "/api/", "host_key": "/host-key", "legal": "/legal/"} {
		if metadata.Routes[route] != want {
			t.Errorf("route %q = %q, want %q", route, metadata.Routes[route], want)
		}
	}
}

func TestStaticAppAssetSupportsGetAndHead(t *testing.T) {
	h := staticAppAsset("font/woff2", []byte("font"))
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(method, "/assets/openlore/outfit.woff2", nil))
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "font/woff2" {
			t.Fatalf("%s asset = %d %q", method, response.Code, response.Header().Get("Content-Type"))
		}
		if method == http.MethodGet && response.Body.String() != "font" {
			t.Fatalf("GET asset body = %q", response.Body.String())
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("HEAD asset returned a body")
		}
	}
}
