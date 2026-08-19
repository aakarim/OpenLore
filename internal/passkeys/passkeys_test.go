package passkeys

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestPasskeys(t *testing.T) (*Passkeys, *http.ServeMux) {
	t.Helper()
	pk, err := New(Config{
		Enabled:      true,
		RPID:         "localhost",
		RPName:       "OpenLore",
		RPOrigins:    []string{"http://localhost:8080"},
		LorePath:     "/lore",
		PasskeysFile: filepath.Join(t.TempDir(), "passkeys.json"),
		SessionTTL:   24 * time.Hour,
	}, []byte("test-session-key"), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	pk.RegisterHTTPHandlers(mux)
	return pk, mux
}

func TestLoginStatusEmpty(t *testing.T) {
	_, mux := newTestPasskeys(t)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/passkey/login/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}
	var body struct {
		Count       int  `json:"count"`
		HasPasskeys bool `json:"has_passkeys"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 0 || body.HasPasskeys {
		t.Fatalf("expected empty store, got %+v", body)
	}
}

func TestPasskeyPagesUseSharedStylesheet(t *testing.T) {
	pk, mux := newTestPasskeys(t)
	pr, err := pk.pending.Create("alice", "MacBook")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/passkey/login", "/passkey/r/" + pr.Token} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		body := rec.Body.String()
		if !strings.Contains(body, `href="/assets/openlore.css"`) {
			t.Errorf("%s does not use the shared stylesheet", target)
		}
		if strings.Contains(body, "<style>") {
			t.Errorf("%s still contains inline styles", target)
		}
	}
}

func TestLoginBeginSetsCookieAndChallenge(t *testing.T) {
	_, mux := newTestPasskeys(t)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/passkey/login/begin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rr.Code, rr.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == loginCookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("expected login cookie to be set")
	}
	if !strings.Contains(rr.Body.String(), "\"challenge\"") {
		t.Fatalf("expected challenge in body, got %s", rr.Body.String())
	}
}

func TestRegisterFlow(t *testing.T) {
	pk, mux := newTestPasskeys(t)

	// Unknown token -> 404.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/passkey/r/bogus", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("bogus token: code = %d", rr.Code)
	}

	// Create a real pending registration and confirm the page + info resolve.
	pr, err := pk.pending.Create("alice", "MacBook")
	if err != nil {
		t.Fatalf("pending.Create: %v", err)
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/passkey/r/"+pr.Token, nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Register Passkey") {
		t.Fatalf("register page: code=%d body=%.80s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/passkey/r/"+pr.Token+"/info", nil))
	var info struct {
		Name     string `json:"name"`
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("info decode: %v", err)
	}
	if info.Name != "MacBook" || info.Identity != "alice" {
		t.Fatalf("unexpected info: %+v", info)
	}
}
