package openlore

import (
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// grantTestServer builds an auth-enforced, writable server with two path-based
// docsets sharing one root filesystem (alfie, miles), each with an /inbox, plus
// the inbox plugin registered so the `publish` grant resolves.
func grantTestServer() *Server {
	s := &Server{
		merge:        NewMergeFS(),
		authEnforced: true,
		grants:       newGrantRegistry(),
		config:       config.Config{Readonly: false},
		auth: &config.AuthConfig{
			Roles: map[string]config.RoleSpec{"alfie-rw": {}, "alfie-ro": {}, "alfie-publish": {}, "root-rw": {}, "all-rw": {}, "backend-publish": {}},
			Docsets: map[string]config.DocsetSpec{
				"alfie": {Paths: []config.PathMapping{{Source: "/alfie", Display: "/alfie"}}, Inbox: "inbox", Access: config.DocsetAccess{Allow: map[string]string{"alfie-rw": "rw", "alfie-ro": "ro", "alfie-publish": "publish", "all-rw": "rw"}}},
				"miles": {Paths: []config.PathMapping{{Source: "/miles", Display: "/miles"}}, Inbox: "inbox", Access: config.DocsetAccess{Allow: map[string]string{"all-rw": "rw"}}},
			},
		},
	}
	if err := s.registerPlugin(NewInboxPlugin()); err != nil {
		panic(err)
	}
	s.authorizationStore = &mutableAuthorizationStore{}
	return s
}

func identityWithPolicy(name string, roles ...string) Identity {
	p := AuthorizationPolicy{IdentityName: name, Roles: roles}
	return Identity{IdentityName: name, Principal: AuthenticatedPrincipal{IdentityName: name}, policySnapshot: &p, Scopes: []string{ScopeFull}}
}

func setCurrentPolicy(s *Server, id Identity) {
	s.authorizationStore.(*mutableAuthorizationStore).policy = *id.policySnapshot
}

func TestIdentityCanWrite_RW(t *testing.T) {
	s := grantTestServer()
	id := identityWithPolicy("alfie", "alfie-rw")
	setCurrentPolicy(s, id)

	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/notes.md") {
		t.Fatal("rw should write anywhere in its docset")
	}
	if !s.identityCanWrite(id, vfs.ChangeActionRemove, "/alfie/notes.md") {
		t.Fatal("rw should delete in its docset")
	}
	// No grant on miles → denied.
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/miles/notes.md") {
		t.Fatal("no grant on miles → write must be denied")
	}
}

func TestIdentityCanWrite_RO(t *testing.T) {
	s := grantTestServer()
	id := identityWithPolicy("bob", "alfie-ro")
	setCurrentPolicy(s, id)
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/notes.md") {
		t.Fatal("ro must never write")
	}
}

func TestIdentityCanWrite_Publish(t *testing.T) {
	s := grantTestServer()
	// miles holds publish on alfie: create/edit only within /alfie/inbox, no deletes.
	id := identityWithPolicy("miles", "alfie-publish")
	setCurrentPolicy(s, id)

	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/inbox/from-miles.md") {
		t.Fatal("publish should write inside the inbox")
	}
	if !s.identityCanWrite(id, vfs.ChangeActionMkdir, "/alfie/inbox/sub") {
		t.Fatal("publish should mkdir inside the inbox")
	}
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/notes.md") {
		t.Fatal("publish must NOT write outside the inbox")
	}
	if s.identityCanWrite(id, vfs.ChangeActionRemove, "/alfie/inbox/from-miles.md") {
		t.Fatal("publish must NOT delete even inside the inbox")
	}
	if s.identityCanWrite(id, vfs.ChangeActionRemoveAll, "/alfie/inbox") {
		t.Fatal("publish must NOT remove_all")
	}
}

func TestIdentityCanWrite_ScopeAndLockCeilings(t *testing.T) {
	s := grantTestServer()
	id := identityWithPolicy("alfie", "alfie-rw")
	setCurrentPolicy(s, id)
	id.Scopes = []string{ScopeRead}
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/notes.md") {
		t.Fatal("a read-scoped token must not write even with an rw grant")
	}

	full := identityWithPolicy("alfie", "alfie-rw")
	setCurrentPolicy(s, full)
	s.config.Readonly = true
	if s.identityCanWrite(full, vfs.ChangeActionWrite, "/alfie/notes.md") {
		t.Fatal("the global write lock must block all writes")
	}
}

func TestValidateGrants(t *testing.T) {
	// publish referenced but inbox plugin NOT registered → fail-closed.
	s := &Server{
		authEnforced: true,
		grants:       newGrantRegistry(), // only ro/rw
		auth: &config.AuthConfig{
			Docsets: map[string]config.DocsetSpec{"alfie": {Access: config.DocsetAccess{Allow: map[string]string{"writer": "publish"}}}},
		},
	}
	if err := s.validateGrants(); err == nil {
		t.Fatal("expected validateGrants to reject unregistered 'publish' grant")
	}

	// Register the inbox plugin → publish becomes valid.
	if err := s.registerPlugin(NewInboxPlugin()); err != nil {
		t.Fatal(err)
	}
	if err := s.validateGrants(); err != nil {
		t.Fatalf("publish should validate once the inbox plugin is registered: %v", err)
	}
}

// TestValidateGrants_RejectsDuplicateDisplayRoots pins that two docsets sharing
// the same display root are rejected at startup (ambiguous access boundary).
func TestValidateGrants_RejectsDuplicateDisplayRoots(t *testing.T) {
	s := &Server{
		authEnforced: true,
		grants:       newGrantRegistry(),
		auth: &config.AuthConfig{
			Docsets: map[string]config.DocsetSpec{
				"a": {Paths: []config.PathMapping{{Source: "/x", Display: "/x"}}},
				"b": {Paths: []config.PathMapping{{Source: "/other", Display: "/x"}}},
			},
		},
	}
	if err := s.validateGrants(); err == nil {
		t.Fatal("expected validateGrants to reject two docsets sharing display root /x")
	}
}

func TestValidateGrants_Aliases(t *testing.T) {
	tests := []struct {
		name    string
		docsets map[string]config.DocsetSpec
		wantErr bool
	}{
		{
			name: "valid",
			docsets: map[string]config.DocsetSpec{
				"jared": {Paths: []config.PathMapping{{Source: "/agent/jared"}}, Aliases: []string{"/jared"}},
				"adil":  {Paths: []config.PathMapping{{Source: "/user/adil"}}, Aliases: []string{"/adil"}},
			},
		},
		{
			name: "valid beneath canonical ancestor",
			docsets: map[string]config.DocsetSpec{
				"openlore": {Paths: []config.PathMapping{{Source: "/"}}},
				"jared":    {Paths: []config.PathMapping{{Source: "/agent/jared"}}, Aliases: []string{"/jared"}},
			},
		},
		{
			name:    "without canonical path",
			docsets: map[string]config.DocsetSpec{"jared": {Aliases: []string{"/jared"}}},
			wantErr: true,
		},
		{
			name:    "relative",
			docsets: map[string]config.DocsetSpec{"jared": {Paths: []config.PathMapping{{Source: "/agent/jared"}}, Aliases: []string{"jared"}}},
			wantErr: true,
		},
		{
			name: "overlapping canonical path",
			docsets: map[string]config.DocsetSpec{
				"jared": {Paths: []config.PathMapping{{Source: "/agent/jared"}}, Aliases: []string{"/legacy"}},
				"other": {Paths: []config.PathMapping{{Source: "/legacy/private"}}},
			},
			wantErr: true,
		},
		{
			name: "overlapping aliases",
			docsets: map[string]config.DocsetSpec{
				"jared": {Paths: []config.PathMapping{{Source: "/agent/jared"}}, Aliases: []string{"/legacy"}},
				"other": {Paths: []config.PathMapping{{Source: "/agent/other"}}, Aliases: []string{"/legacy/private"}},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{authEnforced: true, grants: newGrantRegistry(), auth: &config.AuthConfig{Docsets: tt.docsets}}
			err := s.validateGrants()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateGrants() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateGrants_RejectsAliasOverlappingMount(t *testing.T) {
	for _, alias := range []string{"/jobs", "/jobs/private"} {
		t.Run(alias, func(t *testing.T) {
			merge := NewMergeFS()
			merge.MountSystem("jobs", NewFSAdapter(fstest.MapFS{"status": {Data: []byte("ok")}}))
			s := &Server{
				authEnforced: true,
				grants:       newGrantRegistry(),
				merge:        merge,
				auth: &config.AuthConfig{Docsets: map[string]config.DocsetSpec{
					"jared": {Paths: []config.PathMapping{{Source: "/agent/jared"}}, Aliases: []string{alias}},
				}},
			}
			if err := s.validateGrants(); err == nil {
				t.Fatalf("expected alias %q to conflict with /jobs mount", alias)
			}
		})
	}
}

// TestSessionPublishTargets_ResolvesInboxPath pins that a publish target
// carries the docset's resolved inbox path (not just its name), so `publish`
// routes into /docset/inbox/... where the publish grant actually permits writes.
func TestSessionPublishTargets_ResolvesInboxPath(t *testing.T) {
	s := grantTestServer()
	id := identityWithPolicy("miles", "alfie-publish")
	targets := s.sessionPublishTargets(id)
	if len(targets) != 1 {
		t.Fatalf("want 1 publish target, got %d: %+v", len(targets), targets)
	}
	if targets[0].Name != "alfie" {
		t.Fatalf("target name = %q, want alfie", targets[0].Name)
	}
	if targets[0].InboxPath != "/alfie/inbox" {
		t.Fatalf("target inbox path = %q, want /alfie/inbox", targets[0].InboxPath)
	}
}

// TestGrantForPath_NestedDocsetOverridesAncestor pins that a grant on an
// ancestor docset (the root "/") does not reach into a nested docset the
// identity lacks a grant on: /alfie is carved out of a root "openlore" grant.
func TestGrantForPath_NestedDocsetOverridesAncestor(t *testing.T) {
	s := grantTestServer()
	// Add a root docset "/" alongside the existing /alfie and /miles docsets.
	s.auth.Docsets["openlore"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/", Display: "/"}}, Access: config.DocsetAccess{Allow: map[string]string{"root-rw": "rw"}}}

	// Identity holds rw on the root docset only — no grant on /alfie.
	id := identityWithPolicy("anon", "root-rw")
	setCurrentPolicy(s, id)

	// A root-level file is governed by the root docset → writable.
	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/readme.md") {
		t.Fatal("root grant should write a root-level file")
	}
	// /alfie is a more-specific docset that carves itself out → denied despite
	// the root rw grant.
	if s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/x.md") {
		t.Fatal("nested docset must override ancestor grant: /alfie write should be denied")
	}
}

// TestScopedReadFS_NestedDocsetCarveOut pins that an identity with read access to
// the root "/" cannot read or list a nested docset it lacks a grant on.
func TestScopedReadFS_NestedDocsetCarveOut(t *testing.T) {
	merge := NewMergeFS()
	merge.SetRoot(NewFSAdapter(fstest.MapFS{
		"readme.md":       {Data: []byte("root")},
		"alfie/secret.md": {Data: []byte("alfie")},
		"miles/secret.md": {Data: []byte("miles")},
	}))
	// Readable: the root "/" only. Boundaries: root + the two nested docsets.
	fs := newScopedReadFS(merge, []string{"/"}, []string{"/", "/alfie", "/miles"})

	// Root-level file is readable.
	if _, err := fs.ReadFile("/readme.md"); err != nil {
		t.Fatalf("root-level file should be readable: %v", err)
	}
	// Nested docsets are carved out even though "/" is readable.
	if _, err := fs.ReadFile("/alfie/secret.md"); err == nil {
		t.Fatal("nested docset /alfie must be carved out of the root grant")
	}
	if _, err := fs.ReadFile("/miles/secret.md"); err == nil {
		t.Fatal("nested docset /miles must be carved out of the root grant")
	}

	// Listing "/" shows the root file but hides the carved-out nested docsets.
	entries, err := fs.ReadDir("/")
	if err != nil {
		t.Fatalf("ReadDir(/): %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.FileName] = true
	}
	if !names["readme.md"] {
		t.Fatalf("root listing should show root-level files: %+v", entries)
	}
	if names["alfie"] || names["miles"] {
		t.Fatalf("root listing must hide carved-out nested docsets: %+v", entries)
	}
}

func TestScopedReadFS_HidesUngrantDocsetAncestorsFromRootGrant(t *testing.T) {
	merge := NewMergeFS()
	merge.SetRoot(NewFSAdapter(fstest.MapFS{
		"readme.md":                 {Data: []byte("root")},
		"agent/jared/secret.md":     {Data: []byte("jared")},
		"user/adil/private.md":      {Data: []byte("adil")},
		"channel/general/shared.md": {Data: []byte("shared")},
	}))
	boundaries := []string{"/", "/agent/jared", "/user/adil", "/channel/general"}
	guest := newScopedReadFS(merge, []string{"/"}, boundaries)

	entries, err := guest.ReadDir("/")
	if err != nil {
		t.Fatalf("ReadDir(/): %v", err)
	}
	for _, entry := range entries {
		if entry.FileName == "agent" || entry.FileName == "user" || entry.FileName == "channel" {
			t.Fatalf("guest root listing exposed namespace ancestor %q: %+v", entry.FileName, entries)
		}
	}
	for _, hidden := range []string{"/agent", "/user", "/channel"} {
		if _, err := guest.Stat(hidden); err == nil {
			t.Fatalf("guest Stat(%s) must be hidden", hidden)
		}
		if _, err := guest.ReadDir(hidden); err == nil {
			t.Fatalf("guest ReadDir(%s) must be hidden", hidden)
		}
	}

	// A grant on a nested docset restores only the ancestors needed to reach it.
	jared := newScopedReadFS(merge, []string{"/", "/agent/jared"}, boundaries)
	if _, err := jared.ReadDir("/agent"); err != nil {
		t.Fatalf("granted docset ancestor /agent should be navigable: %v", err)
	}
	if _, err := jared.ReadFile("/agent/jared/secret.md"); err != nil {
		t.Fatalf("granted nested docset should be readable: %v", err)
	}
	for _, hidden := range []string{"/user", "/channel"} {
		if _, err := jared.Stat(hidden); err == nil {
			t.Fatalf("ungranted namespace %s must remain hidden", hidden)
		}
	}
}

func TestScopedReadFS_HidesSiblingDocsets(t *testing.T) {
	merge := NewMergeFS()
	merge.SetRoot(NewFSAdapter(fstest.MapFS{
		"alfie/secret.md": {Data: []byte("alfie")},
		"miles/secret.md": {Data: []byte("miles")},
	}))
	// Session may read only /alfie.
	fs := newScopedReadFS(merge, []string{"/alfie"}, []string{"/alfie", "/miles"})

	if _, err := fs.ReadFile("/alfie/secret.md"); err != nil {
		t.Fatalf("should read granted docset: %v", err)
	}
	if _, err := fs.ReadFile("/miles/secret.md"); err == nil {
		t.Fatal("must not read ungranted sibling docset")
	}

	// Root listing shows only /alfie, never /miles.
	entries, err := fs.ReadDir("/")
	if err != nil {
		t.Fatalf("ReadDir(/): %v", err)
	}
	for _, e := range entries {
		if e.FileName == "miles" {
			t.Fatalf("root listing must hide ungranted docset: %+v", entries)
		}
	}
	var sawAlfie bool
	for _, e := range entries {
		if e.FileName == "alfie" {
			sawAlfie = true
		}
	}
	if !sawAlfie {
		t.Fatalf("root listing should show the granted docset: %+v", entries)
	}
}
