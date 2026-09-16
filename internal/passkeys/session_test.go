package passkeys

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCSRFTokenIsBoundToSession(t *testing.T) {
	manager := NewSessionManager([]byte("secret"), time.Hour)
	requestForSession := func() *SessionInfo {
		rec := httptest.NewRecorder()
		issuedID, err := manager.SetCookie(rec, "alice")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/settings/permissions", nil)
		req.AddCookie(rec.Result().Cookies()[0])
		session, ok := manager.ValidateRequest(req)
		if !ok {
			t.Fatal("issued session did not validate")
		}
		if session.ID != issuedID {
			t.Fatalf("decoded session ID = %q, want issued ID %q", session.ID, issuedID)
		}
		return session
	}

	first := requestForSession()
	second := requestForSession()
	token := manager.CSRFToken(first)
	if !manager.ValidateCSRF(first, token) {
		t.Fatal("session rejected its own CSRF token")
	}
	if manager.ValidateCSRF(second, token) {
		t.Fatal("CSRF token was accepted for another session")
	}
	if manager.ValidateCSRF(first, token+"x") {
		t.Fatal("tampered CSRF token was accepted")
	}
}
