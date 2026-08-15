package openlore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
)

// newScopedTestAPI builds a Server with two docsets — "public" (granted to
// guest) and "secret" — and returns the JSON HTTP API handler backed by the
// identity-scoped MCP shell. An unauthenticated caller resolves to guest, so it
// must see only the public docset.
func newScopedTestAPI(t *testing.T) http.Handler {
	t.Helper()

	publicFS := NewFSAdapter(fstest.MapFS{
		"hello.txt": {Data: []byte("public data\n")},
	})
	secretFS := NewFSAdapter(fstest.MapFS{
		"topsecret.txt": {Data: []byte("classified data\n")},
	})

	merge := NewMergeFS()
	merge.Mount("public", publicFS)
	merge.Mount("secret", secretFS)

	s := &Server{
		merge:        merge,
		authEnforced: true,
		grants:       newGrantRegistry(),
		auth: &config.AuthConfig{
			Docsets: map[string]config.DocsetSpec{
				"public": {Paths: []config.PathMapping{{Source: "/public", Display: "/public"}}, Access: config.DocsetAccess{Allow: map[string]string{"guest": "ro"}}},
				"secret": {Paths: []config.PathMapping{{Source: "/secret", Display: "/secret"}}},
			},
		},
		config: config.Config{Readonly: true},
	}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}

	server := NewMCPServer(s.merge, withMCPShellFactory(s.shellForContext))
	return NewMCPHTTPAPI(server).Handler("/api")
}

// runShell posts a command to the JSON API and returns the tool output.
func runShell(t *testing.T, h http.Handler, command string) toolResponse {
	t.Helper()
	body, _ := json.Marshal(shellRequest{Command: command})
	req := httptest.NewRequest(http.MethodPost, "/api/shell", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp toolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return resp
}

func TestMCPScoping_AnonymousReadsDefaultDocset(t *testing.T) {
	h := newScopedTestAPI(t)

	resp := runShell(t, h, "cat /public/hello.txt")
	if !strings.Contains(resp.Output, "public data") {
		t.Fatalf("expected to read public docset, got %q", resp.Output)
	}
}

func TestMCPScoping_AnonymousCannotReadNonDefaultDocset(t *testing.T) {
	h := newScopedTestAPI(t)

	resp := runShell(t, h, "cat /secret/topsecret.txt")
	if strings.Contains(resp.Output, "classified data") {
		t.Fatalf("anonymous caller must NOT read the secret docset; got %q", resp.Output)
	}
}

func TestMCPScoping_AnonymousListingHidesNonDefaultDocset(t *testing.T) {
	h := newScopedTestAPI(t)

	resp := runShell(t, h, "ls /")
	if !strings.Contains(resp.Output, "public") {
		t.Fatalf("expected public docset in listing, got %q", resp.Output)
	}
	if strings.Contains(resp.Output, "secret") {
		t.Fatalf("secret docset must be absent from anonymous listing; got %q", resp.Output)
	}
}

func TestMCPScoping_AnonymousIsReadOnly(t *testing.T) {
	h := newScopedTestAPI(t)

	// A write verb must be unavailable to a read-only anonymous session.
	resp := runShell(t, h, "echo pwned > /public/hello.txt")
	if !strings.Contains(resp.Output, "exit code") && !strings.Contains(resp.Output, "read-only") && !strings.Contains(resp.Output, "not found") {
		t.Fatalf("expected write to be rejected for read-only anonymous session; got %q", resp.Output)
	}

	// And the file is unchanged.
	after := runShell(t, h, "cat /public/hello.txt")
	if strings.Contains(after.Output, "pwned") {
		t.Fatalf("read-only session must not modify files; got %q", after.Output)
	}
}
