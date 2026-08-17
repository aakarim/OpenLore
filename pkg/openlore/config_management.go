package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aakarim/go-openlore/internal/config"
)

func (s *Server) updateAuth(attribution Attribution, eventType string, mutate func(*config.AuthConfig) error) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	b, err := json.Marshal(s.currentAuth())
	if err != nil {
		return err
	}
	var next config.AuthConfig
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	if err := mutate(&next); err != nil {
		return err
	}
	if err := config.ValidateAuthConfig(&next); err != nil {
		return err
	}
	if s.config.AuthFile == "" {
		return fmt.Errorf("auth_file is required for configuration changes")
	}
	b, err = json.MarshalIndent(&next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.config.AuthFile), 0o700); err != nil {
		return err
	}
	tmp := s.config.AuthFile + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.config.AuthFile); err != nil {
		return err
	}
	s.runtimeAuth.Store(&next)
	s.authorizationStore = fileAuthorizationStore{auth: &next}
	if s.passkeys != nil {
		s.passkeys.SetAuthConfig(&next)
	}
	if s.audit != nil {
		_ = s.audit.Record(context.Background(), AuditEvent{Type: eventType, Attribution: attribution})
	}
	return nil
}

func (s *Server) reloadAuth(attribution Attribution) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	next, err := config.LoadAuthConfig(s.config.AuthFile)
	if err != nil {
		if s.audit != nil {
			_ = s.audit.Record(context.Background(), AuditEvent{Type: "config.reject", Attribution: attribution, Details: map[string]any{"error": err.Error()}})
		}
		return err
	}
	s.runtimeAuth.Store(next)
	s.authorizationStore = fileAuthorizationStore{auth: next}
	if s.passkeys != nil {
		s.passkeys.SetAuthConfig(next)
	}
	if s.audit != nil {
		_ = s.audit.Record(context.Background(), AuditEvent{Type: "config.reload", Attribution: attribution})
	}
	return nil
}

func (s *Server) Reload(principal, actor string) error {
	return s.reloadAuth(Attribution{Principal: principal, Actor: actor})
}

// PromoteIdentity grants authority and credentials to an OAuth-created base
// identity in place, preserving all historical attribution.
func (s *Server) PromoteIdentity(attribution Attribution, name string, roles []string, home, publicKey string, match []config.IdentityMatch) error {
	return s.updateAuth(attribution, "identity.promote", func(auth *config.AuthConfig) error {
		for i := range auth.Identities {
			if auth.Identities[i].Name == name {
				auth.Identities[i].Roles = append([]string(nil), roles...)
				auth.Identities[i].Home = home
				auth.Identities[i].PublicKey = publicKey
				auth.Identities[i].Match = append([]config.IdentityMatch(nil), match...)
				return nil
			}
		}
		return fmt.Errorf("identity %q not found", name)
	})
}
