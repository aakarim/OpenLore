package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/pkg/sftp"
)

func TestProjectedSourceIsFilteredAndReadonly(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	var err error
	s.docstore, err = OpenDocstore(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.auth.Docsets["memory"] = config.DocsetSpec{Projection: &config.ProjectionSpec{Store: "claude/alice/project/memory", Subpath: "notes", Include: []string{"**/*.md"}, Exclude: []string{"private/**"}}, Access: config.DocsetAccess{Allow: map[string]string{"alice": "rw", "guest": "ro"}}}
	if err := config.ValidateAuthConfig(s.auth); err != nil {
		t.Fatal(err)
	}
	if err := s.mountProjections(); err != nil {
		t.Fatal(err)
	}
	req := SnapshotRequest{Files: map[string]SourceFile{}}
	for name, text := range map[string]string{"notes/keep.md": "remember payments", "notes/nested/deep.md": "deep", "notes/private/secret.md": "secret", "notes/data.json": "{}", "elsewhere.md": "hidden"} {
		hash := hashContent([]byte(text))
		req.Files[name] = SourceFile{SHA256: hash, Size: int64(len(text)), Mode: 0644}
		if err := s.docstore.blobs.Put(t.Context(), hash, strings.NewReader(text)); err != nil {
			t.Fatal(err)
		}
	}
	snap, _, err := s.docstore.prepare(t.Context(), "claude/alice/project/memory", "alice", req, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.docstore.commit(t.Context(), "claude/alice/project/memory", "alice", snap.Snapshot); err != nil {
		t.Fatal(err)
	}
	id, _ := s.identityForName("alice")
	id.Scopes = []string{ScopeFull}
	fsys := s.buildSessionFS(id)
	b, err := fsys.ReadFile("/memory/keep.md")
	if err != nil || string(b) != "remember payments" {
		t.Fatalf("read: %q %v", b, err)
	}
	for _, name := range []string{"private/secret.md", "data.json", "elsewhere.md"} {
		if _, err := fsys.ReadFile("/memory/" + name); err == nil {
			t.Fatalf("leaked %s", name)
		}
	}
	entries, err := fsys.ReadDir("/memory")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries: %v %v", entries, err)
	}
	w := fsys.(vfs.WritableFS)
	for _, err := range []error{func() error {
		_, err := w.WriteFileAtomic("/memory/keep.md", []byte("changed"), vfs.WriteOpts{})
		return err
	}(), w.Mkdir("/memory/new"), w.Remove("/memory/keep.md"), w.RemoveAll("/memory/nested", vfs.RemoveOpts{})} {
		if !errors.Is(err, vfs.ErrReadOnly) || !strings.Contains(err.Error(), "claude/alice/project/memory") {
			t.Fatalf("mutation did not name readonly source: %v", err)
		}
	}
	handler := NewSFTPHandler(fsys)
	handler.denied = func(p string) error { return s.mutationDenied(vfs.ChangeActionWrite, p) }
	_, uploadErr := handler.Filewrite(sftp.NewRequest("Put", "/memory/keep.md"))
	for _, err := range []error{uploadErr, handler.Filecmd(sftp.NewRequest("Remove", "/memory/keep.md"))} {
		if err == nil || !strings.Contains(err.Error(), "claude/alice/project/memory") {
			t.Fatalf("SFTP mutation lost source reason: %v", err)
		}
	}
	rows, err := s.sourceRows(id)
	if err != nil || len(rows) != 1 || rows[0].Docset != "memory" {
		t.Fatalf("sources: %v %v", rows, err)
	}
	// The docset ACL alone decides who reads a projection; filters and
	// read-only enforcement still apply to every reader.
	guest := s.buildSessionFS(s.anonymousIdentity())
	if _, err := guest.ReadFile("/memory/keep.md"); err != nil {
		t.Fatal("guest grant on projection unavailable:", err)
	}
	state, err := s.docstore.state("claude/alice/project/memory")
	if err != nil {
		t.Fatal(err)
	}
	view, _, visible := s.visibleSource(s.anonymousIdentity(), state)
	if !visible || len(view.Latest.Files) != 2 {
		t.Fatalf("API leaked excluded files: %+v", view)
	}
}

func TestProjectionBeneathHomeIsReadableByOwnerOnly(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	var err error
	s.docstore, err = OpenDocstore(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.auth.Roles["engineers"] = config.RoleSpec{}
	s.auth.Docsets["alice-home"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/user/alice", Display: "/user/alice"}}}
	s.auth.Docsets["bob-home"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/user/bob", Display: "/user/bob"}}}
	s.auth.Docsets["alice-claude"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/user/alice/claude", Display: "/user/alice/claude"}}, Projection: &config.ProjectionSpec{Store: "claude/alice/project/instructions"}}
	s.auth.Docsets["team-claude"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/channel/x", Display: "/channel/x"}}, Projection: &config.ProjectionSpec{Store: "claude/alice/project/instructions"}, Access: config.DocsetAccess{Allow: map[string]string{"engineers": "ro"}}}
	s.auth.Identities = []config.AuthIdentity{
		{Name: "alice", Roles: []string{"alice", "engineers"}, Home: "alice-home"},
		{Name: "bob", Roles: []string{"engineers"}, Home: "bob-home"},
	}
	if err := config.ValidateAuthConfig(s.auth); err != nil {
		t.Fatal(err)
	}
	if err := s.mountProjections(); err != nil {
		t.Fatal(err)
	}
	text := "always run the tests"
	hash := hashContent([]byte(text))
	if err := s.docstore.blobs.Put(t.Context(), hash, strings.NewReader(text)); err != nil {
		t.Fatal(err)
	}
	req := SnapshotRequest{Files: map[string]SourceFile{"CLAUDE.md": {SHA256: hash, Size: int64(len(text)), Mode: 0644}}}
	snap, _, err := s.docstore.prepare(t.Context(), "claude/alice/project/instructions", "alice", req, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.docstore.commit(t.Context(), "claude/alice/project/instructions", "alice", snap.Snapshot); err != nil {
		t.Fatal(err)
	}
	identity := func(name string) Identity {
		id, ok := s.identityForName(name)
		if !ok {
			t.Fatalf("unknown identity %s", name)
		}
		id.Scopes = []string{ScopeFull}
		return id
	}
	alice, bob := s.buildSessionFS(identity("alice")), s.buildSessionFS(identity("bob"))
	// No access block: only the home owner reads a projection beneath their home.
	if b, err := alice.ReadFile("/user/alice/claude/CLAUDE.md"); err != nil || string(b) != text {
		t.Fatalf("owner read through home projection: %q %v", b, err)
	}
	entries, err := alice.ReadDir("/user/alice")
	if err != nil || len(entries) != 1 || entries[0].FileName != "claude" || !entries[0].Dir {
		t.Fatalf("home listing: %+v %v", entries, err)
	}
	if _, err := bob.ReadFile("/user/alice/claude/CLAUDE.md"); err == nil {
		t.Fatal("projection beneath another identity's home leaked")
	}
	if _, err := bob.ReadDir("/user/alice/claude"); err == nil {
		t.Fatal("projection beneath another identity's home listed")
	}
	if _, err := alice.(vfs.WritableFS).WriteFileAtomic("/user/alice/claude/CLAUDE.md", []byte("changed"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("home projection accepted a write: %v", err)
	}
	// With an access block, the ACL decides exactly like any other docset.
	for name, fsys := range map[string]vfs.FileSystem{"alice": alice, "bob": bob} {
		if b, err := fsys.ReadFile("/channel/x/CLAUDE.md"); err != nil || string(b) != text {
			t.Fatalf("%s read through shared projection: %q %v", name, b, err)
		}
	}
	if _, err := s.buildSessionFS(s.anonymousIdentity()).ReadFile("/channel/x/CLAUDE.md"); err == nil {
		t.Fatal("ACL ignored for guest")
	}
	rows, err := s.sourceRows(identity("bob"))
	if err != nil || len(rows) != 1 || rows[0].Docset != "team-claude" {
		t.Fatalf("bob sources: %v %v", rows, err)
	}
	rows, err = s.sourceRows(identity("alice"))
	if err != nil || len(rows) != 1 || rows[0].Docset != "alice-claude,team-claude" {
		t.Fatalf("alice sources: %v %v", rows, err)
	}
}

func seedProjectionSource(t *testing.T, s *Server, store string, files map[string]string) {
	t.Helper()
	req := SnapshotRequest{Files: map[string]SourceFile{}}
	for name, text := range files {
		hash := hashContent([]byte(text))
		req.Files[name] = SourceFile{SHA256: hash, Size: int64(len(text)), Mode: 0644}
		if err := s.docstore.blobs.Put(t.Context(), hash, strings.NewReader(text)); err != nil {
			t.Fatal(err)
		}
	}
	snap, _, err := s.docstore.prepare(t.Context(), store, "importer", req, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.docstore.commit(t.Context(), store, "importer", snap.Snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestConfigReloadUpdatesExistingProjectionSessions(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil)}, nil)
	role := auth.Roles["writer"]
	role.Allow.Capabilities = []string{"lore:config:edit"}
	auth.Roles["writer"] = role
	auth.Roles["reader"] = config.RoleSpec{}
	auth.Identities = append(auth.Identities, config.AuthIdentity{Name: "bob", Roles: []string{"reader"}})
	s, root := bootRulesServer(t, auth, nil)
	seedProjectionSource(t, s, "folder/importer/first", map[string]string{"docs/keep.md": "first", "docs/private.md": "private", "other/keep.md": "other", "docs/data.json": "{}"})
	seedProjectionSource(t, s, "folder/importer/second", map[string]string{"notes/keep.md": "second"})
	alice := testIdentity("alice")
	bob := testIdentity("bob")
	admin, reader := s.buildSessionShell(alice), s.buildSessionShell(bob)
	fsys := s.buildSessionFS(alice).(vfs.WritableFS)
	stage := func() {
		t.Helper()
		b, err := json.Marshal(auth)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.WriteFileAtomic(authConfigVFSPath, b, vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	reload := func() {
		t.Helper()
		if _, stderr, code := run(admin, "lore config reload"); code != 0 {
			t.Fatalf("reload: %s", stderr)
		}
	}
	read := func(target, want string) {
		t.Helper()
		if b, err := fsys.ReadFile(target); err != nil || string(b) != want {
			t.Fatalf("%s: %q, %v; want %q", target, b, err, want)
		}
	}
	missing := func(target string) {
		t.Helper()
		if _, err := fsys.ReadFile(target); !os.IsNotExist(err) {
			t.Fatalf("%s: want missing, got %v", target, err)
		}
	}
	auth.Docsets["imported"] = config.DocsetSpec{
		Paths: []config.PathMapping{{Source: "/docs/imported"}}, Aliases: []string{"/knowledge"},
		Projection: &config.ProjectionSpec{Store: "folder/importer/first", Subpath: "docs", Include: []string{"**/*.md"}, Exclude: []string{"private.md"}},
		Access:     config.DocsetAccess{Allow: map[string]string{"writer": "rw"}},
	}
	stage()
	missing("/knowledge/keep.md") // Edits remain staged until explicit reload.
	reload()
	read("/knowledge/keep.md", "first")
	read("/docs/imported/keep.md", "first")
	missing("/knowledge/private.md")
	missing("/knowledge/data.json")
	if out, stderr, code := run(admin, "cat /knowledge/keep.md"); code != 0 || out != "first" {
		t.Fatalf("existing shell: %q %s", out, stderr)
	}
	if out, _, _ := run(admin, "lore docsets"); !strings.Contains(out, "/knowledge") {
		t.Fatalf("stale docsets: %s", out)
	}
	if _, _, code := run(reader, "cat /docs/imported/keep.md"); code == 0 {
		t.Fatal("ungranted reader saw projection")
	}
	if _, err := fsys.WriteFileAtomic("/knowledge/keep.md", []byte("changed"), vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("projection writable: %v", err)
	}

	ds := auth.Docsets["imported"]
	// Revoking the nested grant must carve it out of the still-writable parent.
	ds.Access = config.DocsetAccess{}
	auth.Docsets["imported"] = ds
	stage()
	reload()
	missing("/docs/imported/keep.md")
	missing("/knowledge/keep.md")
	if _, err := fsys.Stat("/docs/imported"); !os.IsNotExist(err) {
		t.Fatalf("revoked projection stat: %v", err)
	}
	if entries, err := fsys.ReadDir("/docs"); err != nil || len(entries) != 0 {
		t.Fatalf("revoked projection listed through parent: %v, %v", entries, err)
	}

	// Move the mount and alias, switch store/subpath, and change its audience.
	ds.Paths = []config.PathMapping{{Source: "/moved"}}
	ds.Aliases = []string{"/new-alias"}
	ds.Projection = &config.ProjectionSpec{Store: "folder/importer/second", Subpath: "notes"}
	ds.Access = config.DocsetAccess{Allow: map[string]string{"reader": "ro"}}
	auth.Docsets["imported"] = ds
	stage()
	reload()
	missing("/knowledge/keep.md")
	missing("/docs/imported/keep.md")
	missing("/moved/keep.md")
	if out, stderr, code := run(reader, "cat /new-alias/keep.md"); code != 0 || out != "second" {
		t.Fatalf("new audience: %q %s", out, stderr)
	}
	if out, _, _ := run(admin, "lore docsets"); strings.Contains(out, "imported") {
		t.Fatalf("revoked docset still listed: %s", out)
	}

	// Ordinary docset paths and both rule layers also change without reconnecting.
	if err := os.MkdirAll(filepath.Join(root, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	docs := auth.Docsets["docs"]
	docs.Paths = []config.PathMapping{{Source: "/other"}}
	docs.Rules = map[string]rules.RuleSpec{"short": {Use: "size/lines", Match: []string{"**/*.md"}, With: map[string]any{"max": 1}}}
	auth.Docsets["docs"] = docs
	stage()
	reload()
	if _, err := fsys.WriteFileAtomic("/docs/old.md", []byte("old"), vfs.WriteOpts{}); err == nil {
		t.Fatal("old path writable")
	}
	if _, err := fsys.WriteFileAtomic("/other/long.md", []byte("one\ntwo\n"), vfs.WriteOpts{}); err == nil || !strings.Contains(err.Error(), "short") {
		t.Fatalf("docset rule not live: %v", err)
	}
	if _, err := fsys.WriteFileAtomic("/other/one.md", []byte("one\n"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	docs.Rules = nil
	auth.Docsets["docs"] = docs
	auth.Rules = map[string]rules.RuleSpec{"global": {Use: "size/lines", Match: []string{"**/*.md"}, With: map[string]any{"max": 2}}}
	delete(auth.Docsets, "imported")
	stage()
	reload()
	if _, _, code := run(reader, "cat /new-alias/keep.md"); code == 0 {
		t.Fatal("removed projection still mounted")
	}
	if _, err := fsys.WriteFileAtomic("/other/two.md", []byte("one\ntwo\n"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("removed docset rule retained: %v", err)
	}
	if _, err := fsys.WriteFileAtomic("/other/three.md", []byte("one\ntwo\nthree\n"), vfs.WriteOpts{}); err == nil || !strings.Contains(err.Error(), "global") {
		t.Fatalf("global rule not live: %v", err)
	}
	if err := fsys.Mkdir("/other/sub"); err != nil {
		t.Fatalf("new mkdir boundary: %v", err)
	}

	// Invalid candidates never replace the persisted file through the config
	// view. External invalid files are left untouched, but never activated.
	good, err := os.ReadFile(s.config.AuthFile)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*config.AuthConfig){
		"rules": func(a *config.AuthConfig) { a.Rules["bad"] = rules.RuleSpec{Use: "not/a-rule", Match: []string{"**"}} },
		"grant": func(a *config.AuthConfig) {
			d := a.Docsets["docs"]
			d.Access.Allow["writer"] = "unknown"
			a.Docsets["docs"] = d
		},
		"alias": func(a *config.AuthConfig) {
			d := a.Docsets["docs"]
			d.Aliases = []string{"/jobs"}
			a.Docsets["docs"] = d
		},
		"system ancestor": func(a *config.AuthConfig) {
			a.Docsets["bad"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/opt"}}, Projection: &config.ProjectionSpec{Store: "folder/importer/first"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var bad config.AuthConfig
			if err := json.Unmarshal(good, &bad); err != nil {
				t.Fatal(err)
			}
			mutate(&bad)
			b, _ := json.Marshal(bad)
			if _, err := fsys.WriteFileAtomic(authConfigVFSPath, b, vfs.WriteOpts{}); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			if persisted, _ := os.ReadFile(s.config.AuthFile); !bytes.Equal(persisted, good) {
				t.Fatal("rejected edit changed persisted config")
			}
			writeAuthFixture(t, s.config.AuthFile, &bad)
			if err := s.Reload(cmds.JobAttribution{Principal: "alice"}); err == nil {
				t.Fatal("invalid external config activated")
			}
			read("/other/two.md", "one\ntwo\n")
			if err := os.WriteFile(s.config.AuthFile, good, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentProjectionReloadDoesNotMixContentAndGrants(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil)}, nil)
	s, _ := bootRulesServer(t, auth, nil)
	seedProjectionSource(t, s, "folder/importer/allowed", map[string]string{"note.md": "allowed"})
	seedProjectionSource(t, s, "folder/importer/denied", map[string]string{"note.md": "SECRET"})
	id := testIdentity("alice")
	fsys := s.buildSessionFS(id)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errors := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, target := range []string{"/shared/note.md", "/alias/note.md"} {
					b, err := fsys.ReadFile(target)
					if err == nil && string(b) != "allowed" {
						errors <- fmt.Errorf("leaked %s: %q", target, b)
						return
					}
				}
				// Source metadata must use the same projection/filter policy too.
				r := httptest.NewRequest("GET", "/api/docstore/folder/importer/denied", nil)
				r = r.WithContext(contextWithIdentity(r.Context(), id))
				w := httptest.NewRecorder()
				s.docstoreHandler("/api/docstore/").ServeHTTP(w, r)
				if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
					errors <- fmt.Errorf("denied source status %d: %s", w.Code, w.Body.String())
					return
				}
			}
		})
	}
	for i := range 60 {
		store := "folder/importer/allowed"
		access := config.DocsetAccess{Allow: map[string]string{"writer": "ro"}}
		if i%2 != 0 {
			store = "folder/importer/denied"
			access = config.DocsetAccess{}
		}
		auth.Docsets["shared"] = config.DocsetSpec{Projection: &config.ProjectionSpec{Store: store}, Aliases: []string{"/alias"}, Access: access}
		writeAuthFixture(t, s.config.AuthFile, auth)
		if err := s.Reload(cmds.JobAttribution{Principal: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestReloadRechecksInFlightWrites(t *testing.T) {
	for _, change := range []string{"grant", "rules", "projection", "canonical target"} {
		t.Run(change, func(t *testing.T) {
			auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil)}, nil)
			s, root := bootRulesServer(t, auth, nil)
			admitted, release := make(chan struct{}), make(chan struct{})
			s.writeMW = append(s.writeMW, func(next WriteHandler) WriteHandler {
				return func(ctx context.Context, op WriteOp) (WriteResult, error) {
					close(admitted)
					<-release
					return next(ctx, op)
				}
			})
			w := s.buildSessionFS(testIdentity("alice")).(vfs.WritableFS)
			result := make(chan error, 1)
			go func() {
				_, err := w.WriteFileAtomic("/docs/new.md", []byte("one\ntwo\n"), vfs.WriteOpts{})
				result <- err
			}()
			<-admitted
			ds := auth.Docsets["docs"]
			switch change {
			case "grant":
				ds.Access.Allow["writer"] = "ro"
			case "rules":
				ds.Rules = map[string]rules.RuleSpec{"short": {Use: "size/lines", Match: []string{"**/*.md"}, With: map[string]any{"max": 1}}}
			case "projection":
				ds.Projection = &config.ProjectionSpec{Store: "folder/importer/source"}
			case "canonical target":
				ds.Paths = []config.PathMapping{{Source: "/other"}}
				ds.Aliases = []string{"/docs"}
				if err := os.Mkdir(filepath.Join(root, "other"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			auth.Docsets["docs"] = ds
			writeAuthFixture(t, s.config.AuthFile, auth)
			err := s.Reload(cmds.JobAttribution{Principal: "alice"})
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-result; err == nil {
				t.Fatal("stale in-flight write committed")
			}
			if _, err := os.Stat(filepath.Join(root, "docs", "new.md")); !os.IsNotExist(err) {
				t.Fatalf("substrate changed: %v", err)
			}
		})
	}
}

func TestReloadCarvesPrivateMetadataOutOfExistingSessions(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil)}, nil)
	s, root := bootRulesServer(t, auth, nil)
	id := testIdentity("alice")
	sh := s.buildSessionShell(id)
	w := s.buildSessionFS(id).(vfs.WritableFS)
	if err := w.Mkdir("/docs/private"); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"public.md": "public", "private/secret.md": "secret"} {
		if _, err := w.WriteFileAtomic("/docs/"+name, []byte(text), vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	request := func(endpoint string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/docstore/openlore/docs"+endpoint, nil)
		r = r.WithContext(contextWithIdentity(r.Context(), id))
		w := httptest.NewRecorder()
		s.docstoreHandler("/api/docstore/").ServeHTTP(w, r)
		return w
	}
	if resp := request("?file=private/secret.md"); resp.Code != 200 || resp.Body.String() != "secret" {
		t.Fatalf("initial native source read: %d %s", resp.Code, resp.Body)
	}
	if out, stderr, code := run(sh, "history"); code != 0 || !strings.Contains(out, "private/secret.md") {
		t.Fatalf("initial history: %q %s", out, stderr)
	}
	// A newly configured private boundary must also hide files captured in
	// snapshots and history before that boundary existed.
	auth.Docsets["private"] = docset("/docs/private", nil)
	writeAuthFixture(t, s.config.AuthFile, auth)
	if err := s.Reload(cmds.JobAttribution{Principal: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFile("/docs/private/secret.md"); !os.IsNotExist(err) {
		t.Fatalf("private read after reload: %v", err)
	}
	if resp := request("?file=private/secret.md"); resp.Code != 404 {
		t.Errorf("revoked native source read: %d %s", resp.Code, resp.Body)
	}
	for _, endpoint := range []string{"", "/snapshots"} {
		resp := request(endpoint)
		if resp.Code != 200 || !strings.Contains(resp.Body.String(), "public.md") || strings.Contains(resp.Body.String(), "secret.md") || strings.Contains(resp.Body.String(), hashContent([]byte("secret"))) {
			t.Errorf("native metadata %s: %d %s", endpoint, resp.Code, resp.Body)
		}
	}
	if out, stderr, code := run(sh, "history"); code != 0 || !strings.Contains(out, "public.md") || strings.Contains(out, "secret.md") || strings.Contains(out, hashContent([]byte("secret"))) {
		t.Errorf("revoked history: %q %s", out, stderr)
	}
	// A deeper explicit grant remains visible inside the private carve-out.
	if err := os.MkdirAll(filepath.Join(root, "docs/private/shared"), 0o700); err != nil {
		t.Fatal(err)
	}
	auth.Docsets["shared"] = config.DocsetSpec{Paths: []config.PathMapping{{Source: "/docs/private/shared"}}, Access: config.DocsetAccess{Allow: map[string]string{"writer": "rw"}}}
	writeAuthFixture(t, s.config.AuthFile, auth)
	if err := s.Reload(cmds.JobAttribution{Principal: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteFileAtomic("/docs/private/shared/allowed.md", []byte("allowed"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := run(sh, "history"); code != 0 || !strings.Contains(out, "allowed.md") || strings.Contains(out, "secret.md") {
		t.Fatalf("nested history regrant: %q %s", out, stderr)
	}
}
