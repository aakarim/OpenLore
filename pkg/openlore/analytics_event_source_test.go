package openlore

import (
	"context"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
)

type sliceAnalyticsSource []analytics.Event

func (s sliceAnalyticsSource) Scan(ctx context.Context, filter analytics.EventFilter, fn func(analytics.Event) error) error {
	types := map[string]bool{}
	for _, eventType := range filter.Types {
		types[eventType] = true
	}
	for _, event := range s {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(types) > 0 && !types[event.Type] {
			continue
		}
		if err := fn(event); err != nil {
			return err
		}
	}
	return nil
}

type identityAuthorizationStore map[string]AuthorizationPolicy

func (s identityAuthorizationStore) ResolveAuthorization(_ context.Context, principal AuthenticatedPrincipal) (AuthorizationPolicy, error) {
	return s[principal.IdentityName], nil
}

func analyticsScopeServer() (*Server, Identity, Identity) {
	auth := &config.AuthConfig{
		Roles: map[string]config.RoleSpec{"alpha": {}, "beta": {}},
		Docsets: map[string]config.DocsetSpec{
			"docs":    {Paths: []config.PathMapping{{Source: "/docs"}}, Aliases: []string{"/knowledge"}, Access: config.DocsetAccess{Allow: map[string]string{"alpha": "ro"}}},
			"private": {Paths: []config.PathMapping{{Source: "/docs/private"}}, Access: config.DocsetAccess{Allow: map[string]string{"beta": "ro"}}},
			"sibling": {Paths: []config.PathMapping{{Source: "/sibling"}}, Access: config.DocsetAccess{Allow: map[string]string{"alpha": "ro"}}},
		},
	}
	store := identityAuthorizationStore{
		"alice": {IdentityName: "alice", Roles: []string{"alpha"}},
		"bob":   {IdentityName: "bob", Roles: []string{"beta"}},
	}
	merge := NewMergeFS()
	merge.SetRoot(NewDirFS(".", config.FilesConfig{}))
	server := &Server{authEnforced: true, auth: auth, grants: newGrantRegistry(), authorizationStore: store, merge: merge}
	alice := Identity{IdentityName: "alice", Principal: AuthenticatedPrincipal{IdentityName: "alice"}, Scopes: []string{ScopeFull}}
	bob := Identity{IdentityName: "bob", Principal: AuthenticatedPrincipal{IdentityName: "bob"}, Scopes: []string{ScopeFull}}
	return server, alice, bob
}

func TestDashboardEventSourceScopesPathsSearchesAndCorrelations(t *testing.T) {
	server, alice, bob := analyticsScopeServer()
	now := time.Now().UTC()
	events := sliceAnalyticsSource{
		{ID: "public", Time: now, Type: "doc.read", InvocationID: "public-inv", ParentID: "public-command", Fields: map[string]any{"path": "/docs/readable.md"}},
		{ID: "deleted", Time: now, Type: "doc.write", Fields: map[string]any{"path": "/docs/deleted.md", "action": "delete"}},
		{ID: "private", Time: now, Type: "doc.read", InvocationID: "private-inv", Fields: map[string]any{"path": "/docs/private/secret.md"}},
		{ID: "sibling", Time: now, Type: "doc.read", Fields: map[string]any{"path": "/sibling/note.md"}},
		{ID: "safe-search", Time: now, Type: "search.query", Fields: map[string]any{"scope": []string{"/docs/readable.md"}, "pattern": "safe"}},
		{ID: "carved-search", Time: now, Type: "search.query", Fields: map[string]any{"scope": []string{"/docs"}, "pattern": "carved"}},
		{ID: "mixed-search", Time: now, Type: "search.query", Fields: map[string]any{"scope": []string{"/docs/readable.md", "/docs/private/secret.md"}, "pattern": "mixed"}},
		{ID: "public-command", Time: now, Type: "command.exec", InvocationID: "public-inv", Fields: map[string]any{"command": "cat"}},
		{ID: "unscoped-command", Time: now, Type: "command.exec", InvocationID: "unscoped", Fields: map[string]any{"command": "pwd"}},
		{ID: "mixed-command", Time: now, Type: "command.exec", InvocationID: "mixed-inv", Fields: map[string]any{"command": "grep"}},
		{ID: "mixed-public", Time: now, Type: "doc.hit", InvocationID: "mixed-inv", ParentID: "mixed-command", Fields: map[string]any{"path": "/docs/readable.md"}},
		{ID: "mixed-private", Time: now, Type: "doc.hit", InvocationID: "mixed-inv", ParentID: "mixed-command", Fields: map[string]any{"path": "/docs/private/secret.md"}},
	}

	collect := func(id Identity, prefix string) map[string]bool {
		t.Helper()
		got := map[string]bool{}
		source := &dashboardEventSource{server: server, identity: id, prefix: prefix, source: events}
		if err := source.Scan(context.Background(), analytics.EventFilter{}, func(event analytics.Event) error {
			got[event.ID] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}

	aliceDocs := collect(alice, "/docs")
	for _, id := range []string{"public", "deleted", "safe-search", "public-command", "mixed-public"} {
		if !aliceDocs[id] {
			t.Errorf("alice docs missing %s", id)
		}
	}
	for _, id := range []string{"private", "sibling", "carved-search", "mixed-search", "unscoped-command", "mixed-command", "mixed-private"} {
		if aliceDocs[id] {
			t.Errorf("alice docs leaked %s", id)
		}
	}
	if got := collect(bob, "/docs/private"); !got["private"] || got["public"] || got["deleted"] {
		t.Fatalf("bob private scope = %#v", got)
	}
	aliceRoot := collect(alice, "/")
	if !aliceRoot["sibling"] || aliceRoot["carved-search"] || aliceRoot["mixed-search"] || aliceRoot["private"] {
		t.Fatalf("alice root scope = %#v", aliceRoot)
	}
}

func TestDashboardEventSourceOnlyCorrelatesCommandsAndPoisonsAmbiguousSession(t *testing.T) {
	server, alice, _ := analyticsScopeServer()
	now := time.Now().UTC()
	events := sliceAnalyticsSource{
		{ID: "session-start", Time: now, Type: "session.start", SessionID: "session"},
		{ID: "command", Time: now, Type: "command.exec", SessionID: "session", InvocationID: "invocation", Fields: map[string]any{"command": "cat"}},
		{ID: "read", Time: now, Type: "doc.read", SessionID: "session", InvocationID: "invocation", ParentID: "command", Fields: map[string]any{"path": "/docs/readable.md"}},
		{ID: "unscoped-search", Time: now, Type: "search.query", SessionID: "session", InvocationID: "invocation", Fields: map[string]any{"pattern": "secret terms"}},
		{ID: "plugin-event", Time: now, Type: "plugin.private", SessionID: "session", InvocationID: "invocation", Fields: map[string]any{"secret": "sensitive"}},
		{ID: "session-end", Time: now, Type: "session.end", SessionID: "session"},
	}

	got := map[string]bool{}
	source := &dashboardEventSource{server: server, identity: alice, prefix: "/docs", source: events}
	if err := source.Scan(context.Background(), analytics.EventFilter{}, func(event analytics.Event) error {
		got[event.ID] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !got["command"] || !got["read"] {
		t.Fatalf("authorized command correlation missing: %#v", got)
	}
	for _, id := range []string{"unscoped-search", "plugin-event", "session-start", "session-end"} {
		if got[id] {
			t.Errorf("ambiguous event leaked: %s", id)
		}
	}
}

func TestDashboardEventSourceCanonicalizesCopiedResourceFields(t *testing.T) {
	server, alice, _ := analyticsScopeServer()
	originalPath := "/knowledge/readable.md"
	originalScope := []string{"/knowledge/readable.md"}
	events := sliceAnalyticsSource{
		{ID: "alias-read", Type: "doc.read", Fields: map[string]any{"path": originalPath}},
		{ID: "alias-search", Type: "search.query", Fields: map[string]any{"scope": originalScope}},
	}
	source := &dashboardEventSource{server: server, identity: alice, prefix: "/docs", source: events}
	got := map[string]analytics.Event{}
	if err := source.Scan(context.Background(), analytics.EventFilter{}, func(event analytics.Event) error {
		got[event.ID] = event
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got["alias-read"].Fields["path"] != "/docs/readable.md" {
		t.Fatalf("canonical path = %#v", got["alias-read"].Fields["path"])
	}
	scope, ok := got["alias-search"].Fields["scope"].([]string)
	if !ok || len(scope) != 1 || scope[0] != "/docs/readable.md" {
		t.Fatalf("canonical scope = %#v", got["alias-search"].Fields["scope"])
	}
	sourceScope, ok := events[1].Fields["scope"].([]string)
	if events[0].Fields["path"] != originalPath || !ok || len(sourceScope) != 1 || sourceScope[0] != originalScope[0] {
		t.Fatalf("source fields were mutated: path=%#v scope=%#v", events[0].Fields["path"], events[1].Fields["scope"])
	}
}

func TestAnalyticsActorClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		in   Attribution
		want analytics.Writer
	}{
		{"direct principal", Attribution{Principal: "alice"}, analytics.WriterHuman},
		{"delegated actor", Attribution{Principal: "alice", Actor: "claude@claude.ai"}, analytics.WriterAgent},
		{"explicit human", Attribution{Principal: "alice", Extra: map[string]string{"actor_kind": "human"}}, analytics.WriterHuman},
		{"explicit agent", Attribution{Principal: "alice", Extra: map[string]string{"actor_kind": "agent"}}, analytics.WriterAgent},
		{"internal operation", Attribution{Principal: "system", internal: true}, analytics.WriterAgent},
		{"invalid explicit value", Attribution{Principal: "alice", Extra: map[string]string{"actor_kind": "person"}}, analytics.WriterUnknown},
		{"guest", Attribution{Principal: "guest"}, analytics.WriterUnknown},
		{"anonymous", Attribution{Principal: "anonymous"}, analytics.WriterUnknown},
		{"missing attribution", Attribution{}, analytics.WriterUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyAttribution(test.in); got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAnalyticsEventClassifiesDirectAndDelegatedCallers(t *testing.T) {
	s := &Server{}
	for _, test := range []struct {
		name string
		id   Identity
		want analytics.Writer
	}{
		{
			name: "direct ssh principal",
			id:   Identity{IdentityName: "adil", Attribution: Attribution{Principal: "adil"}, Transport: "ssh"},
			want: analytics.WriterHuman,
		},
		{
			name: "delegated mcp actor",
			id:   Identity{IdentityName: "adil", Attribution: Attribution{Principal: "adil", Actor: "claude@claude.ai"}, Transport: "mcp"},
			want: analytics.WriterAgent,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := s.analyticsEvent(test.id, "command.exec", nil)
			if got := event.Fields["actor_kind"]; got != string(test.want) {
				t.Fatalf("actor_kind = %q, want %q", got, test.want)
			}
			if event.Actor != test.id.Attribution.Actor || event.Transport != test.id.Transport {
				t.Fatalf("event attribution = %#v", event)
			}
		})
	}
}

func TestDashboardEventSourceDiscardsStalePolicySnapshot(t *testing.T) {
	server, alice, _ := analyticsScopeServer()
	policy, err := server.currentPolicy(alice)
	if err != nil {
		t.Fatal(err)
	}
	alice.policySnapshot = &policy
	source := &dashboardEventSource{server: server, identity: alice, prefix: "/docs", source: sliceAnalyticsSource{
		{ID: "read", Type: "doc.read", Fields: map[string]any{"path": "/docs/readable.md"}},
	}}
	count := func() int {
		t.Helper()
		n := 0
		if err := source.Scan(context.Background(), analytics.EventFilter{}, func(analytics.Event) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("readable event was omitted")
	}
	server.authorizationStore.(identityAuthorizationStore)["alice"] = AuthorizationPolicy{IdentityName: "alice"}
	if count() != 0 {
		t.Fatal("reused analytics source retained a removed role")
	}
	if len(alice.policySnapshot.Roles) != 1 {
		t.Fatal("scan mutated caller snapshot")
	}
}
