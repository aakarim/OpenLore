package openlore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

const defaultHistoryPageSize = 100

// HistoryRecord is the storage-neutral metadata for one committed filesystem
// change. It deliberately excludes mutation payloads: current and historical
// file contents belong in the filesystem or a separate content-addressed
// store, not in the query index.
type HistoryRecord struct {
	Time        time.Time   `json:"time"`
	Attribution Attribution `json:"attribution"`
	FileKey     string      `json:"file_key"`
	Action      string      `json:"action"`
	ContentHash string      `json:"content_hash,omitempty"`
}

// HistoryQuery describes a bounded, newest-first metadata query. Cursor is
// opaque to callers so another HistoryStore implementation can use database
// keys rather than the JSONL offset used by the local store.
type HistoryQuery struct {
	FileKey   string
	Roots     []string
	Principal string
	Actor     string
	Limit     int
	Cursor    string
}

type HistoryPage struct {
	Records    []HistoryRecord
	NextCursor string
}

// HistoryRecorder updates the rebuildable history index after a filesystem
// commit. Ordinary mutations append metadata; remove and remove_all mutations
// purge the affected per-file shards instead of retaining deleted-file history.
type HistoryRecorder interface {
	Record(context.Context, []HistoryRecord) error
}

// HistoryStore is the persistence boundary used by history consumers. The
// server currently supplies a sharded JSONL implementation; a database-backed
// implementation can replace it without changing the shell or web browser.
type HistoryStore interface {
	HistoryRecorder
	Query(context.Context, HistoryQuery) (HistoryPage, error)
}

// JSONLHistoryStore keeps a compact global metadata journal and a per-file
// journal selected by the SHA-256 of the canonical file key. File history
// queries therefore never scan unrelated history.
type JSONLHistoryStore struct {
	dir string
	mu  sync.Mutex
}

func NewJSONLHistoryStore(dir string) *JSONLHistoryStore {
	return &JSONLHistoryStore{dir: dir}
}

func (s *JSONLHistoryStore) Record(_ context.Context, records []HistoryRecord) error {
	if len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := make([]HistoryRecord, len(records))
	copy(normalized, records)
	for i := range normalized {
		normalized[i].FileKey = vfs.CleanPath(normalized[i].FileKey)
	}

	if err := appendHistoryRecords(filepath.Join(s.dir, "events.jsonl"), normalized); err != nil {
		return err
	}
	pending := make(map[string][]HistoryRecord)
	flush := func() error {
		for fileKey, fileRecords := range pending {
			if err := appendHistoryRecords(s.filePath(fileKey), fileRecords); err != nil {
				return err
			}
		}
		clear(pending)
		return nil
	}
	for _, record := range normalized {
		switch vfs.ChangeAction(record.Action) {
		case vfs.ChangeActionRemove:
			if err := flush(); err != nil {
				return err
			}
			if err := removeHistoryShard(s.filePath(record.FileKey)); err != nil {
				return err
			}
		case vfs.ChangeActionRemoveAll:
			if err := flush(); err != nil {
				return err
			}
			if err := s.removeHistoryTree(record.FileKey); err != nil {
				return err
			}
		default:
			pending[record.FileKey] = append(pending[record.FileKey], record)
		}
	}
	return flush()
}

func removeHistoryShard(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *JSONLHistoryStore) removeHistoryTree(root string) error {
	f, err := os.Open(filepath.Join(s.dir, "events.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	fileKeys := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record HistoryRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return err
		}
		if pathWithinRoot(root, record.FileKey) {
			fileKeys[record.FileKey] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for fileKey := range fileKeys {
		if err := removeHistoryShard(s.filePath(fileKey)); err != nil {
			return err
		}
	}
	return nil
}

func appendHistoryRecords(path string, records []HistoryRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return f.Sync()
}

func (s *JSONLHistoryStore) Query(_ context.Context, query HistoryQuery) (HistoryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if query.FileKey != "" && !historyReadable(query.Roots, query.FileKey) {
		return HistoryPage{}, nil
	}
	path := filepath.Join(s.dir, "events.jsonl")
	if query.FileKey != "" {
		path = s.filePath(query.FileKey)
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return HistoryPage{}, nil
	}
	if err != nil {
		return HistoryPage{}, err
	}
	defer f.Close()

	var matches []HistoryRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record HistoryRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return HistoryPage{}, err
		}
		if query.FileKey != "" && record.FileKey != vfs.CleanPath(query.FileKey) {
			continue
		}
		if !historyReadable(query.Roots, record.FileKey) || query.Principal != "" && record.Attribution.Principal != query.Principal || query.Actor != "" && record.Attribution.Actor != query.Actor {
			continue
		}
		matches = append(matches, record)
	}
	if err := scanner.Err(); err != nil {
		return HistoryPage{}, err
	}

	offset := 0
	if query.Cursor != "" {
		offset, err = strconv.Atoi(query.Cursor)
		if err != nil || offset < 0 {
			return HistoryPage{}, fmt.Errorf("invalid history cursor")
		}
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultHistoryPageSize
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Time.After(matches[j].Time) })
	if offset >= len(matches) {
		return HistoryPage{}, nil
	}
	end := min(offset+limit, len(matches))
	page := HistoryPage{Records: matches[offset:end]}
	if end < len(matches) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func (s *JSONLHistoryStore) filePath(fileKey string) string {
	sum := sha256.Sum256([]byte(vfs.CleanPath(fileKey)))
	name := hex.EncodeToString(sum[:])
	return filepath.Join(s.dir, "files", name[:2], name+".jsonl")
}

func historyRoots(docsets []cmds.DocsetInfo) []string {
	var roots []string
	for _, docset := range docsets {
		roots = append(roots, docset.Paths...)
	}
	return roots
}

func historyReadable(roots []string, target string) bool {
	for _, root := range roots {
		if pathWithinRoot(root, target) {
			return true
		}
	}
	return false
}

type scopedHistory struct {
	store HistoryStore
	roots []string
}

func (h scopedHistory) Query(principal, actor string) ([]byte, error) {
	page, err := h.store.Query(context.Background(), HistoryQuery{Roots: h.roots, Principal: principal, Actor: actor})
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, record := range page.Records {
		line, _ := json.Marshal(struct {
			Time        string `json:"time"`
			Attribution string `json:"attribution"`
			Target      string `json:"target"`
			Action      string `json:"action"`
			Hash        string `json:"hash,omitempty"`
		}{record.Time.Format(time.RFC3339), record.Attribution.String(), record.FileKey, record.Action, record.ContentHash})
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out, nil
}
