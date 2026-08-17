package openlore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrRefreshReuse signals that an already-used refresh token was presented — a
// theft indicator. The store revokes the whole chain when this happens.
var ErrRefreshReuse = errors.New("refresh token reuse detected")

// ErrRefreshInvalid signals an unknown or expired refresh token.
var ErrRefreshInvalid = errors.New("invalid refresh token")

// RefreshToken is a stateful, revocable credential. Tokens in the same ChainID
// descend from one login; rotation issues a new token in the chain and marks
// the old one used, so re-presenting a used token reveals theft.
type RefreshToken struct {
	Token     string    `json:"token"`
	Subject   string    `json:"subject"`
	Actor     string    `json:"actor,omitempty"`
	Scope     string    `json:"scope"`
	ChainID   string    `json:"chain_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

// RefreshTokenStore persists refresh tokens with rotation and reuse detection.
// The flat-file default lives in DataDir; knowledge-backend supplies a SQLite
// implementation (docs/mcp-bearer-auth.md §9).
type RefreshTokenStore interface {
	// Save stores a newly issued refresh token.
	Save(rt RefreshToken) error
	// Lookup returns the token if present.
	Lookup(token string) (RefreshToken, bool, error)
	// Rotate consumes oldToken and stores newToken (same chain) atomically. If
	// oldToken was already used it revokes the whole chain and returns
	// ErrRefreshReuse; if unknown/expired it returns ErrRefreshInvalid.
	Rotate(oldToken string, newToken RefreshToken) error
	// RevokeChain deletes every token descending from one login.
	RevokeChain(chainID string) error
}

// fileRefreshStore is a mutex-guarded JSON-file RefreshTokenStore.
type fileRefreshStore struct {
	mu     sync.Mutex
	path   string
	tokens map[string]RefreshToken
}

func newFileRefreshStore(path string) (*fileRefreshStore, error) {
	s := &fileRefreshStore{path: path, tokens: map[string]RefreshToken{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.tokens); err != nil {
			return nil, fmt.Errorf("parsing refresh store %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading refresh store %s: %w", path, err)
	}
	return s, nil
}

// persist writes the current map to disk. Caller must hold the mutex.
func (s *fileRefreshStore) persist() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.tokens)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *fileRefreshStore) Save(rt RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[rt.Token] = rt
	return s.persist()
}

func (s *fileRefreshStore) Lookup(token string) (RefreshToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.tokens[token]
	return rt, ok, nil
}

func (s *fileRefreshStore) Rotate(oldToken string, newToken RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.tokens[oldToken]
	if !ok {
		return ErrRefreshInvalid
	}
	if old.Used {
		// Reuse of a rotated token → theft. Revoke the whole chain.
		s.revokeChainLocked(old.ChainID)
		if err := s.persist(); err != nil {
			return err
		}
		return ErrRefreshReuse
	}
	if !old.ExpiresAt.IsZero() && old.ExpiresAt.Before(time.Now()) {
		delete(s.tokens, oldToken)
		if err := s.persist(); err != nil {
			return err
		}
		return ErrRefreshInvalid
	}

	old.Used = true
	s.tokens[oldToken] = old
	s.tokens[newToken.Token] = newToken
	return s.persist()
}

func (s *fileRefreshStore) RevokeChain(chainID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeChainLocked(chainID)
	return s.persist()
}

func (s *fileRefreshStore) revokeChainLocked(chainID string) {
	for tok, rt := range s.tokens {
		if rt.ChainID == chainID {
			delete(s.tokens, tok)
		}
	}
}
