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
		if err := manager.SetCookie(rec, "alice"); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/settings/permissions", nil)
		req.AddCookie(rec.Result().Cookies()[0])
		session, ok := manager.ValidateRequest(req)
		if !ok {
			t.Fatal("issued session did not validate")
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
