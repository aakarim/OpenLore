package openlore

import (
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

func boolPtr(b bool) *bool { return &b }

// enforcedDocsetServer builds a writable, auth-enforced server with a mix of
// docset shapes for exercising the per-session builders. The inbox plugin's
// `publish` grant is registered so inbox docsets can be exercised.
func enforcedDocsetServer() *Server {
	s := &Server{
		merge:        NewMergeFS(),
		authEnforced: true,
		grants:       newGrantRegistry(),
		config:       config.Config{Readonly: false}, // global write lock open
		auth: &config.AuthConfig{
			Roles: map[string]config.RoleSpec{"agent": {}, "reader": {}, "publisher": {}},
			Docsets: map[string]config.DocsetSpec{
				"public": {Paths: []config.PathMapping{{Source: "/docs/public", Display: "/docs/public"}}, Access: config.DocsetAccess{Allow: map[string]string{"agent": "rw", "reader": "ro"}}},
				"backend": {
					Paths:        []config.PathMapping{{Source: "/docs/backend", Display: "/docs/backend"}},
					Inbox:        "inbox",
					MaxWriteSize: 5000,
					Access:       config.DocsetAccess{Allow: map[string]string{"agent": "rw", "reader": "ro", "publisher": "publish"}},
				},
				"archive": {
					Paths:    []config.PathMapping{{Source: "/docs/archive", Display: "/docs/archive"}},
					Readonly: boolPtr(true),
					Access:   config.DocsetAccess{Allow: map[string]string{"agent": "rw"}},
				},
				"home": {
					Paths:   []config.PathMapping{{Source: "/home/agent", Display: "/home/agent"}},
					Aliases: []string{"/agent"},
					Inbox:   "inbox",
					Access:  config.DocsetAccess{Allow: map[string]string{"agent": "rw"}},
				},
			},
		},
	}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	if err := s.registerPlugin(NewInboxPlugin()); err != nil {
		panic(err)
	}
	return s
}

// agentIdentity is the full-authority identity used across the docset tests.
func agentIdentity() Identity {
	id := identityWithPolicy("agent", "agent")
	id.HomeDocset = "home"
	id.policySnapshot.HomeDocset = "home"
	return id
}

func docsetByName(ds []cmds.DocsetInfo, name string) (cmds.DocsetInfo, bool) {
	for _, d := range ds {
		if d.Name == name {
			return d, true
		}
	}
	return cmds.DocsetInfo{}, false
}

func TestSessionDocsets_Enforced(t *testing.T) {
	s := enforcedDocsetServer()
	got := s.sessionDocsets(agentIdentity())

	// Sorted by name.
	wantOrder := []string{"archive", "backend", "home", "home", "public"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d docsets, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Fatalf("docset[%d] = %q, want %q (sorted by name)", i, got[i].Name, name)
		}
	}

	public, _ := docsetByName(got, "public")
	if !public.Writable {
		t.Fatalf("public should be directly writable for a full-scope agent: %+v", public)
	}
	if public.Home || public.Inbox {
		t.Fatalf("public should carry no attributes: %+v", public)
	}
	if len(public.Grants) != 1 || public.Grants[0] != "rw" {
		t.Fatalf("public grants = %v, want [rw]", public.Grants)
	}

	backend, _ := docsetByName(got, "backend")
	if !backend.Writable {
		t.Fatalf("backend should be writable")
	}
	if !backend.Inbox {
		t.Fatalf("backend has an inbox → Inbox")
	}

	archive, _ := docsetByName(got, "archive")
	if archive.Writable {
		t.Fatalf("archive is per-docset readonly → not writable")
	}

	home, _ := docsetByName(got, "home")
	if !home.Home {
		t.Fatalf("home docset should be marked Home")
	}
	if !home.Writable {
		t.Fatalf("home should be writable")
	}
	alias := got[3]
	if alias.Paths[0] != "/agent" || alias.AliasTarget != "/home/agent" || alias.Home || alias.Inbox {
		t.Fatalf("home alias row = %+v", alias)
	}
}

func TestSessionDocsets_GlobalReadonlyMakesAllRead(t *testing.T) {
	s := enforcedDocsetServer()
	s.config.Readonly = true // close the global lock

	for _, d := range s.sessionDocsets(agentIdentity()) {
		if d.Writable {
			t.Fatalf("global lock closed: %q must not be writable", d.Name)
		}
	}
}

func TestSessionDocsets_ReadOnlyGrantIsReadOnly(t *testing.T) {
	s := enforcedDocsetServer()
	// An identity with only ro grants (no identity name / not a writer).
	id := identityWithPolicy("reader", "reader")

	for _, d := range s.sessionDocsets(id) {
		if d.Writable {
			t.Fatalf("ro-grant session: %q must be read-only", d.Name)
		}
	}
}

func TestSessionPublishTargets_OnlyWritableInboxDocsets(t *testing.T) {
	s := enforcedDocsetServer()
	targets := s.sessionPublishTargets(agentIdentity())
	names := map[string]int64{}
	for _, tgt := range targets {
		names[tgt.Name] = tgt.MaxFileSize
	}
	if _, ok := names["public"]; ok {
		t.Fatalf("public has no inbox → not a publish target")
	}
	if _, ok := names["archive"]; ok {
		t.Fatalf("archive has no inbox → not a publish target")
	}
	if _, ok := names["backend"]; !ok {
		t.Fatalf("backend has an inbox → should be a publish target")
	}
	if names["backend"] != 5000 {
		t.Fatalf("backend max size = %d, want 5000", names["backend"])
	}
	if _, ok := names["home"]; !ok {
		t.Fatalf("home has an inbox → should be a publish target")
	}
}

func TestSessionPublishTargets_PublishGrant(t *testing.T) {
	s := enforcedDocsetServer()
	// A publish grant on backend (which has an inbox) is a publish target.
	id := identityWithPolicy("miles", "publisher")
	targets := s.sessionPublishTargets(id)
	if len(targets) != 1 || targets[0].Name != "backend" {
		t.Fatalf("publish grant should yield backend as a target, got %+v", targets)
	}
}

func TestSessionPublishTargets_UnenforcedHasNone(t *testing.T) {
	s := &Server{
		merge:        NewMergeFS(),
		authEnforced: false,
		grants:       newGrantRegistry(),
		config:       config.Config{Readonly: false},
		auth: &config.AuthConfig{
			Docsets: map[string]config.DocsetSpec{
				"public": {Paths: []config.PathMapping{{Source: "/", Display: "/"}}},
			},
		},
	}
	id := Identity{Scopes: []string{ScopeFull}}
	if targets := s.sessionPublishTargets(id); targets != nil {
		t.Fatalf("unenforced mode has no publish inboxes, got %+v", targets)
	}
}

func TestSessionDocsets_UnenforcedPublicIsWritable(t *testing.T) {
	s := &Server{
		merge:        NewMergeFS(),
		authEnforced: false,
		grants:       newGrantRegistry(),
		config:       config.Config{Readonly: false}, // writes unrestricted
		auth: &config.AuthConfig{
			Docsets: map[string]config.DocsetSpec{
				"public": {Paths: []config.PathMapping{{Source: "/", Display: "/"}}},
			},
		},
	}
	id := Identity{Scopes: []string{ScopeFull}}
	got := s.sessionDocsets(id)
	if len(got) != 1 || got[0].Name != "public" {
		t.Fatalf("want single public docset, got %+v", got)
	}
	if !got[0].Writable {
		t.Fatalf("unenforced public with open lock should be writable")
	}
	if got[0].Home {
		t.Fatalf("unenforced public must never be home")
	}
}
