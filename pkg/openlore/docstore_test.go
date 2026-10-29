package openlore

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestNativeWriteSnapshotsAndJournal(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "existing.md"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(t.TempDir(), "lore.json")
	if err := os.WriteFile(auth, []byte(`{"roles":{"alice":{}},"identities":[{"name":"alice","roles":["alice"]}],"docsets":{"docs":{"paths":["/docs"],"access":{"allow":{"alice":"rw"}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(root, WithReadonly(false), config.WithDataDir(data), config.WithAuthFile(auth))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.writeLog.Close(t.Context()) })
	id, _ := s.identityForName("alice")
	id.Scopes = []string{ScopeFull}
	w := s.buildSessionFS(id).(vfs.WritableFS)
	for _, text := range []string{"first", "second"} {
		if _, err := w.WriteFileAtomic("/docs/new.md", []byte(text), vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Remove("/docs/new.md"); err != nil {
		t.Fatal(err)
	}
	state, err := s.docstore.state("openlore/docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Snapshots) != 3 {
		t.Fatalf("snapshots: %+v", state)
	}
	for i, snap := range state.Snapshots {
		if snap.Files["existing.md"].SHA256 != hashContent([]byte("retained")) {
			t.Fatal("lost untouched file")
		}
		if i < 2 {
			want := []string{"first", "second"}[i]
			if snap.Files["new.md"].SHA256 != hashContent([]byte(want)) {
				t.Fatal("incorrect version hash")
			}
		} else if len(snap.Files) != 1 || snap.Deleted != 1 {
			t.Fatal("deletion not reflected")
		}
	}
	cursor, err := OpenHistoryCursor(filepath.Join(data, "history", "commits.jsonl"), HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.(io.Closer).Close()
	for i := range 3 {
		record, ok, err := cursor.Next(t.Context())
		if err != nil || !ok || record.DocstoreSnapshots["openlore/docs"] != state.Snapshots[i].Snapshot || len(record.Leaves) != 1 {
			t.Fatalf("journal %d: %+v %v", i, record, err)
		}
		if i < 2 && record.Leaves[0].AfterHash != state.Snapshots[i].Files["new.md"].SHA256 {
			t.Fatal("journal hash differs from snapshot")
		}
	}
}

func TestDocstoreSnapshots(t *testing.T) {
	s := newTokenTestServer(t, true, "allow")
	var err error
	s.docstore, err = OpenDocstore(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "/api/docstore/"
	ts := httptest.NewServer(s.authMiddleware(s.docstoreHandler(prefix), true))
	defer ts.Close()
	token := mint(t, s, "alice", ScopeFull)
	call := func(method, endpoint, token string, body []byte, status int, out any) {
		t.Helper()
		r, err := http.NewRequest(method, ts.URL+prefix+endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, endpoint, resp.StatusCode, status, b)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s: %v", b, err)
			}
		}
	}
	file := func(b string) SourceFile {
		return SourceFile{SHA256: hashContent([]byte(b)), Size: int64(len(b)), Mode: 0644}
	}
	req := SnapshotRequest{Files: map[string]SourceFile{"keep.md": file("first"), "remove.md": file("remove")}}
	b, _ := json.Marshal(req)
	var pending struct {
		Snapshot string   `json:"snapshot"`
		Missing  []string `json:"missing"`
	}
	call("POST", "folder/alice/notes/snapshots", "", b, 401, nil)
	call("POST", "claude/bob/project/memory/snapshots", token, b, 403, nil)
	call("POST", "folder/alice/notes/snapshots", mint(t, s, "alice", ScopeRead), b, 403, nil)
	call("POST", "folder/alice/notes/snapshots?dry_run=true", token, b, 200, &pending)
	var roots []any
	call("GET", "", token, nil, 200, &roots)
	if len(roots) != 0 {
		t.Fatal("dry run persisted state")
	}
	call("POST", "folder/alice/notes/snapshots", token, b, 200, &pending)
	if len(pending.Missing) != 2 {
		t.Fatalf("missing: %v", pending.Missing)
	}
	first := pending.Snapshot
	call("POST", "folder/alice/notes/snapshots/"+first+"/commit", token, nil, 409, nil)
	call("PUT", "blobs/"+req.Files["keep.md"].SHA256, token, []byte("wrong"), 400, nil)
	for _, text := range []string{"first", "remove"} {
		call("PUT", "blobs/"+hashContent([]byte(text)), token, []byte(text), 200, nil)
	}
	call("POST", "folder/alice/notes/snapshots/"+first+"/commit", token, nil, 200, nil)
	call("POST", "folder/alice/notes/snapshots/"+first+"/commit", token, nil, 200, nil)
	// Retry the original prepare after commit: it returns the same ID.
	call("POST", "folder/alice/notes/snapshots", token, b, 200, &pending)
	if pending.Snapshot != first || len(pending.Missing) != 0 {
		t.Fatal("prepare retry is not idempotent")
	}
	req.Parent = first
	req.Files = map[string]SourceFile{"keep.md": file("changed"), "nested/new.md": file("first")}
	b, _ = json.Marshal(req)
	call("POST", "folder/alice/notes/snapshots", token, b, 200, &pending)
	if len(pending.Missing) != 1 {
		t.Fatalf("expected one new blob, got %v", pending.Missing)
	}
	call("PUT", "blobs/"+hashContent([]byte("changed")), token, []byte("changed"), 200, nil)
	call("POST", "folder/alice/notes/snapshots/"+pending.Snapshot+"/commit", token, nil, 200, nil)
	// Reopen durable state and run the shared history collector.
	s.docstore, err = OpenDocstore(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = GarbageCollectHistoryBlobs(t.Context(), filepath.Join(s.config.DataDir, "history")); err != nil {
		t.Fatal(err)
	}
	var history []SourceSnapshot
	call("GET", "folder/alice/notes/snapshots", token, nil, 200, &history)
	if len(history) != 2 || history[1].Deleted != 1 || len(history[1].Record.Leaves) != 3 || history[0].Files["remove.md"].SHA256 != hashContent([]byte("remove")) {
		t.Fatalf("wrong history: %+v", history)
	}
	reader, _, err := s.docstore.blobs.Get(t.Context(), hashContent([]byte("remove")))
	if err != nil {
		t.Fatal("GC lost historical blob:", err)
	}
	reader.Close()
	call("GET", "", mint(t, s, "bob", ScopeFull), nil, 200, &roots)
	if len(roots) != 0 {
		t.Fatal("another identity can see private sources")
	}
	// A stale parent and traversal names must never advance the source.
	req.Parent = "stale"
	b, _ = json.Marshal(req)
	call("POST", "folder/alice/notes/snapshots", token, b, 409, nil)
	req.Parent = pending.Snapshot
	req.Files["../escape"] = file("first")
	b, _ = json.Marshal(req)
	call("POST", "folder/alice/notes/snapshots", token, b, 409, nil)
}
