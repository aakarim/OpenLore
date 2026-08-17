package openlore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// InboxPlugin contributes the "publish" grant: an identity holding it may read
// the whole docset, may never delete anything, and may only create or edit files
// within the docset's configured inbox folder. It is registered like any other
// plugin (Server.RegisterPlugin) and exposes its grant via GrantTypeProvider.
//
// The inbox model lets an outside collaborator drop material into a docset (an
// "inbox") without granting them write access to the rest of the docset and
// without any ability to delete.
type InboxPlugin struct {
	now         func() time.Time
	replayMu    sync.Mutex
	replays     map[string]map[string]time.Time
	recipientsM sync.RWMutex
	recipients  map[string]struct{}
}

const inboxReplayPerToken = 1000
const inboxReplayGlobal = 10000

// NewInboxPlugin returns the inbox plugin.
func NewInboxPlugin() *InboxPlugin {
	return &InboxPlugin{now: time.Now, replays: make(map[string]map[string]time.Time)}
}

// GrantTypes implements GrantTypeProvider.
func (*InboxPlugin) GrantTypes() []GrantType { return []GrantType{publishGrant{}} }

// Info implements PluginInfoProvider.
func (*InboxPlugin) Info() PluginInfo { return PluginInfo{Name: "inbox", Version: "1.0.0"} }

var mentionPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9._%+\-])@([a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?)`)

// WriteMiddleware expands collaboration messages into inbox notification
// writes before the ordered log sees them. The source and notifications are
// therefore one ordered ChangeSet rather than best-effort post-commit work.
// Only channel posts and thread replies are messages; documentation containing
// an @handle must never produce notifications.
func (p *InboxPlugin) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			leaves := op.Leaves()
			changes := make([]vfs.Change, 0, len(leaves))
			changed := false
			for _, leaf := range leaves {
				changes = append(changes, leaf)
				if leaf.Action != vfs.ChangeActionWrite || leaf.Write == nil || !isMentionSource(leaf.Target) {
					continue
				}
				for _, recipient := range p.mentionedRecipients(string(leaf.Write.Bytes), op.Actor.ID) {
					hash := sha256.Sum256([]byte(leaf.Target + "\x00" + recipient))
					filename := fmt.Sprintf("%x-%s", hash[:8], path.Base(leaf.Target))
					target := vfs.CleanPath("/inboxes/" + recipient + "/" + filename)
					body := fmt.Sprintf("---\nfrom: %s\nsource: %s\n---\nMentioned by @%s in `%s`.\n", op.Actor.ID, leaf.Target, op.Actor.ID, leaf.Target)
					changes = append(changes, vfs.Change{
						Target: target,
						Action: vfs.ChangeActionWrite,
						Write:  &vfs.WriteChange{Bytes: []byte(body), Opts: vfs.WriteOpts{ContentPolicyValidated: true}},
					})
					changed = true
				}
			}
			if !changed {
				return next(ctx, op)
			}
			return next(ctx, NewWriteOp(op.Actor, vfs.ChangeSet{Changes: changes}))
		}
	}}
}

func isMentionSource(target string) bool {
	clean := vfs.CleanPath(target)
	parts := strings.Split(strings.Trim(clean, "/"), "/")
	if len(parts) >= 5 && parts[0] == "channels" && parts[2] == "posts" && strings.HasSuffix(parts[len(parts)-1], ".md") {
		return true
	}
	return len(parts) >= 5 && parts[0] == "threads" && parts[3] == "replies" && strings.HasSuffix(parts[len(parts)-1], ".md")
}

func (p *InboxPlugin) mentionedRecipients(body, author string) []string {
	p.recipientsM.RLock()
	defer p.recipientsM.RUnlock()
	var out []string
	seen := map[string]struct{}{}
	for _, match := range mentionPattern.FindAllStringSubmatch(body, -1) {
		name := match[1]
		if name == author {
			continue
		}
		if _, ok := p.recipients[name]; !ok {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
		if len(out) == 32 {
			break
		}
	}
	return out
}

var (
	_ GrantTypeProvider       = (*InboxPlugin)(nil)
	_ PluginInfoProvider      = (*InboxPlugin)(nil)
	_ WriteMiddlewareProvider = (*InboxPlugin)(nil)
)

// publishGrant is the "publish" grant type: read anywhere in the docset, no
// deletes, create/edit only within the docset's inbox.
type publishGrant struct{}

func (publishGrant) Name() string { return "publish" }

// CanRead permits reading the whole docset.
func (publishGrant) CanRead(config.DocsetSpec, string) bool { return true }

// AllowsWrite is true: publish grants write inside the inbox.
func (publishGrant) AllowsWrite() bool { return true }

// CanWrite permits create/edit (write, mkdir) only within the docset's inbox,
// and denies every removal everywhere.
func (publishGrant) CanWrite(ds config.DocsetSpec, action vfs.ChangeAction, p string) bool {
	switch action {
	case vfs.ChangeActionRemove, vfs.ChangeActionRemoveAll:
		return false // publish can never delete
	}
	inbox := inboxPath(ds)
	if inbox == "" {
		return false // no inbox configured: publish can write nothing
	}
	clean := vfs.CleanPath(p)
	return clean == inbox || strings.HasPrefix(clean, inbox+"/")
}

// inboxPath resolves a docset's inbox to a cleaned VFS display path. An absolute
// Inbox is used verbatim; a relative Inbox is joined onto the docset's first
// display root. Returns "" when the docset has no inbox or no paths.
func inboxPath(ds config.DocsetSpec) string {
	if ds.Inbox == "" {
		return ""
	}
	if strings.HasPrefix(ds.Inbox, "/") {
		return vfs.CleanPath(ds.Inbox)
	}
	if len(ds.Paths) == 0 {
		return ""
	}
	root := ds.Paths[0].Display
	if root == "" {
		root = ds.Paths[0].Source
	}
	return vfs.CleanPath(root + "/" + ds.Inbox)
}

func (p *InboxPlugin) PrepareHTTPRoutes(s *Server) (HTTPRouteRegistrar, error) {
	p.recipientsM.Lock()
	p.recipients = make(map[string]struct{}, len(s.auth.Identities))
	for _, identity := range s.auth.Identities {
		p.recipients[identity.Name] = struct{}{}
	}
	p.recipientsM.Unlock()
	store, err := NewInboxTokenStore(s.config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initializing inbox token store: %w", err)
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /inbox/{docset}", p.uploadHandler(s, store))
		manage := p.managementAuth(s, http.HandlerFunc(p.tokenHandler(s, store)))
		mux.Handle("POST /inbox/tokens", manage)
		mux.Handle("GET /inbox/tokens", manage)
		mux.Handle("DELETE /inbox/tokens/{id}", manage)
	}, nil
}

func (p *InboxPlugin) managementAuth(s *Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.issuer == nil || bearerToken(r) == "" {
			http.Error(w, "OAuth access token required", http.StatusUnauthorized)
			return
		}
		s.authMiddleware(next, true).ServeHTTP(w, r)
	})
}

func writeInboxJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (p *InboxPlugin) tokenHandler(s *Server, store *InboxTokenStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := s.identityFromContext(r.Context())
		if id.IdentityName == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if !scopeGrantsWrite(id.Scopes) {
			http.Error(w, "write scope required", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Cache-Control", "no-store")
			var req struct{ Label, TTL string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, "invalid JSON", 400)
				return
			}
			var expires *time.Time
			if req.TTL != "" {
				d, err := time.ParseDuration(req.TTL)
				if err != nil || d <= 0 {
					http.Error(w, "invalid ttl", 400)
					return
				}
				t := p.now().UTC().Add(d)
				expires = &t
			}
			t, err := store.Create(id.IdentityName, req.Label, expires)
			if err != nil {
				http.Error(w, "token store failure", 500)
				return
			}
			writeInboxJSON(w, 201, map[string]any{"id": t.ID, "token": t.Credential(), "identity": t.Identity, "label": t.Label, "created_at": t.CreatedAt, "expires_at": t.ExpiresAt})
		case http.MethodGet:
			tokens, err := store.List()
			if err != nil {
				http.Error(w, "token store failure", 500)
				return
			}
			owned := make([]InboxToken, 0)
			for _, t := range tokens {
				if t.Identity == id.IdentityName {
					t.Secret = ""
					owned = append(owned, t)
				}
			}
			writeInboxJSON(w, 200, owned)
		case http.MethodDelete:
			t, ok, err := store.Get(r.PathValue("id"))
			if err != nil {
				http.Error(w, "token store failure", 500)
				return
			}
			if !ok || t.Identity != id.IdentityName {
				http.NotFound(w, r)
				return
			}
			_, err = store.Delete(t.ID)
			if err != nil {
				http.Error(w, "token store failure", 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func parseInboxCredential(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "olin_") {
		return "", "", false
	}
	id, secret, ok := strings.Cut(strings.TrimPrefix(value, "olin_"), "_")
	if !ok || id == "" || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

func (p *InboxPlugin) lookupUploadToken(r *http.Request, store *InboxTokenStore) (InboxToken, bool, bool) {
	var tokenID, suppliedSecret string
	isBearer := bearerToken(r) != ""
	if bearer := bearerToken(r); isBearer {
		tokenID, suppliedSecret, _ = parseInboxCredential(bearer)
	} else {
		tokenID = r.Header.Get("X-OpenLore-Token-Id")
	}
	t, ok, err := store.Get(tokenID)
	if err != nil || !ok || (t.ExpiresAt != nil && !p.now().Before(*t.ExpiresAt)) {
		return InboxToken{}, isBearer, false
	}
	if isBearer && !hmac.Equal([]byte(suppliedSecret), []byte(t.Secret)) {
		return InboxToken{}, true, false
	}
	return t, isBearer, true
}

func (p *InboxPlugin) bindUploadIdentity(s *Server, t InboxToken) (Identity, bool) {
	if _, ok := s.findAuthIdentity(t.Identity); !ok {
		return Identity{}, false
	}
	if matchedName, matched := s.matchRule(t.Identity); matched && matchedName != t.Identity {
		return Identity{}, false
	}
	id, ok := s.identityForName(t.Identity)
	return id, ok && id.IdentityName == t.Identity
}

func (p *InboxPlugin) verifyUploadSignature(r *http.Request, body []byte, t InboxToken) bool {
	params := map[string]string{}
	for _, raw := range strings.Split(r.Header.Get("X-OpenLore-Signature"), ",") {
		k, v, found := strings.Cut(strings.TrimSpace(raw), "=")
		if !found || (k != "t" && k != "v1") || strings.TrimSpace(v) == "" {
			return false
		}
		if _, duplicate := params[k]; duplicate {
			return false
		}
		params[k] = strings.TrimSpace(v)
	}
	if len(params) != 2 {
		return false
	}
	ts, err := strconv.ParseInt(params["t"], 10, 64)
	if err != nil {
		return false
	}
	if d := p.now().Sub(time.Unix(ts, 0)); d < -5*time.Minute || d > 5*time.Minute {
		return false
	}
	sig, err := hex.DecodeString(params["v1"])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(t.Secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	key := strconv.FormatInt(ts, 10) + ":" + hex.EncodeToString(sig)
	p.replayMu.Lock()
	now := p.now()
	total := 0
	for tokenID, bucket := range p.replays {
		for k, expiry := range bucket {
			if now.After(expiry) {
				delete(bucket, k)
			}
		}
		if len(bucket) == 0 {
			delete(p.replays, tokenID)
		} else {
			total += len(bucket)
		}
	}
	bucket := p.replays[t.ID]
	if _, replayed := bucket[key]; replayed {
		p.replayMu.Unlock()
		return false
	}
	// The bounded cache is deliberately process-local; deployments with multiple
	// HTTP instances need sticky routing or a shared replay defense upstream.
	if len(bucket) >= inboxReplayPerToken || total >= inboxReplayGlobal {
		p.replayMu.Unlock()
		return false
	}
	if bucket == nil {
		bucket = make(map[string]time.Time)
		p.replays[t.ID] = bucket
	}
	bucket[key] = time.Unix(ts, 0).Add(5 * time.Minute)
	p.replayMu.Unlock()
	return true
}

type inboxUpload struct {
	name, mime string
	data       []byte
}

func parseMultipartUploads(body []byte, boundary string) ([]inboxUpload, map[string]any, int, error) {
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var files []inboxUpload
	var metadata map[string]any
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, 0, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, nil, 0, err
		}
		switch part.FormName() {
		case "file":
			files = append(files, inboxUpload{part.FileName(), part.Header.Get("Content-Type"), data})
		case "metadata":
			if metadata != nil || part.FileName() != "" || json.Unmarshal(data, &metadata) != nil || metadata == nil {
				return nil, nil, 0, errors.New("malformed metadata")
			}
		default:
			return nil, nil, 0, errors.New("unknown multipart part")
		}
	}
	if len(files) == 0 {
		return nil, nil, 0, errors.New("no files")
	}
	copied := 0
	for _, file := range files {
		copied += len(file.data)
	}
	if copied > len(body) {
		return nil, nil, 0, errors.New("multipart aggregate exceeds request body")
	}
	return files, metadata, copied, nil
}

func (p *InboxPlugin) uploadHandler(s *Server, store *InboxTokenStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, bearer, ok := p.lookupUploadToken(r, store)
		if !ok {
			http.Error(w, "invalid inbox credential", http.StatusUnauthorized)
			return
		}
		var id Identity
		if bearer {
			if id, ok = p.bindUploadIdentity(s, token); !ok {
				http.Error(w, "invalid inbox credential", http.StatusUnauthorized)
				return
			}
		}
		ds, ok := s.auth.Docsets[r.PathValue("docset")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.config.Inbox.MaxUploadSize)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if !bearer && !p.verifyUploadSignature(r, body, token) {
			http.Error(w, "invalid inbox credential", 401)
			return
		}
		if !bearer {
			if id, ok = p.bindUploadIdentity(s, token); !ok {
				http.Error(w, "invalid inbox credential", http.StatusUnauthorized)
				return
			}
		}
		base := inboxPath(ds)
		if base == "" {
			http.NotFound(w, r)
			return
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "invalid content type", 400)
			return
		}
		var uploads []inboxUpload
		var metadata map[string]any
		if mediaType == "multipart/form-data" {
			var copied int
			uploads, metadata, copied, err = parseMultipartUploads(body, params["boundary"])
			if copied > len(body) {
				err = errors.New("multipart aggregate exceeds request body")
			}
		} else {
			uploads = []inboxUpload{{r.URL.Query().Get("name"), mediaType, body}}
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		now := p.now().UTC()
		changes := make([]vfs.Change, 0)
		paths := make([]string, 0, len(uploads))
		for _, up := range uploads {
			if up.name == "" || strings.ContainsAny(up.name, "/\\\x00") || filepath.Base(up.name) != up.name || up.name == "." || up.name == ".." || len(up.data) == 0 {
				http.Error(w, "invalid or empty filename", 400)
				return
			}
			ext := strings.ToLower(filepath.Ext(up.name))
			expected, allowed := s.config.Inbox.AllowedTypes[ext]
			actual, _, parseErr := mime.ParseMediaType(up.mime)
			if parseErr != nil || !allowed || !strings.EqualFold(actual, expected) {
				http.Error(w, "disallowed extension/MIME pair", http.StatusUnsupportedMediaType)
				return
			}
			random := make([]byte, 8)
			if _, err := rand.Read(random); err != nil {
				http.Error(w, "randomness unavailable", 500)
				return
			}
			name := now.Format("2006-01-02T150405") + "-" + hex.EncodeToString(random) + "-" + up.name
			path := vfs.CleanPath(base + "/" + name)
			if metadata != nil {
				side := make(map[string]any, len(metadata)+4)
				for k, v := range metadata {
					side[k] = v
				}
				side["identity"] = id.IdentityName
				side["received_at"] = now
				side["original_filename"] = up.name
				side["mime"] = actual
				b, _ := json.MarshalIndent(side, "", "  ")
				sidePath := path + ".json"
				// WritableFS has no transaction primitive. Sidecar-first ordering ensures
				// pollers never observe a file lacking authoritative metadata, but a later
				// failure can leave an orphan sidecar; batches do not promise rollback.
				changes = append(changes, vfs.Change{Target: sidePath, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: append(b, '\n'), Opts: vfs.WriteOpts{IfNoneMatch: true, ContentPolicyValidated: true}}})
				paths = append(paths, sidePath)
			}
			changes = append(changes, vfs.Change{Target: path, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: up.data, Opts: vfs.WriteOpts{IfNoneMatch: true, ContentPolicyValidated: true}}})
			paths = append(paths, path)
		}
		// Multipart parts are the only retained request copies by this point.
		// Release the raw signed body before the commit may block.
		body = nil
		uploads = nil
		if _, err := s.AdmitChangeSet(r.Context(), id, vfs.ChangeSet{Changes: changes}); err != nil {
			var pending *vfs.PendingChangeError
			if errors.As(err, &pending) {
				writeInboxJSON(w, http.StatusAccepted, map[string]any{"ref": pending.Ref})
				return
			}
			if errors.Is(err, vfs.ErrReadOnly) {
				http.Error(w, "forbidden", 403)
			} else {
				http.Error(w, "upload failed", 500)
			}
			return
		}
		writeInboxJSON(w, 201, map[string]any{"paths": paths})
	}
}
