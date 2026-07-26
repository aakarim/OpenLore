package openlore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
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

func TestAdminConfigMountIsRestrictedAndReadOnly(t *testing.T) {
	root := t.TempDir()
	loreFile := filepath.Join(root, "lore.json")
	serverFile := filepath.Join(root, "openlore.yml")
	lore := `{
  "roles": {
    "admins": {"allow": {"capabilities": ["admin"]}},
    "members": {}
  },
  "docsets": {
    "root": {
      "paths": ["/"],
      "access": {"allow": {"admins": "ro", "members": "ro"}}
    }
  },
  "identities": [
    {"name": "alice", "roles": ["admins"]},
    {"name": "bob", "roles": ["members"]}
  ]
}`
	if err := os.WriteFile(loreFile, []byte(lore), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverFile, []byte("version: \"1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := NewServer(root, WithConfigFile(serverFile), WithAuthFile(loreFile))
	if err != nil {
		t.Fatal(err)
	}
	identity := func(name string) Identity {
		return Identity{
			IdentityName: name,
			Principal:    AuthenticatedPrincipal{IdentityName: name},
			Scopes:       []string{ScopeFull},
		}
	}

	adminFS := s.buildSessionFS(identity("alice"))
	for path, want := range map[string]string{
		"/opt/openlore/lore.json":    lore,
		"/opt/openlore/openlore.yml": "version: \"1\"\n",
	} {
		got, err := adminFS.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("admin ReadFile(%s) = %q, %v", path, got, err)
		}
	}
	if _, err := adminFS.ReadFile("/lore.json"); err == nil {
		t.Fatal("host lore.json remained accessible through the ordinary lore tree")
	}
	if _, err := adminFS.ReadFile("/openlore.yml"); err == nil {
		t.Fatal("host openlore.yml remained accessible through the ordinary lore tree")
	}

	nonAdminFS := s.buildSessionFS(identity("bob"))
	for _, path := range []string{"/opt", "/opt/openlore", "/opt/openlore/lore.json", "/opt/openlore/openlore.yml"} {
		if _, err := nonAdminFS.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-admin Stat(%s) error = %v, want not exist", path, err)
		}
	}

	writable := adminFS.(vfs.WritableFS)
	if _, err := writable.WriteFileAtomic("/opt/openlore/lore.json", []byte("{}"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("admin config write error = %v, want read-only", err)
	}
	if err := os.WriteFile(serverFile, []byte("version: \"2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := adminFS.ReadFile("/opt/openlore/openlore.yml"); err != nil || string(got) != "version: \"2\"\n" {
		t.Fatalf("mounted config did not reflect command-path update: %q, %v", got, err)
	}

	if !s.buildSessionShell(identity("alice")).ActionAllowed(cmds.ActionAdmin) {
		t.Fatal("admin capability did not expose administrative commands")
	}
	if s.buildSessionShell(identity("bob")).ActionAllowed(cmds.ActionAdmin) {
		t.Fatal("non-admin identity can invoke administrative commands")
	}
}
