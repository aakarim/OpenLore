package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestAgentsSkillUsesEffectiveSSHTarget pins that generated connection
// instructions follow each server's configured SSH port rather than the 2222
// default, even though skill commands live in the process-global registry.
func TestAgentsSkillUsesEffectiveSSHTarget(t *testing.T) {
	newServer := func(port int) *Server {
		s, err := NewServer("", config.WithDataDir(t.TempDir()), config.WithPort(port))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
		return s
	}
	first := newServer(2223)
	second := newServer(2224)
	for _, tc := range []struct {
		server *Server
		want   string
	}{{first, "-p 2223 localhost"}, {second, "-p 2224 localhost"}} {
		var out, errOut bytes.Buffer
		if code := tc.server.buildSessionShell(Identity{}).ExecPipeline("agents", &out, &errOut, nil); code != 0 {
			t.Fatalf("agents exited %d: %s", code, errOut.String())
		}
		for _, want := range []string{"ssh " + tc.want + "\n", "sshfs " + tc.want + ":/ "} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("agents output missing %q", want)
			}
		}
		if strings.Contains(out.String(), "2222") || strings.Contains(out.String(), "{{") {
			t.Errorf("agents output has stale connection details:\n%s", out.String())
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
