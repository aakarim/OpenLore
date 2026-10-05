package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
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
// instructions follow the configured SSH port rather than the 2222 default.
func TestAgentsSkillUsesEffectiveSSHTarget(t *testing.T) {
	s, err := NewServer("", config.WithDataDir(t.TempDir()), config.WithPort(2223), config.WithHTTPPort(8081))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	var out bytes.Buffer
	if code := cmds.Registry["agents"](nil, nil, &out, io.Discard, nil); code != 0 {
		t.Fatalf("agents exited %d", code)
	}
	for _, want := range []string{"ssh -p 2223 localhost\n", "sshfs -p 2223 localhost:/ "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("agents output missing %q", want)
		}
	}
	if strings.Contains(out.String(), "2222") || strings.Contains(out.String(), "{{") {
		t.Errorf("agents output has stale connection details:\n%s", out.String())
	}
}

func TestSSHTargetPlaceholderAllowsInnerWhitespace(t *testing.T) {
	in := "ssh {{ssh_target}}; ssh {{ ssh_target }}; ssh {{\tssh_target  }}; {{ ssh_targets }}"
	want := "ssh X; ssh X; ssh X; {{ ssh_targets }}"
	if got := sshTargetPlaceholder.ReplaceAllLiteralString(in, "X"); got != want {
		t.Fatalf("got %q, want %q", got, want)
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
