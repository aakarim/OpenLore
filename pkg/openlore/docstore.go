package openlore

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Docstore uses the history blob store; manifests are the authoritative roots
// for blob retention. Pending and committed manifests survive process restarts.
type Docstore struct {
	dir   string
	blobs BlobStore
}

var docstoreMu sync.Mutex // also serializes history GC with pending manifests

type SourceFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}
type SnapshotRequest struct {
	Files  map[string]SourceFile `json:"files"`
	Parent string                `json:"parent,omitempty"`
	Meta   map[string]string     `json:"meta,omitempty"`
}
type SourceSnapshot struct {
	SnapshotRequest
	Snapshot string       `json:"snapshot"`
	Time     time.Time    `json:"time"`
	Deleted  int          `json:"deleted"`
	Record   CommitRecord `json:"record"`
}
type sourceState struct {
	Store         string                       `json:"store"`
	Owner         string                       `json:"owner"`
	Snapshots     []SourceSnapshot             `json:"snapshots"`
	Pending       map[string]SourceSnapshot    `json:"pending"`
	Installations map[string]skillInstallation `json:"skill_installations,omitempty"`
}

func OpenDocstore(dataDir string) (*Docstore, error) {
	b, err := OpenBlobStore(filepath.Join(dataDir, "history", "objects"))
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(dataDir, "docstore")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Docstore{dir: dir, blobs: b}, nil
}

// validSourcePath accepts a store path. The final segment may not be the
// snapshots route suffix, which would make the store's URLs ambiguous.
func validSourcePath(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.ContainsAny(p, "\\\x00") && path.Base(p) != config.ReservedStoreSegment
}
func (d *Docstore) state(store string) (sourceState, error) {
	s := sourceState{Store: store, Pending: map[string]SourceSnapshot{}}
	b, err := os.ReadFile(filepath.Join(d.dir, hashContent([]byte(store))+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func (d *Docstore) save(s sourceState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d.dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(d.dir, hashContent([]byte(s.Store))+".json")); err != nil {
		return err
	}
	dir, err := os.Open(d.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (d *Docstore) states() ([]sourceState, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	out := []sourceState{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var s sourceState
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Store < out[j].Store })
	return out, nil
}
func validateSnapshot(req SnapshotRequest) error {
	if req.Files == nil || len(req.Files) > 10000 {
		return fmt.Errorf("files required (maximum 10000)")
	}
	var size int64
	for p, f := range req.Files {
		decoded, err := hex.DecodeString(f.SHA256)
		if !validSourcePath(p) || len(decoded) != 32 || err != nil || f.SHA256 != strings.ToLower(f.SHA256) || f.Size < 0 || f.Size > maxSFTPStagedFileBytes || f.Mode & ^uint32(0777) != 0 {
			return fmt.Errorf("invalid file %q (8 MiB maximum; regular files only)", p)
		}
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if _, ok := req.Files[parent]; ok {
				return fmt.Errorf("file/directory collision: %s", parent)
			}
		}
		size += f.Size
	}
	if size > 128<<20 {
		return fmt.Errorf("snapshot exceeds 128 MiB")
	}
	return nil
}
func latestSnapshot(s sourceState) SourceSnapshot {
	if len(s.Snapshots) == 0 {
		return SourceSnapshot{}
	}
	return s.Snapshots[len(s.Snapshots)-1]
}

func (d *Docstore) prepare(ctx context.Context, store, owner string, req SnapshotRequest, dry bool) (SourceSnapshot, []string, error) {
	if err := validateSnapshot(req); err != nil {
		return SourceSnapshot{}, nil, err
	}
	s, err := d.state(store)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	if s.Owner != "" && s.Owner != owner {
		return SourceSnapshot{}, nil, os.ErrPermission
	}
	b, _ := json.Marshal(struct {
		Store, Owner string
		Request      SnapshotRequest
	}{store, owner, req})
	id := hashContent(b)
	for _, old := range s.Snapshots {
		if old.Snapshot == id {
			return old, []string{}, nil
		}
	}
	if latestSnapshot(s).Snapshot != req.Parent {
		return SourceSnapshot{}, nil, fmt.Errorf("parent conflict: refresh latest snapshot before retrying")
	}
	missing := []string{}
	seen := map[string]bool{}
	for _, f := range req.Files {
		ok, err := d.blobs.Has(ctx, f.SHA256)
		if err != nil {
			return SourceSnapshot{}, nil, err
		}
		if !ok && !seen[f.SHA256] {
			missing = append(missing, f.SHA256)
			seen[f.SHA256] = true
		}
	}
	sort.Strings(missing)
	snap := SourceSnapshot{SnapshotRequest: req, Snapshot: id}
	if dry {
		return snap, missing, nil
	}
	if len(s.Pending) >= 16 {
		if _, ok := s.Pending[id]; !ok {
			return snap, nil, fmt.Errorf("too many pending snapshots")
		}
	}
	s.Owner = owner
	s.Pending[id] = snap
	return snap, missing, d.save(s)
}
func (d *Docstore) commit(ctx context.Context, store, owner, id string) (SourceSnapshot, error) {
	s, err := d.state(store)
	if err != nil {
		return SourceSnapshot{}, err
	}
	if s.Owner != owner {
		return SourceSnapshot{}, os.ErrPermission
	}
	for _, snap := range s.Snapshots {
		if snap.Snapshot == id {
			return snap, nil
		}
	}
	snap, ok := s.Pending[id]
	if !ok {
		return snap, os.ErrNotExist
	}
	previous := latestSnapshot(s)
	if snap.Parent != previous.Snapshot {
		return snap, fmt.Errorf("parent conflict: snapshot has advanced")
	}
	for _, f := range snap.Files {
		r, size, err := d.blobs.Get(ctx, f.SHA256)
		if err != nil {
			return snap, fmt.Errorf("missing blob %s", f.SHA256)
		}
		r.Close()
		if size != f.Size {
			return snap, fmt.Errorf("blob size mismatch for %s", f.SHA256)
		}
	}
	snap.Time = time.Now().UTC()
	snap.Record = CommitRecord{ID: id, Time: snap.Time, Attribution: Attribution{Principal: owner}}
	for p, before := range previous.Files {
		after, exists := snap.Files[p]
		if !exists {
			snap.Deleted++
		}
		if !exists || after != before {
			leaf := LeafRecord{Target: store + "/" + p, Action: vfs.ChangeActionWrite, BeforeExists: true, BeforeHash: before.SHA256, BeforeSize: before.Size}
			if exists {
				leaf.AfterHash = after.SHA256
				leaf.AfterSize = after.Size
			} else {
				leaf.Action = vfs.ChangeActionRemove
			}
			snap.Record.Leaves = append(snap.Record.Leaves, leaf)
		}
	}
	for p, f := range snap.Files {
		if _, exists := previous.Files[p]; !exists {
			snap.Record.Leaves = append(snap.Record.Leaves, LeafRecord{Target: store + "/" + p, Action: vfs.ChangeActionWrite, AfterHash: f.SHA256, AfterSize: f.Size})
		}
	}
	sort.Slice(snap.Record.Leaves, func(i, j int) bool { return snap.Record.Leaves[i].Target < snap.Record.Leaves[j].Target })
	s.Snapshots = append(s.Snapshots, snap)
	// Every outstanding prepare names the previous parent, so none can commit
	// after this publication. Retaining them would eventually exhaust the cap.
	s.Pending = map[string]SourceSnapshot{}
	return snap, d.save(s)
}

// docstoreHandler is mounted separately so authentication is mandatory even on
// servers whose shell API deliberately permits anonymous access.
func (s *Server) docstoreHandler(prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.identityFromContext(r.Context())
		owner := id.IdentityName
		if owner == "" || owner == "anonymous" {
			writeAPIError(w, 403, "named identity required")
			return
		}
		p := strings.TrimPrefix(r.URL.Path, prefix)
		if p == "identity" && r.Method == "GET" {
			writeJSON(w, 200, map[string]string{"identity": owner})
			return
		}
		d := s.docstore
		if strings.HasPrefix(p, "blobs/") && r.Method == "PUT" {
			if !scopeGrantsWrite(id.Scopes) {
				writeAPIError(w, 403, "full scope required")
				return
			}
			hash := strings.TrimPrefix(p, "blobs/")
			// Receive the upload before taking the store lock so a slow client
			// cannot stall every other docstore request and projected read.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSFTPStagedFileBytes))
			if err == nil && hashContent(body) != hash {
				err = fmt.Errorf("blob hash mismatch")
			}
			if err != nil {
				writeAPIError(w, 400, err.Error())
				return
			}
			docstoreMu.Lock()
			defer docstoreMu.Unlock()
			states, err := d.states()
			if err != nil {
				writeAPIError(w, 500, err.Error())
				return
			}
			allowed := false
			for _, state := range states {
				if state.Owner == owner {
					for _, snap := range state.Pending {
						for _, f := range snap.Files {
							if f.SHA256 == hash {
								allowed = true
							}
						}
					}
				}
			}
			if !allowed {
				writeAPIError(w, 403, "blob not requested by an owned pending snapshot")
				return
			}
			if err := d.blobs.Put(r.Context(), hash, bytes.NewReader(body)); err != nil {
				writeAPIError(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		docstoreMu.Lock()
		defer docstoreMu.Unlock()
		store, tail, snapshots := p, "", false
		if i := strings.LastIndex(p, "/snapshots"); i >= 0 {
			candidate, rest := p[:i], p[i+len("/snapshots"):]
			minParts := 3
			if strings.HasPrefix(candidate, "openlore/") {
				minParts = 2
			}
			if len(strings.Split(candidate, "/")) >= minParts && (rest == "" || strings.HasPrefix(rest, "/") && strings.HasSuffix(rest, "/commit")) {
				store, tail, snapshots = candidate, rest, true
			}
		}
		store = strings.TrimSuffix(store, "/")
		parts := strings.Split(store, "/")
		if !validSourcePath(store) && store != "" {
			writeAPIError(w, 400, "invalid store path")
			return
		}
		if r.Method == "GET" {
			states, err := d.states()
			if err != nil {
				writeAPIError(w, 500, err.Error())
				return
			}
			out := []sourceView{}
			for _, st := range states {
				view, selected, visible := s.visibleSource(id, st)
				if !visible {
					continue
				}
				if st.Store == store {
					if file := r.URL.Query().Get("file"); file != "" && !snapshots {
						f, ok := view.Latest.Files[file]
						if !ok {
							writeAPIError(w, 404, "file not found")
							return
						}
						reader, _, err := d.blobs.Get(r.Context(), f.SHA256)
						if err != nil {
							writeAPIError(w, 500, err.Error())
							return
						}
						defer reader.Close()
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						w.Header().Set("X-Content-Type-Options", "nosniff")
						io.Copy(w, reader)
						return
					}
					if snapshots {
						history := append([]SourceSnapshot{}, st.Snapshots...)
						if selected != nil {
							for i := range history {
								history[i] = filterSourceSnapshot(history[i], selected)
							}
						}
						writeJSON(w, 200, history)
						return
					}
					writeJSON(w, 200, view)
					return
				}
				if store == "" || strings.HasPrefix(st.Store, store+"/") {
					out = append(out, view)
				}
			}
			writeJSON(w, 200, out)
			return
		}
		if r.Method != "POST" {
			writeAPIError(w, 405, "method not allowed")
			return
		}
		if !scopeGrantsWrite(id.Scopes) {
			writeAPIError(w, 403, "full scope required")
			return
		}
		if len(parts) < 3 || (parts[0] != "folder" && parts[0] != "claude" && parts[0] != "git") || (parts[0] != "git" && parts[1] != owner) || (parts[0] == "git" && len(parts) < 4) {
			writeAPIError(w, 403, "store must be folder/<identity>/<name>, claude/<identity>/<project>/…, or git/<host>/<org>/<repo>")
			return
		}
		if tail == "" && snapshots {
			var req SnapshotRequest
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				writeAPIError(w, 400, err.Error())
				return
			}
			snap, missing, err := d.prepare(r.Context(), store, owner, req, r.URL.Query().Get("dry_run") == "true")
			if err != nil {
				writeAPIError(w, 409, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"snapshot": snap.Snapshot, "missing": missing})
			return
		}
		if strings.HasSuffix(tail, "/commit") {
			snap, err := d.commit(r.Context(), store, owner, strings.TrimSuffix(strings.TrimPrefix(tail, "/"), "/commit"))
			if err != nil {
				writeAPIError(w, 409, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"snapshot": snap.Snapshot, "time": snap.Time, "files": len(snap.Files), "deleted": snap.Deleted})
			return
		}
		writeAPIError(w, 404, "not found")
	})
}
