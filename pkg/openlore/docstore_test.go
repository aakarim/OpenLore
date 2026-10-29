package openlore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"hegel.dev/go/hegel"
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
	s.auth.Identities = append(s.auth.Identities, config.AuthIdentity{Name: "bob"})
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

// All security properties enter through the real bearer middleware and HTTP
// handler. In particular, an unrecognized subject is not a named identity.
func newDocstoreSecurityServer(t *testing.T) *Server {
	t.Helper()
	s := newTokenTestServer(t, true, "allow")
	s.auth.Identities = append(s.auth.Identities, config.AuthIdentity{Name: "bob"})
	var err error
	s.docstore, err = OpenDocstore(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type docstoreTestTB interface {
	Helper()
	Fatalf(string, ...any)
}

func docstoreRequest(t docstoreTestTB, s *Server, token, method, endpoint string, body []byte, status int) []byte {
	t.Helper()
	r := httptest.NewRequest(method, "/api/docstore/"+endpoint, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.authMiddleware(s.docstoreHandler("/api/docstore/"), true).ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status %d want %d: %s", method, endpoint, w.Code, status, w.Body.String())
	}
	return w.Body.Bytes()
}

type docstorePrepared struct {
	Snapshot string   `json:"snapshot"`
	Missing  []string `json:"missing"`
}

func docstorePrepare(t docstoreTestTB, s *Server, token, store string, req SnapshotRequest, status int) docstorePrepared {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b = docstoreRequest(t, s, token, "POST", store+"/snapshots", b, status)
	var p docstorePrepared
	if status == 200 {
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("prepare response: %v", err)
		}
	}
	return p
}

func securityFile(body []byte, size int64) SourceFile {
	return SourceFile{SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Size: size, Mode: 0644}
}

func TestDocstorePublicOAuthCannotClaim(t *testing.T) {
	s := newDocstoreSecurityServer(t)
	verifier, challenge := pkcePair()
	const redirect = "http://127.0.0.1:52123/callback"
	w, authz := runAuthorize(t, s, url.Values{
		"response_type": {"code"}, "client_id": {"obsidian"}, "redirect_uri": {redirect},
		"scope": {ScopeFull}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
	})
	if w.Code != 200 || authz == "" {
		t.Fatalf("authorize: %d %s", w.Code, w.Body.String())
	}
	post := func(endpoint string, form url.Values, status int) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		for p, h := range s.oauthRoutes() {
			mux.Handle(p, h)
		}
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body.String())
		}
		return w
	}
	w = post(authorizePublicPath, url.Values{"authz": {authz}}, 302)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	w = post(tokenPath, url.Values{
		"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")},
		"client_id": {"obsidian"}, "redirect_uri": {redirect}, "code_verifier": {verifier},
	}, 200)
	var token tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &token); err != nil || token.AccessToken == "" {
		t.Fatalf("token response: %s (%v)", w.Body.String(), err)
	}
	if token.Scope != ScopeFull {
		t.Fatalf("expected the public full-scope flow, got scope %q", token.Scope)
	}
	docstorePrepare(t, s, token.AccessToken, "git/example.com/team/repo", SnapshotRequest{Files: map[string]SourceFile{}}, 403)
	states, err := s.docstore.states()
	if err != nil || len(states) != 0 {
		t.Fatalf("public token persisted a claim: %+v %v", states, err)
	}
}

func TestDocstoreIdentityProperties(t *testing.T) {
	hegel.Test(t, func(t *hegel.T) {
		s := newDocstoreSecurityServer(t.T)
		subject := hegel.Draw(t, hegel.SampledFrom([]string{"alice", "bob", anonymousSubject, "unknown-subject", "guest"}))
		scope := hegel.Draw(t, hegel.SampledFrom([]string{ScopeFull, ScopeRead}))
		token := mint(t.T, s, subject, scope)
		store := hegel.Draw(t, hegel.SampledFrom([]string{"git/example.com/team/repo", "folder/" + subject + "/notes"}))
		allowed := (subject == "alice" || subject == "bob") && scope == ScopeFull
		status := 403
		if allowed {
			status = 200
		}
		p := docstorePrepare(t, s, token, store, SnapshotRequest{Files: map[string]SourceFile{}}, status)
		if allowed {
			docstoreRequest(t, s, token, "POST", store+"/snapshots/"+p.Snapshot+"/commit", nil, 200)
			other := "alice"
			if subject == other {
				other = "bob"
			}
			status := 403
			if strings.HasPrefix(store, "git/") {
				status = 409 // namespace is valid, but durable ownership differs
			}
			docstorePrepare(t, s, mint(t.T, s, other, ScopeFull), store, SnapshotRequest{Parent: p.Snapshot, Files: map[string]SourceFile{}}, status)
		}
		states, err := s.docstore.states()
		if err != nil || allowed && (len(states) != 1 || states[0].Owner != subject) || !allowed && len(states) != 0 {
			t.Fatalf("claim authority: allowed=%v states=%+v err=%v", allowed, states, err)
		}
	}, hegel.WithTestCases(100))
}

// Knowing a digest, having a read token, and owning another source do not prove
// possession. Verified uploads do, including across retries, GC and reopen.
func TestDocstoreBlobAuthorityProperties(t *testing.T) {
	hegel.Test(t, func(t *hegel.T) {
		s := newDocstoreSecurityServer(t.T)
		body := hegel.Draw(t, hegel.Binary(1, 512))
		owner := hegel.Draw(t, hegel.SampledFrom([]string{"alice", "bob"}))
		other := "bob"
		if owner == other {
			other = "alice"
		}
		if hegel.Draw(t, hegel.Booleans()) {
			other = owner // source-scoped, even for the same principal
		}
		token, otherToken := mint(t.T, s, owner, ScopeFull), mint(t.T, s, other, ScopeFull)
		store, target := "folder/"+owner+"/private", "folder/"+other+"/copy"
		file := securityFile(body, int64(len(body)))
		req := SnapshotRequest{Files: map[string]SourceFile{"secret.txt": file}}
		p := docstorePrepare(t, s, token, store, req, 200)
		docstoreRequest(t, s, token, "PUT", "blobs/"+file.SHA256, body, 200)
		if hegel.Draw(t, hegel.Booleans()) {
			docstoreRequest(t, s, token, "POST", store+"/snapshots/"+p.Snapshot+"/commit", nil, 200)
		}
		if other != owner {
			got := docstoreRequest(t, s, otherToken, "GET", store+"?file=secret.txt", nil, 200)
			if string(got) != "[]\n" {
				t.Fatalf("private source visible: %s", got)
			}
		}
		p = docstorePrepare(t, s, otherToken, target, req, 200)
		if len(p.Missing) != 1 || p.Missing[0] != file.SHA256 {
			t.Fatalf("global CAS presence granted authority: missing=%v", p.Missing)
		}
		docstoreRequest(t, s, otherToken, "POST", target+"/snapshots/"+p.Snapshot+"/commit", nil, 409)
		docstoreRequest(t, s, otherToken, "GET", target+"?file=secret.txt", nil, 404)
		docstoreRequest(t, s, mint(t.T, s, other, ScopeRead), "PUT", "blobs/"+file.SHA256, body, 403)
		for range hegel.Draw(t, hegel.Integers(1, 3)) {
			if hegel.Draw(t, hegel.Booleans()) {
				var err error
				s.docstore, err = OpenDocstore(s.config.DataDir)
				if err != nil {
					t.Fatal(err)
				}
			}
			docstoreRequest(t, s, otherToken, "PUT", "blobs/"+file.SHA256, body, 200)
		}
		var err error
		s.docstore, err = OpenDocstore(s.config.DataDir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := GarbageCollectHistoryBlobs(t.Context(), filepath.Join(s.config.DataDir, "history")); err != nil {
			t.Fatal(err)
		}
		p = docstorePrepare(t, s, otherToken, target, req, 200)
		if len(p.Missing) != 0 {
			t.Fatalf("lost durable upload proof: %v", p.Missing)
		}
		for range hegel.Draw(t, hegel.Integers(1, 3)) {
			docstoreRequest(t, s, otherToken, "POST", target+"/snapshots/"+p.Snapshot+"/commit", nil, 200)
		}
		if got := docstoreRequest(t, s, otherToken, "GET", target+"?file=secret.txt", nil, 200); !bytes.Equal(got, body) {
			t.Fatalf("uploaded content changed: got %x want %x", got, body)
		}
		// Model a pre-upgrade source: committed history exists, but the
		// persisted state has no upload proofs. Reuse must rely on history.
		state, err := s.docstore.state(target)
		if err != nil {
			t.Fatal(err)
		}
		state.Uploaded = nil
		if err := s.docstore.save(state); err != nil {
			t.Fatal(err)
		}
		s.docstore, err = OpenDocstore(s.config.DataDir)
		if err != nil {
			t.Fatal(err)
		}
		req = SnapshotRequest{Parent: p.Snapshot, Files: map[string]SourceFile{"renamed.txt": file}}
		p = docstorePrepare(t, s, otherToken, target, req, 200)
		if len(p.Missing) != 0 {
			t.Fatalf("history-only reuse required upload: %v", p.Missing)
		}
		docstoreRequest(t, s, otherToken, "POST", target+"/snapshots/"+p.Snapshot+"/commit", nil, 200)
		stats, err := s.docstore.blobs.Stats(t.Context())
		if err != nil || stats.Objects != 1 || stats.Bytes != int64(len(body)) {
			t.Fatalf("physical deduplication: %+v %v", stats, err)
		}
	}, hegel.WithTestCases(100))
}

func TestDocstoreUploadBudgetProperties(t *testing.T) {
	hegel.Test(t, func(t *hegel.T) {
		s := newDocstoreSecurityServer(t.T)
		body := hegel.Draw(t, hegel.Binary(0, 2048))
		actual := int64(len(body))
		declared := hegel.Draw(t, hegel.SampledFrom([]int64{0, max(0, actual-1), actual, actual + 1, actual + 128}))
		conflict := hegel.Draw(t, hegel.Booleans())
		token := mint(t.T, s, "alice", ScopeFull)
		file := securityFile(body, declared)
		req := SnapshotRequest{Files: map[string]SourceFile{"a": file}}
		status := 200
		if conflict {
			req.Files["b"] = securityFile(body, declared+1)
			status = 409
		}
		p := docstorePrepare(t, s, token, "folder/alice/notes", req, status)
		if !conflict {
			status = 400
			if declared == actual {
				status = 200
			}
			docstoreRequest(t, s, token, "PUT", "blobs/"+file.SHA256, body, status)
			commitStatus := 409
			if declared == actual {
				commitStatus = 200
			}
			docstoreRequest(t, s, token, "POST", "folder/alice/notes/snapshots/"+p.Snapshot+"/commit", nil, commitStatus)
		}
		stats, err := s.docstore.blobs.Stats(t.Context())
		want := BlobStats{}
		if !conflict && declared == actual {
			want = BlobStats{Objects: 1, Bytes: actual}
		}
		if err != nil || stats != want {
			t.Fatalf("declared=%d actual=%d conflict=%v: stored %+v want %+v err=%v", declared, actual, conflict, stats, want, err)
		}
		// A separate, correctly sized declaration must succeed for every
		// generated payload, even alongside a bad pending declaration. This
		// is also a positive control for empty files and global hash reuse.
		req = SnapshotRequest{Files: map[string]SourceFile{"valid": securityFile(body, actual)}}
		good := docstorePrepare(t, s, token, "folder/alice/valid", req, 200)
		badReq := SnapshotRequest{Files: map[string]SourceFile{"invalid": securityFile(body, actual+1)}}
		bad := docstorePrepare(t, s, token, "folder/alice/valid", badReq, 200)
		docstoreRequest(t, s, token, "PUT", "blobs/"+file.SHA256, body, 200)
		docstoreRequest(t, s, token, "POST", "folder/alice/valid/snapshots/"+bad.Snapshot+"/commit", nil, 409)
		if !conflict && declared != actual {
			// Uploading the right content to the good source cannot authorize
			// a wrong-length reference in another pending source.
			docstoreRequest(t, s, token, "POST", "folder/alice/notes/snapshots/"+p.Snapshot+"/commit", nil, 409)
		}
		docstoreRequest(t, s, token, "POST", "folder/alice/valid/snapshots/"+good.Snapshot+"/commit", nil, 200)
		if got := docstoreRequest(t, s, token, "GET", "folder/alice/valid?file=valid", nil, 200); !bytes.Equal(got, body) {
			t.Fatalf("valid upload differs: got %x want %x", got, body)
		}
		stats, err = s.docstore.blobs.Stats(t.Context())
		if err != nil || stats != (BlobStats{Objects: 1, Bytes: actual}) {
			t.Fatalf("valid upload budget: %+v %v", stats, err)
		}
	}, hegel.WithTestCases(150))
}

func TestDocstoreZeroDeclarationCannotAllocateMegabyte(t *testing.T) {
	s := newDocstoreSecurityServer(t)
	token := mint(t, s, "alice", ScopeFull)
	body := bytes.Repeat([]byte("x"), 1<<20)
	file := securityFile(body, 0)
	p := docstorePrepare(t, s, token, "folder/alice/notes", SnapshotRequest{Files: map[string]SourceFile{"large": file}}, 200)
	docstoreRequest(t, s, token, "PUT", "blobs/"+file.SHA256, body, 400)
	var err error
	s.docstore, err = OpenDocstore(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GarbageCollectHistoryBlobs(t.Context(), filepath.Join(s.config.DataDir, "history")); err != nil {
		t.Fatal(err)
	}
	stats, err := s.docstore.blobs.Stats(t.Context())
	if err != nil || stats != (BlobStats{}) {
		t.Fatalf("rejected upload allocated retained storage: %+v %v", stats, err)
	}
	docstoreRequest(t, s, token, "POST", "folder/alice/notes/snapshots/"+p.Snapshot+"/commit", nil, 409)
}
