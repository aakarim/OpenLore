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

// ErrRefreshReuse signals that an already-used refresh token was presented
// outside the short retry window. The store revokes the whole chain when this
// happens.
var ErrRefreshReuse = errors.New("refresh token reuse detected")

// ErrRefreshInvalid signals an unknown or expired refresh token.
var ErrRefreshInvalid = errors.New("invalid refresh token")

// ErrRefreshStaleRetry signals that an immediate retry's successor has already
// been consumed or expired. The current chain remains valid and is not revoked.
var ErrRefreshStaleRetry = errors.New("stale refresh retry")

// refreshRetryGrace lets an OAuth client safely retry a refresh request whose
// response was lost, or issue concurrent refreshes during reconnect. Both
// requests receive the same successor refresh token. Reuse after this window
// remains a theft signal and revokes the chain.
const refreshRetryGrace = 2 * time.Minute

// RefreshToken is a stateful, revocable credential. Tokens in the same ChainID
// descend from one login; rotation issues a new token in the chain and marks
// the old one used, so re-presenting a used token reveals theft.
type RefreshToken struct {
	Token      string          `json:"token"`
	Subject    string          `json:"subject"`
	Actor      string          `json:"actor,omitempty"`
	ClientID   string          `json:"client_id,omitempty"`
	ClientAuth ClientAuthLevel `json:"client_auth,omitempty"`
	Scope      string          `json:"scope"`
	ChainID    string          `json:"chain_id"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Used       bool            `json:"used"`
	RotatedAt  time.Time       `json:"rotated_at,omitempty"`
	ReplacedBy string          `json:"replaced_by,omitempty"`
}

type RefreshRotation struct {
	Token   RefreshToken
	Retried bool
}

// RefreshTokenStore persists refresh tokens with rotation and reuse detection.
// The flat-file default lives in DataDir; knowledge-backend supplies a SQLite
// implementation (docs/mcp-bearer-auth.md §9).
type RefreshTokenStore interface {
	// Save stores a newly issued refresh token.
	Save(rt RefreshToken) error
	// Lookup returns the token if present.
	Lookup(token string) (RefreshToken, bool, error)
	// Rotate consumes oldToken and stores newToken (same chain) atomically.
	// Immediate retries return the previously stored successor. Reuse outside
	// the retry window revokes the chain and returns ErrRefreshReuse;
	// unknown/expired tokens return ErrRefreshInvalid.
	Rotate(oldToken string, newToken RefreshToken) (RefreshRotation, error)
	// RevokeChain deletes every token descending from one login.
	RevokeChain(chainID string) error
	// RevokeDelegation revokes every chain issued to actor on behalf of subject.
	RevokeDelegation(subject, actor string) (int, error)
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

func (s *fileRefreshStore) Rotate(oldToken string, newToken RefreshToken) (RefreshRotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.tokens[oldToken]
	if !ok {
		return RefreshRotation{}, ErrRefreshInvalid
	}
	if old.Used {
		elapsed := time.Since(old.RotatedAt)
		if elapsed >= 0 && elapsed <= refreshRetryGrace {
			if successor, ok := s.tokens[old.ReplacedBy]; ok {
				if successor.Used || (!successor.ExpiresAt.IsZero() && successor.ExpiresAt.Before(time.Now())) {
					return RefreshRotation{}, ErrRefreshStaleRetry
				}
				return RefreshRotation{Token: successor, Retried: true}, nil
			}
			return RefreshRotation{}, ErrRefreshStaleRetry
		}
		// Reuse of a rotated token → theft. Revoke the whole chain.
		s.revokeChainLocked(old.ChainID)
		if err := s.persist(); err != nil {
			return RefreshRotation{}, err
		}
		return RefreshRotation{}, ErrRefreshReuse
	}
	if !old.ExpiresAt.IsZero() && old.ExpiresAt.Before(time.Now()) {
		delete(s.tokens, oldToken)
		if err := s.persist(); err != nil {
			return RefreshRotation{}, err
		}
		return RefreshRotation{}, ErrRefreshInvalid
	}

	old.Used = true
	old.RotatedAt = time.Now()
	old.ReplacedBy = newToken.Token
	s.tokens[oldToken] = old
	s.tokens[newToken.Token] = newToken
	if err := s.persist(); err != nil {
		return RefreshRotation{}, err
	}
	return RefreshRotation{Token: newToken}, nil
}

func (s *fileRefreshStore) RevokeChain(chainID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeChainLocked(chainID)
	return s.persist()
}

func (s *fileRefreshStore) RevokeDelegation(subject, actor string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chains := map[string]bool{}
	for _, rt := range s.tokens {
		if rt.Subject == subject && rt.Actor == actor {
			chains[rt.ChainID] = true
		}
	}
	for chainID := range chains {
		s.revokeChainLocked(chainID)
	}
	if err := s.persist(); err != nil {
		return 0, err
	}
	return len(chains), nil
}

func (s *fileRefreshStore) revokeChainLocked(chainID string) {
	for tok, rt := range s.tokens {
		if rt.ChainID == chainID {
			delete(s.tokens, tok)
		}
	}
}
