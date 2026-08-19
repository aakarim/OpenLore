package passkeys

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "openlore_session"

// SessionManager handles HMAC-signed session cookies.
type SessionManager struct {
	key []byte
	ttl time.Duration
}

// NewSessionManager creates a session manager keyed from the given secret.
func NewSessionManager(key []byte, ttl time.Duration) *SessionManager {
	// Derive a session-specific key so raw host key material isn't used directly.
	h := sha256.Sum256(append([]byte("openlore-passkey-session:"), key...))
	return &SessionManager{key: h[:], ttl: ttl}
}

// SessionInfo holds the decoded session values.
type SessionInfo struct {
	Identity  string
	ID        string
	ExpiresAt time.Time
}

// SetCookie creates and sets a signed session cookie on the response.
func (sm *SessionManager) SetCookie(w http.ResponseWriter, identity string) error {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	expiry := time.Now().Add(sm.ttl)
	payload := fmt.Sprintf("%s:%s:%d", identity, hex.EncodeToString(nonce), expiry.Unix())
	sig := sm.sign(payload)
	value := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiry,
	})
	return nil
}

// ValidateRequest checks the session cookie and returns session info if valid.
func (sm *SessionManager) ValidateRequest(r *http.Request) (*SessionInfo, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}

	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}

	payload := string(payloadBytes)
	if !sm.verify(payload, sigBytes) {
		return nil, false
	}

	expirySep := strings.LastIndex(payload, ":")
	if expirySep < 0 {
		return nil, false
	}
	idSep := strings.LastIndex(payload[:expirySep], ":")
	if idSep < 0 {
		return nil, false
	}
	identity := payload[:idSep]
	sessionID := payload[idSep+1 : expirySep]
	if len(sessionID) != 64 {
		return nil, false
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		return nil, false
	}
	expiryUnix, err := strconv.ParseInt(payload[expirySep+1:], 10, 64)
	if err != nil {
		return nil, false
	}

	expiry := time.Unix(expiryUnix, 0)
	if time.Now().After(expiry) {
		return nil, false
	}

	return &SessionInfo{Identity: identity, ID: sessionID, ExpiresAt: expiry}, true
}

// CSRFToken returns a token bound to one authenticated browser session.
func (sm *SessionManager) CSRFToken(session *SessionInfo) string {
	return base64.RawURLEncoding.EncodeToString(sm.sign("csrf:" + session.ID))
}

// ValidateCSRF checks a submitted token against the authenticated session.
func (sm *SessionManager) ValidateCSRF(session *SessionInfo, token string) bool {
	got, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && hmac.Equal(got, sm.sign("csrf:"+session.ID))
}

// ClearCookie removes the session cookie.
func (sm *SessionManager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func (sm *SessionManager) sign(payload string) []byte {
	mac := hmac.New(sha256.New, sm.key)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (sm *SessionManager) verify(payload string, sig []byte) bool {
	expected := sm.sign(payload)
	return hmac.Equal(expected, sig)
}
