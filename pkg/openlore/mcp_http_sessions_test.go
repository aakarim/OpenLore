package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
)

func newSessionTestAPI(t *testing.T) (*MCPHTTPAPI, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/docs", 0o755); err != nil {
		t.Fatal(err)
	}
	fs := NewDirFS(dir, config.FilesConfig{})
	api := NewMCPHTTPAPI(NewMCPServer(fs), func(context.Context) *shell.Shell {
		return shell.NewShell(fs)
	})
	t.Cleanup(func() {
		api.sessions.mu.Lock()
		defer api.sessions.mu.Unlock()
		for _, session := range api.sessions.sessions {
			session.expiry.Stop()
		}
	})
	return api, api.Handler("/api")
}

func sessionRequest(t *testing.T, handler http.Handler, identity Identity, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), identity))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func createSession(t *testing.T, handler http.Handler, identity Identity) createSessionResponse {
	t.Helper()
	w := sessionRequest(t, handler, identity, http.MethodPost, "/api/sessions", `{"client_ref":"amp-thread-1"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, body = %s", w.Code, w.Body.String())
	}
	var response createSessionResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestMCPHTTPSessionLifecyclePreservesShellState(t *testing.T) {
	_, handler := newSessionTestAPI(t)
	identity := Identity{
		IdentityName: "adil",
		Principal:    AuthenticatedPrincipal{Subject: "adil"},
		Attribution:  Attribution{Principal: "adil"},
		Scopes:       []string{ScopeFull},
	}
	created := createSession(t, handler, identity)
	if len(created.ID) != 48 {
		t.Fatalf("session ID length = %d, want 48", len(created.ID))
	}
	if created.IdleTimeoutSeconds != int64(defaultHTTPSessionTTL/time.Second) {
		t.Fatalf("idle timeout = %d, want %d", created.IdleTimeoutSeconds, int64(defaultHTTPSessionTTL/time.Second))
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	path := "/api/sessions/" + created.ID
	if w := sessionRequest(t, handler, identity, http.MethodPost, path+"/shell", `{"command":"cd /docs"}`); w.Code != http.StatusOK {
		t.Fatalf("cd status = %d, body = %s", w.Code, w.Body.String())
	}
	w := sessionRequest(t, handler, identity, http.MethodPost, path+"/shell", `{"command":"pwd"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("pwd status = %d, body = %s", w.Code, w.Body.String())
	}
	var result toolResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(result.Output); got != "/docs" {
		t.Fatalf("pwd output = %q, want /docs", got)
	}

	if w := sessionRequest(t, handler, identity, http.MethodPost, path+"/touch", `{}`); w.Code != http.StatusNoContent {
		t.Fatalf("touch status = %d, body = %s", w.Code, w.Body.String())
	}
	if w := sessionRequest(t, handler, identity, http.MethodDelete, path, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}
	if w := sessionRequest(t, handler, identity, http.MethodPost, path+"/shell", `{"command":"pwd"}`); w.Code != http.StatusNotFound {
		t.Fatalf("shell after delete status = %d, want 404", w.Code)
	}
}

func TestMCPHTTPSessionIsBoundToIdentity(t *testing.T) {
	_, handler := newSessionTestAPI(t)
	owner := Identity{IdentityName: "owner", Principal: AuthenticatedPrincipal{Subject: "owner"}, Attribution: Attribution{Principal: "owner"}, Scopes: []string{ScopeFull}}
	other := Identity{IdentityName: "other", Principal: AuthenticatedPrincipal{Subject: "other"}, Attribution: Attribution{Principal: "other"}, Scopes: []string{ScopeFull}}
	created := createSession(t, handler, owner)
	path := "/api/sessions/" + created.ID

	if w := sessionRequest(t, handler, other, http.MethodPost, path+"/shell", `{"command":"pwd"}`); w.Code != http.StatusNotFound {
		t.Fatalf("cross-identity shell status = %d, want 404", w.Code)
	}
	if w := sessionRequest(t, handler, other, http.MethodDelete, path, ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-identity delete status = %d, want 404", w.Code)
	}
}

func TestMCPHTTPSessionExpiresAfterIdleTimeout(t *testing.T) {
	api, handler := newSessionTestAPI(t)
	identity := Identity{IdentityName: "adil", Principal: AuthenticatedPrincipal{Subject: "adil"}, Attribution: Attribution{Principal: "adil"}, Scopes: []string{ScopeFull}}
	created := createSession(t, handler, identity)

	api.sessions.mu.Lock()
	api.sessions.sessions[created.ID].lastUsedAt = time.Now().Add(-api.sessions.ttl)
	api.sessions.mu.Unlock()

	path := "/api/sessions/" + created.ID + "/shell"
	w := sessionRequest(t, handler, identity, http.MethodPost, path, `{"command":"pwd"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expired session status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestMCPHTTPSessionRejectsInvalidCreateBody(t *testing.T) {
	_, handler := newSessionTestAPI(t)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString("not json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d, want 400", w.Code)
	}
}
