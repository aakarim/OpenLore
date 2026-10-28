package openlore

import (
	"errors"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
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
