package openlore

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type mutableAuthorizationStore struct {
	mu     sync.Mutex
	policy AuthorizationPolicy
	err    error
	calls  int
}

func (m *mutableAuthorizationStore) ResolveAuthorization(context.Context, AuthenticatedPrincipal) (AuthorizationPolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.policy, m.err
}

func (m *mutableAuthorizationStore) set(policy AuthorizationPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = policy
}
func (m *mutableAuthorizationStore) callCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }

func rbacServer() (*Server, Identity, *mutableAuthorizationStore) {
	store := &mutableAuthorizationStore{policy: AuthorizationPolicy{IdentityName: "alice", Roles: []string{"reader", "writer"}, HomeDocset: "home"}}
	s := &Server{
		authEnforced:       true,
		grants:             newGrantRegistry(),
		authorizationStore: store,
		config:             config.Config{},
		auth: &config.AuthConfig{
			Roles: map[string]config.RoleSpec{"reader": {}, "writer": {}, "blocked": {}},
			Docsets: map[string]config.DocsetSpec{
				"docs":   {Paths: []config.PathMapping{{Source: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"reader": "ro", "writer": "rw"}}},
				"home":   {Paths: []config.PathMapping{{Source: "/home"}}},
				"nested": {Paths: []config.PathMapping{{Source: "/home/private"}}},
			},
		},
	}
	id := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Scopes: []string{ScopeFull}, HomeDocset: "home"}
	return s, id, store
}

func TestBuildSessionFSReadSnapshotAndRuntimeWrites(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"docs", "extra"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "note.md"), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	merge := NewMergeFS()
	merge.SetRoot(NewDirFS(dir, config.FilesConfig{}).WithDocsetRoots([]string{"/docs", "/extra"}))
	if err := merge.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	store := &mutableAuthorizationStore{policy: AuthorizationPolicy{IdentityName: "alice", Roles: []string{"reader"}}}
	s := &Server{authEnforced: true, grants: newGrantRegistry(), authorizationStore: store, merge: merge, auth: &config.AuthConfig{
		Roles: map[string]config.RoleSpec{"reader": {}, "rw": {}, "extra": {}},
		Docsets: map[string]config.DocsetSpec{
			"docs":  {Paths: []config.PathMapping{{Source: "/docs"}}, Access: config.DocsetAccess{Allow: map[string]string{"reader": "ro", "rw": "rw"}}},
			"extra": {Paths: []config.PathMapping{{Source: "/extra"}}, Access: config.DocsetAccess{Allow: map[string]string{"extra": "ro"}}},
		},
	}}
	id := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Scopes: []string{ScopeFull}}
	fsys := s.buildSessionFS(id)
	if got := store.callCount(); got != 1 {
		t.Fatalf("construction lookups = %d, want 1", got)
	}
	if b, err := fsys.ReadFile("/docs/note.md"); err != nil || string(b) != "docs" {
		t.Fatalf("snapshot read = %q, %v", b, err)
	}
	if got := store.callCount(); got != 1 {
		t.Fatalf("read performed policy lookup: %d", got)
	}
	if _, err := fsys.ReadFile("/extra/note.md"); err == nil {
		t.Fatal("extra unexpectedly visible")
	}

	store.set(AuthorizationPolicy{IdentityName: "alice", Roles: []string{"rw", "extra"}})
	if !fsys.(vfs.WriteScopeFS).CanWrite("/docs/new.md") {
		t.Fatal("existing FS did not observe runtime rw grant")
	}
	if _, err := fsys.ReadFile("/extra/note.md"); err == nil {
		t.Fatal("read snapshot expanded")
	}
	store.set(AuthorizationPolicy{IdentityName: "alice", Roles: nil})
	if fsys.(vfs.WriteScopeFS).CanWrite("/docs/new.md") {
		t.Fatal("existing FS retained revoked write")
	}
	if _, err := fsys.ReadFile("/docs/note.md"); err != nil {
		t.Fatalf("read snapshot revoked: %v", err)
	}
}

func TestRBACRemoveAllCannotCrossNestedDocset(t *testing.T) {
	dir := t.TempDir()
	privateFile := filepath.Join(dir, "parent", "tree", "private", "secret.md")
	if err := os.MkdirAll(filepath.Dir(privateFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateFile, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	merge := NewMergeFS()
	merge.SetRoot(NewDirFS(dir, config.FilesConfig{}).WithDocsetRoots([]string{"/parent", "/parent/tree/private"}))
	if err := merge.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	store := &mutableAuthorizationStore{policy: AuthorizationPolicy{IdentityName: "alice", Roles: []string{"writer"}}}
	s := &Server{
		authEnforced:       true,
		grants:             newGrantRegistry(),
		authorizationStore: store,
		merge:              merge,
		auth: &config.AuthConfig{
			Roles: map[string]config.RoleSpec{"writer": {}},
			Docsets: map[string]config.DocsetSpec{
				"parent":  {Paths: []config.PathMapping{{Source: "/parent"}}, Access: config.DocsetAccess{Allow: map[string]string{"writer": "rw"}}},
				"private": {Paths: []config.PathMapping{{Source: "/parent/tree/private"}}},
			},
		},
	}
	id := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Scopes: []string{ScopeFull}}
	fsys := s.buildSessionFS(id)
	writable, ok := fsys.(vfs.WritableFS)
	if !ok {
		t.Fatal("session filesystem is not writable")
	}
	if err := writable.RemoveAll("/parent/tree", vfs.RemoveOpts{}); err == nil {
		t.Fatal("recursive delete crossing nested docset succeeded")
	}
	if got, err := os.ReadFile(privateFile); err != nil || string(got) != "secret" {
		t.Fatalf("nested docset content changed: %q, %v", got, err)
	}
}

func TestAuthorizationPolicyValidationEdges(t *testing.T) {
	s, id, store := rbacServer()
	t.Run("zero role home owner", func(t *testing.T) {
		store.set(AuthorizationPolicy{IdentityName: "alice", HomeDocset: "home"})
		if _, err := s.currentPolicy(id); err != nil {
			t.Fatalf("policy: %v", err)
		}
		if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/home/note.md") {
			t.Fatal("implicit home rw denied")
		}
	})
	t.Run("known and unknown role fails whole lookup", func(t *testing.T) {
		store.set(AuthorizationPolicy{IdentityName: "alice", Roles: []string{"reader", "missing"}, HomeDocset: "home"})
		if _, err := s.currentPolicy(id); err == nil {
			t.Fatal("unknown role accepted")
		}
		if s.identityCanWrite(id, vfs.ChangeActionWrite, "/home/note.md") {
			t.Fatal("invalid policy received home rw")
		}
	})
	for _, tc := range []struct {
		name, principal string
		policy          AuthorizationPolicy
	}{
		{"malformed guest", "guest", AuthorizationPolicy{IdentityName: "guest", Roles: []string{"reader"}}},
		{"malformed non-guest", "alice", AuthorizationPolicy{IdentityName: "alice", Roles: []string{"guest"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.set(tc.policy)
			check := id
			check.IdentityName = tc.principal
			check.Principal.IdentityName = tc.principal
			if _, err := s.currentPolicy(check); err == nil {
				t.Fatal("malformed policy accepted")
			}
		})
	}
	t.Run("role denied on dynamic home", func(t *testing.T) {
		home := s.auth.Docsets["home"]
		home.Access.Deny = []string{"reader"}
		s.auth.Docsets["home"] = home
		store.set(AuthorizationPolicy{IdentityName: "alice", Roles: []string{"reader"}, HomeDocset: "home"})
		if _, err := s.currentPolicy(id); err == nil {
			t.Fatal("home deny accepted")
		}
	})
}

func TestRBACMultiRoleGrantUnionAndDeny(t *testing.T) {
	s, id, store := rbacServer()
	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/docs/file.md") {
		t.Fatal("rw from one role must authorize write despite another role's ro")
	}
	ds := s.auth.Docsets["docs"]
	ds.Access.Deny = []string{"reader"}
	s.auth.Docsets["docs"] = ds
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/docs/file.md") {
		t.Fatal("matching deny must override every allow")
	}
	store.policy.Roles = []string{"writer"}
	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/docs/file.md") {
		t.Fatal("write must use current role policy")
	}
}

func TestRBACImplicitHomeStopsAtNestedBoundary(t *testing.T) {
	s, id, _ := rbacServer()
	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/home/note.md") {
		t.Fatal("owner must receive implicit rw on home")
	}
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/home/private/note.md") {
		t.Fatal("implicit home access must stop at nested docset")
	}
}

func TestRBACCapabilityDenyAndRuntimeLookup(t *testing.T) {
	s, id, store := rbacServer()
	s.auth.Roles["reader"] = config.RoleSpec{Allow: config.CapabilityRules{Capabilities: []string{"spawn"}}}
	if !s.hasCurrentCapability(id, "spawn") {
		t.Fatal("role capability allow should apply")
	}
	s.auth.Roles["writer"] = config.RoleSpec{Deny: config.CapabilityRules{Capabilities: []string{"spawn"}}}
	if s.hasCurrentCapability(id, "spawn") {
		t.Fatal("capability deny must win")
	}
	store.policy.Roles = []string{"reader"}
	if !s.hasCurrentCapability(id, "spawn") {
		t.Fatal("capability checks must observe current role membership")
	}
}
