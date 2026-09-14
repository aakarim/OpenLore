package openlore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type CommitRecord struct {
	ID          string        `json:"id,omitempty"`
	Time        time.Time     `json:"time"`
	Attribution Attribution   `json:"attribution"`
	ChangeSet   vfs.ChangeSet `json:"change_set"`
	Hash        string        `json:"hash,omitempty"`
	Leaves      []LeafRecord  `json:"leaves,omitempty"`
}
type LeafRecord struct {
	Target        string           `json:"target"`
	Action        vfs.ChangeAction `json:"action"`
	BeforeExists  bool             `json:"before_exists,omitempty"`
	BeforeHash    string           `json:"before_hash,omitempty"`
	BeforeSize    int64            `json:"before_size,omitempty"`
	AfterHash     string           `json:"after_hash,omitempty"`
	AfterSize     int64            `json:"after_size,omitempty"`
	BeforeUnknown bool             `json:"before_unknown,omitempty"`
}
type BlobStats struct{ Objects, Bytes int64 }
type BlobStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string) (io.ReadCloser, int64, error)
	Has(context.Context, string) (bool, error)
	Stats(context.Context) (BlobStats, error)
}
type fileBlobStore struct{ dir string }

func OpenBlobStore(dir string) (BlobStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileBlobStore{dir}, nil
}
func (s *fileBlobStore) object(hash string) (string, error) {
	if len(hash) != 64 {
		return "", fmt.Errorf("invalid sha256 %q", hash)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, hash[:2], hash), nil
}
func (s *fileBlobStore) Put(ctx context.Context, hash string, r io.Reader) error {
	p, err := s.object(hash)
	if err != nil {
		return err
	}
	if ok, _ := s.Has(ctx, hash); ok {
		return nil
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != hash {
		return errors.New("history blob hash mismatch")
	}
	if err = os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err = os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
func (s *fileBlobStore) Get(_ context.Context, hash string) (io.ReadCloser, int64, error) {
	p, err := s.object(hash)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}
func (s *fileBlobStore) Has(_ context.Context, hash string) (bool, error) {
	p, err := s.object(hash)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
func (s *fileBlobStore) Stats(ctx context.Context) (BlobStats, error) {
	var out BlobStats
	err := filepath.WalkDir(s.dir, func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			info, err := e.Info()
			if err != nil {
				return err
			}
			out.Objects++
			out.Bytes += info.Size()
		}
		return nil
	})
	return out, err
}

func hashContent(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func capturePreImages(ctx context.Context, fsys vfs.FileSystem, blobs BlobStore, cs vfs.ChangeSet, enabled bool) ([]LeafRecord, error) {
	var out []LeafRecord
	var capture func(vfs.Change) error
	capture = func(leaf vfs.Change) error {
		info, err := fsys.Stat(leaf.Target)
		if err == nil && info.Dir && (leaf.Action == vfs.ChangeActionRemove || leaf.Action == vfs.ChangeActionRemoveAll) {
			entries, e := fsys.ReadDir(leaf.Target)
			if e != nil {
				return e
			}
			for _, entry := range entries {
				child := leaf
				child.Target = path.Join(leaf.Target, entry.FileName)
				if err := capture(child); err != nil {
					return err
				}
			}
			return nil
		}
		record := LeafRecord{Target: vfs.CleanPath(leaf.Target), Action: leaf.Action}
		if err == nil && !info.Dir {
			record.BeforeExists = true
			b, e := fsys.ReadFile(leaf.Target)
			if e != nil {
				record.BeforeUnknown = true
			} else {
				record.BeforeHash = hashContent(b)
				record.BeforeSize = int64(len(b))
				if enabled && blobs != nil {
					if e = blobs.Put(ctx, record.BeforeHash, strings.NewReader(string(b))); e != nil {
						record.BeforeUnknown = true
					}
				}
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			record.BeforeUnknown = true
		}
		out = append(out, record)
		return nil
	}
	for _, leaf := range cs.Leaves() {
		if err := capture(leaf); err != nil {
			return out, err
		}
	}
	return out, nil
}
func fillAfter(leaves []LeafRecord, committed vfs.ChangeSet) []LeafRecord {
	byPath := map[string]vfs.Change{}
	for _, leaf := range committed.Leaves() {
		byPath[vfs.CleanPath(leaf.Target)] = leaf
	}
	for i := range leaves {
		if leaf, ok := byPath[leaves[i].Target]; ok && leaf.Write != nil {
			leaves[i].AfterHash = hashContent(leaf.Write.Bytes)
			leaves[i].AfterSize = int64(len(leaf.Write.Bytes))
		}
	}
	return leaves
}
func appendCommitRecord(file string, record CommitRecord) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(record); err != nil {
		return err
	}
	return f.Sync()
}

type HistoryPosition struct {
	Offset int64  `json:"offset"`
	LastID string `json:"last_id"`
}
type HistoryCursor interface {
	Next(context.Context) (CommitRecord, bool, error)
	Seek(context.Context, string) error
	Position() HistoryPosition
}
type historyCursorRestorer interface {
	RestorePosition(HistoryPosition) error
}
type fileHistoryCursor struct {
	mu       sync.Mutex
	file     *os.File
	reader   *bufio.Reader
	position HistoryPosition
}

func OpenHistoryCursor(file string, from HistoryPosition) (HistoryCursor, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Seek(from.Offset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &fileHistoryCursor{file: f, reader: bufio.NewReader(f), position: from}, nil
}
func (c *fileHistoryCursor) Next(ctx context.Context) (CommitRecord, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CommitRecord{}, false, err
	}
	line, err := c.reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return CommitRecord{}, false, nil
	}
	if errors.Is(err, io.EOF) {
		// An append-only journal may be observed between the record write and
		// its terminating newline. Do not consume that record until complete.
		if _, seekErr := c.file.Seek(c.position.Offset, io.SeekStart); seekErr != nil {
			return CommitRecord{}, false, seekErr
		}
		c.reader.Reset(c.file)
		return CommitRecord{}, false, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return CommitRecord{}, false, err
	}
	var r CommitRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return CommitRecord{}, false, err
	}
	c.position.Offset += int64(len(line))
	c.position.LastID = r.ID
	return r, true, nil
}
func (c *fileHistoryCursor) Seek(ctx context.Context, id string) error {
	for {
		r, ok, err := c.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return io.EOF
		}
		if r.ID == id {
			return nil
		}
	}
}
func (c *fileHistoryCursor) Position() HistoryPosition {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.position
}
func (c *fileHistoryCursor) RestorePosition(position HistoryPosition) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.file.Seek(position.Offset, io.SeekStart); err != nil {
		return err
	}
	c.reader.Reset(c.file)
	c.position = position
	return nil
}
