package openlore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/shell"
)

const (
	defaultHTTPSessionTTL   = 30 * time.Minute
	maxHTTPSessions         = 256
	maxHTTPSessionsPerOwner = 64
)

type httpShellSessions struct {
	factory func(context.Context) *shell.Shell
	ttl     time.Duration

	mu       sync.Mutex
	sessions map[string]*httpShellSession
}

type httpShellSession struct {
	mu         sync.Mutex
	shell      *shell.Shell
	owner      string
	clientRef  string
	createdAt  time.Time
	lastUsedAt time.Time
	expiry     *time.Timer
}

type createSessionRequest struct {
	ClientRef string `json:"client_ref"`
}

type createSessionResponse struct {
	ID                 string    `json:"id"`
	CreatedAt          time.Time `json:"created_at"`
	IdleTimeoutSeconds int64     `json:"idle_timeout_seconds"`
}

func newHTTPShellSessions(factory func(context.Context) *shell.Shell) *httpShellSessions {
	return &httpShellSessions{
		factory:  factory,
		ttl:      defaultHTTPSessionTTL,
		sessions: make(map[string]*httpShellSession),
	}
}

func (s *httpShellSessions) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.factory == nil {
		writeAPIError(w, http.StatusNotImplemented, "persistent sessions are unavailable")
		return
	}

	id, err := randomHTTPSessionID()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "creating session")
		return
	}
	now := time.Now()
	owner := httpSessionOwner(r.Context())
	s.mu.Lock()
	s.pruneExpiredLocked(now)
	ownerSessions := 0
	for _, existing := range s.sessions {
		if existing.owner == owner {
			ownerSessions++
		}
	}
	if len(s.sessions) >= maxHTTPSessions || ownerSessions >= maxHTTPSessionsPerOwner {
		s.mu.Unlock()
		writeAPIError(w, http.StatusTooManyRequests, "too many active sessions")
		return
	}
	session := &httpShellSession{
		shell:      s.factory(r.Context()),
		owner:      owner,
		clientRef:  strings.TrimSpace(req.ClientRef),
		createdAt:  now,
		lastUsedAt: now,
	}
	s.sessions[id] = session
	session.expiry = time.AfterFunc(s.ttl, func() { s.expire(id, session) })
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, createSessionResponse{
		ID:                 id,
		CreatedAt:          now,
		IdleTimeoutSeconds: int64(s.ttl / time.Second),
	})
}

func (s *httpShellSessions) handleShell(w http.ResponseWriter, r *http.Request) {
	var req shellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeAPIError(w, http.StatusBadRequest, "command is required")
		return
	}

	session := s.get(r.PathValue("id"), httpSessionOwner(r.Context()), time.Now())
	if session == nil {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	session.mu.Lock()
	result := executeSessionShell(session.shell, req.Command)
	session.mu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

func (s *httpShellSessions) handleTouch(w http.ResponseWriter, r *http.Request) {
	if s.get(r.PathValue("id"), httpSessionOwner(r.Context()), time.Now()) == nil {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *httpShellSessions) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	owner := httpSessionOwner(r.Context())
	s.mu.Lock()
	session, ok := s.sessions[id]
	if ok && session.owner == owner {
		delete(s.sessions, id)
		session.expiry.Stop()
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *httpShellSessions) get(id, owner string, now time.Time) *httpShellSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	session := s.sessions[id]
	if session == nil || session.owner != owner {
		return nil
	}
	session.lastUsedAt = now
	session.expiry.Reset(s.ttl)
	return session
}

func (s *httpShellSessions) pruneExpiredLocked(now time.Time) {
	for id, session := range s.sessions {
		if now.Sub(session.lastUsedAt) >= s.ttl {
			delete(s.sessions, id)
			session.expiry.Stop()
		}
	}
}

func (s *httpShellSessions) expire(id string, expected *httpShellSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session != expected {
		return
	}
	remaining := s.ttl - time.Since(session.lastUsedAt)
	if remaining > 0 {
		session.expiry.Reset(remaining)
		return
	}
	delete(s.sessions, id)
}

func httpSessionOwner(ctx context.Context) string {
	if id, ok := ctx.Value(identityCtxKey{}).(Identity); ok {
		return strings.Join([]string{
			id.Principal.Subject,
			id.IdentityName,
			id.Attribution.String(),
			strings.Join(id.Scopes, ","),
		}, "\x00")
	}
	return "anonymous"
}

func randomHTTPSessionID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func executeSessionShell(sh *shell.Shell, command string) toolResponse {
	var stdout, stderr bytes.Buffer
	exitCode := sh.ExecPipeline(command, &stdout, &stderr, nil)
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n" + stderr.String()
	}
	if exitCode != 0 {
		output += fmt.Sprintf("\nexit code: %d", exitCode)
	}
	return toolResponse{Output: output}
}
