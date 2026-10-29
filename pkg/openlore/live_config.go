package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// liveConfig is prepared completely before publication. Readers retain one
// generation of policy and projection backends for the whole filesystem read.
type liveConfig struct {
	auth        *config.AuthConfig
	projections map[string]vfs.FileSystem
	engine      *rules.Engine
	scopes      []analytics.KnowledgeScope
}

func (s *Server) prepareLiveConfig(auth *config.AuthConfig) (*liveConfig, error) {
	if err := s.validateLiveAuthCandidate(auth); err != nil {
		return nil, err
	}
	projections, err := s.projectionMounts(auth)
	if err != nil {
		return nil, err
	}
	plugin, err := newRulesPluginWithTokenizer(auth, rules.Defaults{Growth: s.config.Rules.Growth}, s.merge, s.logger, s.config.Rules.Tokenizer)
	if err != nil {
		return nil, err
	}
	var scopes []analytics.KnowledgeScope
	if s.analytics != nil {
		scopes, err = s.knowledgeScopes(auth)
		if err != nil {
			return nil, err
		}
	}
	return &liveConfig{auth: auth, projections: projections, engine: plugin.engine, scopes: scopes}, nil
}

// Publish in the same queue as content commits. A queued write is authorized
// and checked against rules entirely before or entirely after this switch.
// persist runs before publication, so failed writes leave the live state alone.
func (s *Server) installLiveConfig(next *liveConfig, persist func() error) error {
	install := func() error {
		if persist != nil {
			if err := persist(); err != nil {
				return err
			}
		}
		s.runtimeConfig.Store(next)
		if s.analytics != nil {
			s.analytics.SetKnowledgeScopes(next.scopes)
		}
		if s.passkeys != nil {
			s.passkeys.SetAuthConfig(next.auth)
		}
		return nil
	}
	if s.writeLog != nil {
		return s.writeLog.Do(context.Background(), install)
	}
	return install()
}

func (s *Server) knowledgeScopes(auth *config.AuthConfig) ([]analytics.KnowledgeScope, error) {
	dataDir := s.config.DataDir
	if dataDir == "" {
		dataDir = ".openlore"
	}
	analyticsDir := s.config.Analytics.Dir
	if !filepath.IsAbs(analyticsDir) {
		analyticsDir = filepath.Join(dataDir, analyticsDir)
	}
	excluded, err := analyticsInternalRoots(s.merge, dataDir, analyticsDir)
	if err != nil {
		return nil, err
	}
	var scopes []analytics.KnowledgeScope
	for name, ds := range auth.Docsets {
		boundary := analyticsContentBoundary(s.config.Files)
		if ds.Projection != nil {
			encoded, _ := json.Marshal(ds.Projection)
			boundary += ":" + hashContent(encoded)
		}
		for _, mapping := range ds.Paths {
			scopes = append(scopes, analytics.KnowledgeScope{Name: name, Root: displayPath(mapping), Exclude: excluded, ContentBoundary: boundary})
		}
	}
	return scopes, nil
}

func (s *Server) projectionMounts(auth *config.AuthConfig) (map[string]vfs.FileSystem, error) {
	mounts := map[string]vfs.FileSystem{}
	for name, ds := range auth.Docsets {
		if ds.Projection == nil {
			continue
		}
		root := primaryDisplayPath(ds)
		if root == "" {
			root = "/" + name
		}
		// Neither an ancestor nor a descendant may replace control-plane data.
		reserved := []string{"/jobs", "/requests", "/opt/openlore"}
		if s.merge != nil {
			reserved = append(reserved, s.merge.SystemMountPaths()...)
		}
		for _, mount := range reserved {
			if pathWithinRoot(mount, root) || pathWithinRoot(root, mount) {
				return nil, fmt.Errorf("projection %q conflicts with mount %q", name, mount)
			}
		}
		if s.merge != nil {
			for _, mount := range s.merge.mountPaths() {
				if pathWithinRoot(root, mount) {
					return nil, fmt.Errorf("projection %q shadows mount %q", name, mount)
				}
			}
		}
		mounts[strings.TrimPrefix(root, "/")] = &projectionFS{store: s.docstore, spec: *ds.Projection}
	}
	return mounts, nil
}

// readView captures the live policy and its mounts together. It deliberately
// does not copy Server's synchronization primitives or session policy snapshots.
func (s *Server) readView() *Server {
	auth := s.auth
	merge := *s.merge
	projections := s.initialProjections
	if live := s.runtimeConfig.Load(); live != nil {
		auth = live.auth
		projections = live.projections
	}
	merge.projections = func() map[string]vfs.FileSystem { return projections }
	store := s.authorizationStore
	if _, file := store.(fileAuthorizationStore); file {
		store = fileAuthorizationStore{auth: auth}
	}
	return &Server{config: s.config, auth: auth, authEnforced: s.authEnforced, grants: s.grants, merge: &merge, authorizationStore: store}
}

func (s *Server) liveReadIdentity(id Identity) Identity {
	// External authorization stores retain their session snapshot contract.
	// File-backed policy is refreshed with each captured configuration view.
	if _, file := s.authorizationStore.(fileAuthorizationStore); file {
		id.policySnapshot = nil
	}
	return s.resolveSessionIdentity(id)
}

type liveReadFS struct {
	vfs.WritableFS
	server   *Server
	identity Identity
}

func (f *liveReadFS) view() *scopedReadFS {
	s := f.server.readView()
	id := s.liveReadIdentity(f.identity)
	return newScopedReadFS(s.merge, s.readableRoots(id), s.allDocsetRoots())
}

func (f *liveReadFS) Stat(p string) (*vfs.FileInfo, error)     { return f.view().Stat(p) }
func (f *liveReadFS) ReadDir(p string) ([]vfs.FileInfo, error) { return f.view().ReadDir(p) }
func (f *liveReadFS) ReadFile(p string) ([]byte, error)        { return f.view().ReadFile(p) }
func (f *liveReadFS) ReadFileBounded(p string, max int64) ([]byte, error) {
	return f.view().ReadFileBounded(p, max)
}
func (f *liveReadFS) GetXattr(p, name string) ([]byte, error) { return f.view().GetXattr(p, name) }
func (f *liveReadFS) ListXattrs(p string) ([]string, error)   { return f.view().ListXattrs(p) }
