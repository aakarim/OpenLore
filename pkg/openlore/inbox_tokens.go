package openlore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// InboxToken is a revocable credential for the inbox upload endpoint. Secret is
// deliberately persisted in plaintext because it is the HMAC key.
type InboxToken struct {
	ID        string     `json:"id"`
	Secret    string     `json:"secret,omitempty"`
	Identity  string     `json:"identity"`
	Label     string     `json:"label,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (t InboxToken) Credential() string { return "olin_" + t.ID + "_" + t.Secret }

type InboxTokenStore struct {
	mu   sync.Mutex
	path string
}

func NewInboxTokenStore(dataDir string) (*InboxTokenStore, error) {
	if dataDir == "" {
		dataDir = "."
	}
	s := &InboxTokenStore{path: filepath.Join(dataDir, "auth", "inbox_tokens.json")}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(s.path), 0700); err != nil {
		return nil, err
	}
	if err := s.locked(func() error {
		if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
			return os.WriteFile(s.path, []byte("[]\n"), 0600)
		} else {
			return err
		}
	}); err != nil {
		return nil, err
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return nil, err
	}
	return s, nil
}

func randomInboxPart(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomInboxID(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *InboxTokenStore) load() ([]InboxToken, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var tokens []InboxToken
	if err := json.Unmarshal(b, &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

func (s *InboxTokenStore) save(tokens []InboxToken) error {
	b, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".inbox_tokens-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *InboxTokenStore) Create(identity, label string, expires *time.Time) (InboxToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := randomInboxID(9)
	if err != nil {
		return InboxToken{}, err
	}
	secret, err := randomInboxPart(32)
	if err != nil {
		return InboxToken{}, err
	}
	t := InboxToken{ID: id, Secret: secret, Identity: identity, Label: label, CreatedAt: time.Now().UTC(), ExpiresAt: expires}
	err = s.locked(func() error {
		tokens, e := s.load()
		if e != nil {
			return e
		}
		tokens = append(tokens, t)
		return s.save(tokens)
	})
	return t, err
}

func (s *InboxTokenStore) Get(id string) (InboxToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tokens []InboxToken
	err := s.locked(func() error { var e error; tokens, e = s.load(); return e })
	if err != nil {
		return InboxToken{}, false, err
	}
	for _, t := range tokens {
		if t.ID == id {
			return t, true, nil
		}
	}
	return InboxToken{}, false, nil
}

func (s *InboxTokenStore) List() ([]InboxToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tokens []InboxToken
	err := s.locked(func() error { var e error; tokens, e = s.load(); return e })
	if err != nil {
		return nil, err
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].CreatedAt.Before(tokens[j].CreatedAt) })
	return tokens, nil
}

func (s *InboxTokenStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := false
	err := s.locked(func() error {
		tokens, e := s.load()
		if e != nil {
			return e
		}
		for i, t := range tokens {
			if t.ID == id {
				tokens = append(tokens[:i], tokens[i+1:]...)
				deleted = true
				return s.save(tokens)
			}
		}
		return nil
	})
	return deleted, err
}
