package openlore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

var inboxTestNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

type inboxHTTPFixture struct {
	t      *testing.T
	s      *Server
	p      *InboxPlugin
	store  *InboxTokenStore
	mux    *http.ServeMux
	root   string
	tokens map[string]InboxToken
}

type explodingBody struct{ reads int }

func (b *explodingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}
func (*explodingBody) Close() error { return nil }

func newInboxHTTPFixture(t *testing.T) *inboxHTTPFixture {
	t.Helper()
	root, data := t.TempDir(), t.TempDir()
	s, err := NewServer(root, WithReadonly(false), config.WithDataDir(data))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	s.authEnforced = true
	s.auth = &config.AuthConfig{
		Roles:   map[string]config.RoleSpec{"publisher": {}},
		Docsets: map[string]config.DocsetSpec{"docs": {Paths: []config.PathMapping{{Source: "/docs", Display: "/docs"}}, Inbox: "inbox", Access: config.DocsetAccess{Allow: map[string]string{"publisher": "publish"}}}},
		Identities: []config.AuthIdentity{
			{Name: "alice", Roles: []string{"publisher"}},
			{Name: "bob", Docsets: map[string]string{}},
		},
	}
	s.identityStore = serverIdentityStore{s}
	s.authorizationStore = fileAuthorizationStore{auth: s.auth}
	p := NewInboxPlugin()
	p.now = func() time.Time { return inboxTestNow }
	s.RegisterPlugin(p)
	register, err := p.PrepareHTTPRoutes(s)
	if err != nil {
		t.Fatalf("PrepareHTTPRoutes: %v", err)
	}
	mux := http.NewServeMux()
	register(mux)
	store, err := NewInboxTokenStore(data)
	if err != nil {
		t.Fatal(err)
	}
	f := &inboxHTTPFixture{t: t, s: s, p: p, store: store, mux: mux, root: root, tokens: map[string]InboxToken{}}
	f.tokens["alice"] = f.create("alice", nil)
	f.tokens["bob"] = f.create("bob", nil)
	return f
}

func (f *inboxHTTPFixture) create(identity string, expires *time.Time) InboxToken {
	f.t.Helper()
	tok, err := f.store.Create(identity, identity, expires)
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func (f *inboxHTTPFixture) request(method, target, contentType string, body []byte, tok InboxToken) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if tok.ID != "" {
		req.Header.Set("Authorization", "Bearer "+tok.Credential())
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func responsePaths(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response JSON: %v (%s)", err, rec.Body.String())
	}
	return out.Paths
}

func TestInboxHTTPBearerUploadDistinctNamesAndContent(t *testing.T) {
	f := newInboxHTTPFixture(t)
	var paths []string
	for range 2 {
		rec := f.request(http.MethodPost, "/inbox/docs?name=note.md", "text/markdown", []byte("hello"), f.tokens["alice"])
		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		paths = append(paths, responsePaths(t, rec)[0])
	}
	if paths[0] == paths[1] || !strings.HasPrefix(paths[0], "/docs/inbox/") {
		t.Fatalf("paths not distinct/in inbox: %v", paths)
	}
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(f.root, strings.TrimPrefix(p, "/")))
		if err != nil || string(b) != "hello" {
			t.Fatalf("stored %s = %q, %v", p, b, err)
		}
	}
}

func hmacRequest(f *inboxHTTPFixture, body []byte, ts time.Time, signature string) *httptest.ResponseRecorder {
	tok := f.tokens["alice"]
	sec := strconv.FormatInt(ts.Unix(), 10)
	if signature == "valid" {
		mac := hmac.New(sha256.New, []byte(tok.Secret))
		mac.Write([]byte(sec + "."))
		mac.Write(body)
		signature = "t=" + sec + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	}
	req := httptest.NewRequest(http.MethodPost, "/inbox/docs?name=signed.md", bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/markdown")
	req.Header.Set("X-OpenLore-Token-Id", tok.ID)
	req.Header.Set("X-OpenLore-Signature", signature)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func rawHMACRequest(f *inboxHTTPFixture, body []byte, timestamp, signatureHex string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/inbox/docs?name=signed.md", bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/markdown")
	req.Header.Set("X-OpenLore-Token-Id", f.tokens["alice"].ID)
	req.Header.Set("X-OpenLore-Signature", "t="+timestamp+",v1="+signatureHex)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func TestInboxHTTPHMACValidationAndReplay(t *testing.T) {
	tests := []struct {
		name, sig string
		ts        time.Time
		want      int
	}{
		{"valid", "valid", inboxTestNow, 201},
		{"malformed", "nonsense", inboxTestNow, 401},
		{"stale", "valid", inboxTestNow.Add(-5*time.Minute - time.Second), 401},
		{"future", "valid", inboxTestNow.Add(5*time.Minute + time.Second), 401},
		{"bad_mac", "t=" + strconv.FormatInt(inboxTestNow.Unix(), 10) + ",v1=00", inboxTestNow, 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newInboxHTTPFixture(t)
			if got := hmacRequest(f, []byte("signed"), tc.ts, tc.sig).Code; got != tc.want {
				t.Fatalf("status=%d want=%d", got, tc.want)
			}
		})
	}
	t.Run("exact_replay", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		body := []byte("same")
		tok, sec := f.tokens["alice"], strconv.FormatInt(inboxTestNow.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(tok.Secret))
		mac.Write([]byte(sec + "."))
		mac.Write(body)
		sig := "t=" + sec + ",v1=" + hex.EncodeToString(mac.Sum(nil))
		if hmacRequest(f, body, inboxTestNow, sig).Code != 201 || hmacRequest(f, body, inboxTestNow, sig).Code != 401 {
			t.Fatal("expected replay to be rejected")
		}
	})
	t.Run("canonical_variants_are_replays", func(t *testing.T) {
		for _, variant := range []struct {
			name string
			ts   func(string) string
			sig  func(string) string
		}{
			{"uppercase_hex", func(s string) string { return s }, strings.ToUpper},
			{"plus_timestamp", func(s string) string { return "+" + s }, func(s string) string { return s }},
			{"leading_zero_timestamp", func(s string) string { return "0" + s }, func(s string) string { return s }},
		} {
			t.Run(variant.name, func(t *testing.T) {
				f := newInboxHTTPFixture(t)
				body := []byte("canonical")
				canonical := strconv.FormatInt(inboxTestNow.Unix(), 10)
				mac := hmac.New(sha256.New, []byte(f.tokens["alice"].Secret))
				fmt.Fprintf(mac, "%d.", inboxTestNow.Unix())
				mac.Write(body)
				hexSig := hex.EncodeToString(mac.Sum(nil))
				if rawHMACRequest(f, body, canonical, hexSig).Code != 201 {
					t.Fatal("initial signature rejected")
				}
				if rawHMACRequest(f, body, variant.ts(canonical), variant.sig(hexSig)).Code != 401 {
					t.Fatal("canonical encoding variant replay accepted")
				}
			})
		}
	})
	t.Run("future_timestamp_blocked_until_validity_end", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		body := []byte("future")
		ts := inboxTestNow.Add(4 * time.Minute)
		canonical := strconv.FormatInt(ts.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(f.tokens["alice"].Secret))
		fmt.Fprintf(mac, "%d.", ts.Unix())
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		if rawHMACRequest(f, body, canonical, sig).Code != 201 {
			t.Fatal("initial future signature rejected")
		}
		f.p.now = func() time.Time { return ts.Add(5*time.Minute - time.Second) }
		if rawHMACRequest(f, body, canonical, sig).Code != 401 {
			t.Fatal("future signature replay expired before signature validity end")
		}
	})
	t.Run("full_cache_fails_closed", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		bucket := make(map[string]time.Time)
		for i := 0; i < inboxReplayPerToken; i++ {
			bucket[strconv.Itoa(i)] = inboxTestNow.Add(time.Minute)
		}
		f.p.replays[f.tokens["alice"].ID] = bucket
		if got := hmacRequest(f, []byte("new signature"), inboxTestNow, "valid").Code; got != 401 {
			t.Fatalf("full replay cache status=%d", got)
		}
		if len(bucket) != inboxReplayPerToken {
			t.Fatalf("full replay cache evicted a valid entry: len=%d", len(bucket))
		}
	})
	t.Run("expiry_boundary_is_still_replay", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		body := []byte("boundary")
		if hmacRequest(f, body, inboxTestNow, "valid").Code != 201 {
			t.Fatal("initial request failed")
		}
		f.p.now = func() time.Time { return inboxTestNow.Add(5 * time.Minute) }
		if hmacRequest(f, body, inboxTestNow, "valid").Code != 401 {
			t.Fatal("replay accepted at exact expiry boundary")
		}
	})
	t.Run("full_sender_does_not_block_other_token", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		bucket := make(map[string]time.Time)
		for i := 0; i < inboxReplayPerToken; i++ {
			bucket[strconv.Itoa(i)] = inboxTestNow.Add(time.Minute)
		}
		f.p.replays[f.tokens["bob"].ID] = bucket
		if got := hmacRequest(f, []byte("alice isolated"), inboxTestNow, "valid").Code; got != 201 {
			t.Fatalf("other token status=%d", got)
		}
	})
}

func TestInboxHTTPAuthorizationFailuresAndLiveAuthority(t *testing.T) {
	f := newInboxHTTPFixture(t)
	expired := inboxTestNow.Add(-time.Second)
	if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), f.create("alice", &expired)).Code; got != 401 {
		t.Fatalf("expired status=%d", got)
	}
	revoked := f.create("alice", nil)
	_, _ = f.store.Delete(revoked.ID)
	if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), revoked).Code; got != 401 {
		t.Fatalf("revoked status=%d", got)
	}
	if got := f.request("POST", "/inbox/missing?name=x.md", "text/markdown", []byte("x"), f.tokens["alice"]).Code; got != 404 {
		t.Fatalf("unknown docset status=%d", got)
	}
	if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), f.tokens["bob"]).Code; got != 403 {
		t.Fatalf("no grant status=%d", got)
	}
	f.s.auth.Identities[0].Roles = nil
	if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), f.tokens["alice"]).Code; got != 403 {
		t.Fatalf("live removed grant status=%d", got)
	}
}

func TestInboxHTTPInvalidBearerPreauthDoesNotReadBody(t *testing.T) {
	f := newInboxHTTPFixture(t)
	body := &explodingBody{}
	req := httptest.NewRequest(http.MethodPost, "/inbox/missing?name=x.md", nil)
	req.Body = body
	req.Header.Set("Authorization", "Bearer olin_missing_invalid")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || body.reads != 0 {
		t.Fatalf("status=%d body reads=%d", rec.Code, body.reads)
	}
	req = httptest.NewRequest(http.MethodPost, "/inbox/missing?name=x.md", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+f.tokens["alice"].Credential())
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("valid bearer missing docset status=%d", rec.Code)
	}
}

func TestInboxHTTPExactIdentityBindingRejectsAliasAndDeletedIdentity(t *testing.T) {
	t.Run("alias collision", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		f.s.auth.Identities = append([]config.AuthIdentity{{Name: "mallory", Match: []config.IdentityMatch{{Sub: "alice"}}}}, f.s.auth.Identities...)
		if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), f.tokens["alice"]).Code; got != 401 {
			t.Fatalf("exact alice token status=%d", got)
		}
	})
	t.Run("deleted identity", func(t *testing.T) {
		f := newInboxHTTPFixture(t)
		f.s.config.UnknownIdentity = "allow"
		f.s.auth.Identities = f.s.auth.Identities[1:]
		if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), f.tokens["alice"]).Code; got != 401 {
			t.Fatalf("deleted identity status=%d", got)
		}
	})
}

func TestInboxHTTPMediaTypeAndBodyCap(t *testing.T) {
	f := newInboxHTTPFixture(t)
	if got := f.request("POST", "/inbox/docs?name=x.md", "text/plain", []byte("x"), f.tokens["alice"]).Code; got != 415 {
		t.Fatalf("mismatch status=%d", got)
	}
	f.s.config.Inbox.MaxUploadSize = 8
	if got := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("12345678"), f.tokens["alice"]).Code; got != 201 {
		t.Fatalf("exact cap status=%d", got)
	}
	if got := f.request("POST", "/inbox/docs?name=y.md", "text/markdown", []byte("123456789"), f.tokens["alice"]).Code; got != 413 {
		t.Fatalf("cap+1 status=%d", got)
	}
}

func TestInboxHTTPSizePolicyIsIndependentFromDirFS(t *testing.T) {
	f := newInboxHTTPFixture(t)
	large := bytes.Repeat([]byte("x"), 9*1024*1024)
	if got := f.request("POST", "/inbox/docs?name=large.md", "text/markdown", large, f.tokens["alice"]).Code; got != 201 {
		t.Fatalf("inbox large write status=%d", got)
	}
	if _, err := f.s.merge.WriteFileAtomic("/docs/normal.md", large, vfs.WriteOpts{}); err == nil {
		t.Fatal("normal write unexpectedly bypassed substrate size limit")
	}
}

func TestInboxHTTPAudioPolicyIsIsolatedFromOrdinaryWrites(t *testing.T) {
	f := newInboxHTTPFixture(t)
	f.s.config.Inbox.AllowedTypes[".m4a"] = "audio/mp4"
	if got := f.request("POST", "/inbox/docs?name=voice.m4a", "audio/mp4", []byte("audio"), f.tokens["alice"]).Code; got != 201 {
		t.Fatalf("inbox audio status=%d", got)
	}
	if _, err := f.s.merge.WriteFileAtomic("/docs/ordinary.m4a", []byte("audio"), vfs.WriteOpts{}); err == nil {
		t.Fatal("ordinary write inherited inbox audio policy")
	}
}

type captureInboxWrites struct{ ops []WriteOp }

func (c *captureInboxWrites) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			c.ops = append(c.ops, op)
			return next(ctx, op)
		}
	}}
}

func multipartInboxBody(t *testing.T, names ...string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	meta, _ := w.CreateFormField("metadata")
	_, _ = io.WriteString(meta, `{"identity":"forged","received_at":"yesterday","original_filename":"wrong","mime":"evil","custom":"kept"}`)
	for _, name := range names {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
		h.Set("Content-Type", "text/markdown")
		part, _ := w.CreatePart(h)
		_, _ = io.WriteString(part, "# "+name)
	}
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

func TestInboxHTTPMultipartMetadataAtomicAdmissionOrdering(t *testing.T) {
	f := newInboxHTTPFixture(t)
	cap := &captureInboxWrites{}
	f.s.RegisterPlugin(cap)
	body, ct := multipartInboxBody(t, "one.md", "two.md")
	rec := f.request("POST", "/inbox/docs", ct, body, f.tokens["alice"])
	if rec.Code != 201 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	paths := responsePaths(t, rec)
	if len(paths) != 4 || len(cap.ops) != 1 || len(cap.ops[0].Leaves()) != 4 || cap.ops[0].Actor.ID != "alice" {
		t.Fatalf("paths=%v ops=%+v", paths, cap.ops)
	}
	for i := 0; i < 4; i += 2 {
		if paths[i] != paths[i+1]+".json" || cap.ops[0].Leaves()[i].Target != paths[i] || cap.ops[0].Leaves()[i+1].Target != paths[i+1] {
			t.Fatalf("sidecar ordering paths=%v changes=%+v", paths, cap.ops[0].Leaves())
		}
		var meta map[string]any
		if err := json.Unmarshal(cap.ops[0].Leaves()[i].Write.Bytes, &meta); err != nil {
			t.Fatal(err)
		}
		wantName := []string{"one.md", "two.md"}[i/2]
		if meta["identity"] != "alice" || meta["original_filename"] != wantName || meta["mime"] != "text/markdown" || meta["custom"] != "kept" || meta["received_at"] != inboxTestNow.Format(time.RFC3339) {
			t.Fatalf("metadata=%v", meta)
		}
	}
}

func TestMultipartAggregateCopiesDoNotExceedRawBody(t *testing.T) {
	body, ct := multipartInboxBody(t, "one.md", "two.md")
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatal(err)
	}
	uploads, _, copied, err := parseMultipartUploads(body, params["boundary"])
	if err != nil || copied > len(body) || copied != len(uploads[0].data)+len(uploads[1].data) {
		t.Fatalf("copied=%d body=%d uploads=%d err=%v", copied, len(body), len(uploads), err)
	}
}

type failSecondWriteFS struct {
	wlRecordingFS
	writes int
}

func (f *failSecondWriteFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	f.writes++
	if f.writes == 2 {
		return "", errors.New("second failed")
	}
	return f.wlRecordingFS.WriteFileAtomic(name, data, opts)
}

func TestInboxHTTPPartialBatchReturnsCommittedPaths(t *testing.T) {
	f := newInboxHTTPFixture(t)
	if err := f.s.writeLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fs := &failSecondWriteFS{}
	f.s.writeLog = newWriteLog(fs, nil, nil, 1)
	body, ct := multipartInboxBody(t, "one.md", "two.md")
	// Remove metadata so exactly the two file leaves are committed.
	var plain bytes.Buffer
	w := multipart.NewWriter(&plain)
	for _, name := range []string{"one.md", "two.md"} {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
		h.Set("Content-Type", "text/markdown")
		part, _ := w.CreatePart(h)
		_, _ = io.WriteString(part, name)
	}
	_ = w.Close()
	body, ct = plain.Bytes(), w.FormDataContentType()
	rec := f.request(http.MethodPost, "/inbox/docs", ct, body, f.tokens["alice"])
	var response struct {
		Committed []string `json:"committed_paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != 500 || len(response.Committed) != 1 || len(fs.order()) != 1 || response.Committed[0] != fs.order()[0] {
		t.Fatalf("status=%d response=%+v applied=%v err=%v", rec.Code, response, fs.order(), err)
	}
}

func TestInboxHTTPPendingChangeReturnsAcceptedRef(t *testing.T) {
	f := newInboxHTTPFixture(t)
	f.s.RegisterPlugin(inboxPendingMiddleware{})
	rec := f.request("POST", "/inbox/docs?name=x.md", "text/markdown", []byte("x"), f.tokens["alice"])
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"ref":"pending-42"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type inboxPendingMiddleware struct{}

func (inboxPendingMiddleware) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(WriteHandler) WriteHandler {
		return func(_ context.Context, op WriteOp) (WriteResult, error) {
			return WriteResult{}, op.Pending("pending-42")
		}
	}}
}

func TestInboxManagementOAuthAndOwnership(t *testing.T) {
	f := newInboxHTTPFixture(t)
	f.s.config.AllowKeyless = true
	f.s.config.Tokens = &config.AuthTokensConfig{Issuer: "https://openlore.test", Audience: "https://openlore.test"}
	if err := f.s.initAuth(); err != nil {
		t.Fatal(err)
	}
	if got := f.request("GET", "/inbox/tokens", "", nil, InboxToken{}).Code; got != 401 {
		t.Fatalf("keyless management status=%d", got)
	}
	oauth := mint(t, f.s, "alice", ScopeFull)
	do := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		return rec
	}
	created := do("POST", "/inbox/tokens", oauth, `{"label":"owned"}`)
	if created.Code != 201 || created.Header().Get("Cache-Control") != "no-store" || !strings.Contains(created.Body.String(), `"identity":"alice"`) {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	var made map[string]any
	_ = json.Unmarshal(created.Body.Bytes(), &made)
	listed := do("GET", "/inbox/tokens", oauth, "")
	if listed.Code != 200 || strings.Contains(listed.Body.String(), "bob") || strings.Contains(listed.Body.String(), `"secret"`) || strings.Contains(listed.Body.String(), "olin_") {
		t.Fatalf("unsafe/unfiltered list=%s", listed.Body.String())
	}
	if got := do("DELETE", "/inbox/tokens/"+f.tokens["bob"].ID, oauth, "").Code; got != 404 {
		t.Fatalf("other revoke status=%d", got)
	}
	if got := do("DELETE", "/inbox/tokens/"+made["id"].(string), oauth, "").Code; got != 204 {
		t.Fatalf("own revoke status=%d", got)
	}
	readOAuth := mint(t, f.s, "alice", ScopeRead)
	if got := do("POST", "/inbox/tokens", readOAuth, `{"label":"escalation"}`).Code; got != 403 {
		t.Fatalf("read-scoped token management status=%d", got)
	}
}

func TestInboxHTTPPrepareRoutesStoreInitializationError(t *testing.T) {
	f := newInboxHTTPFixture(t)
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.s.config.DataDir = file
	if _, err := f.p.PrepareHTTPRoutes(f.s); err == nil || !strings.Contains(err.Error(), "initializing inbox token store") {
		t.Fatalf("error=%v", err)
	}
}
