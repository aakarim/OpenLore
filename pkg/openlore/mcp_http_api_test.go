package openlore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
)

func newTestAPI(t *testing.T) http.Handler {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	fs := NewDirFS(dir, config.FilesConfig{})
	server := NewMCPServer(fs)

	api := NewMCPHTTPAPI(server, func(context.Context) *shell.Shell { return shell.NewShell(fs) })
	return api.Handler("/api")
}

func TestMCPHTTPAPI_Shell(t *testing.T) {
	h := newTestAPI(t)

	body := strings.NewReader(`{"command":"cat /hello.txt"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/shell", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp toolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected is_error=true: %q", resp.Output)
	}
	if !strings.Contains(resp.Output, "world") {
		t.Fatalf("output %q does not contain %q", resp.Output, "world")
	}
}

func TestMCPHTTPAPI_ShellMissingCommand(t *testing.T) {
	h := newTestAPI(t)

	req := httptest.NewRequest(http.MethodPost, "/api/shell", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPHTTPAPI_ListCommands(t *testing.T) {
	h := newTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/commands", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp toolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !strings.Contains(resp.Output, "Available commands:") {
		t.Fatalf("output %q missing command listing", resp.Output)
	}
	if !strings.Contains(resp.Output, "cat") {
		t.Fatalf("output %q does not list expected command 'cat'", resp.Output)
	}
}

func TestMCPHTTPAPI_MethodNotAllowed(t *testing.T) {
	h := newTestAPI(t)

	// GET on /api/shell (which only accepts POST) should not match.
	req := httptest.NewRequest(http.MethodGet, "/api/shell", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for GET /api/shell, got 200")
	}
}
