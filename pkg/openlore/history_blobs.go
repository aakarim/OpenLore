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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/klauspost/compress/zstd"
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
type HistoryGCStats struct{ Objects, Bytes int64 }
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
	commitJournalMu.Lock()
	defer commitJournalMu.Unlock()
	return appendCommitRecordUnlocked(file, record)
}

var commitJournalMu sync.Mutex

func appendCommitRecordUnlocked(file string, record CommitRecord) error {
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
	// Segment identifies a rotated segment. Empty retains compatibility with
	// checkpoints made when commits.jsonl was the only journal file.
	Segment string `json:"segment,omitempty"`
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
	mu         sync.Mutex
	path       string
	reader     *bufio.Reader
	closer     io.Closer
	position   HistoryPosition
	sealedTail string
}

func OpenHistoryCursor(file string, from HistoryPosition) (HistoryCursor, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	if f, err := os.OpenFile(file, os.O_CREATE|os.O_RDONLY, 0o600); err != nil {
		return nil, err
	} else {
		f.Close()
	}
	c := &fileHistoryCursor{path: file, position: from}
	if from.Segment == "" && (from.Offset != 0 || from.LastID != "") {
		from.Segment = filepath.Base(file)
		c.position.Segment = from.Segment
	}
	if err := c.openCurrent(); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *fileHistoryCursor) segments() ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(c.path))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && (strings.HasPrefix(n, "commits-") && (strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.zst"))) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		iday, isequence := historySegmentOrder(out[i])
		jday, jsequence := historySegmentOrder(out[j])
		if iday != jday {
			return iday < jday
		}
		return isequence < jsequence
	})
	out = append(out, filepath.Base(c.path))
	return out, nil
}

func historySegmentOrder(name string) (string, int) {
	const prefix = "commits-"
	const dayLength = len("2006-01-02")
	remainder := strings.TrimPrefix(name, prefix)
	if len(remainder) <= dayLength || remainder[dayLength] != '-' {
		return remainder[:min(dayLength, len(remainder))], 0
	}
	sequenceText := strings.SplitN(remainder[dayLength+1:], ".", 2)[0]
	sequence, _ := strconv.Atoi(sequenceText)
	return remainder[:dayLength], sequence
}
func (c *fileHistoryCursor) openCurrent() error {
	if c.closer != nil {
		_ = c.closer.Close()
		c.closer = nil
	}
	segments, err := c.segments()
	if err != nil {
		return err
	}
	c.sealedTail = ""
	if len(segments) > 1 {
		c.sealedTail = segments[len(segments)-2]
	}
	name := c.position.Segment
	if name == "" {
		name = segments[0]
		if name != filepath.Base(c.path) {
			c.position.Segment = name
		}
	}
	f, err := os.Open(filepath.Join(filepath.Dir(c.path), name))
	if err != nil {
		return err
	}
	var r io.Reader = f
	if strings.HasSuffix(name, ".zst") {
		zr, e := zstd.NewReader(f)
		if e != nil {
			f.Close()
			return e
		}
		r = zr
		c.closer = multiCloser{decoderCloser{zr}, f}
	} else {
		c.closer = f
	}
	if c.position.Offset > 0 {
		if _, err = io.CopyN(io.Discard, r, c.position.Offset); err != nil {
			c.closer.Close()
			return err
		}
	}
	c.reader = bufio.NewReader(r)
	return nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	for _, c := range m {
		_ = c.Close()
	}
	return nil
}

type decoderCloser struct{ *zstd.Decoder }

func (d decoderCloser) Close() error { d.Decoder.Close(); return nil }
func (c *fileHistoryCursor) advance() (bool, error) {
	segments, err := c.segments()
	if err != nil {
		return false, err
	}
	for i, n := range segments {
		if n == c.position.Segment && i+1 < len(segments) {
			c.position.Offset = 0
			c.position.Segment = segments[i+1]
			return true, c.openCurrent()
		}
	}
	return false, nil
}
func (c *fileHistoryCursor) Next(ctx context.Context) (CommitRecord, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CommitRecord{}, false, err
	}
	if recovered, err := c.recoverRotation(); err != nil {
		return CommitRecord{}, false, err
	} else if recovered {
		return c.nextLocked(ctx)
	}
	line, err := c.reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		if recovered, e := c.recoverRotation(); e != nil {
			return CommitRecord{}, false, e
		} else if recovered {
			return c.nextLocked(ctx)
		}
		if c.position.Segment != "" && c.position.Segment != filepath.Base(c.path) {
			if advanced, e := c.advance(); e != nil {
				return CommitRecord{}, false, e
			} else if advanced {
				return c.nextLocked(ctx)
			}
		}
		return CommitRecord{}, false, nil
	}
	if errors.Is(err, io.EOF) {
		// An append-only journal may be observed between the record write and
		// its terminating newline. Do not consume that record until complete.
		if seekErr := c.openCurrent(); seekErr != nil {
			return CommitRecord{}, false, seekErr
		}
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
func (c *fileHistoryCursor) nextLocked(ctx context.Context) (CommitRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CommitRecord{}, false, err
	}
	line, err := c.reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 && c.position.Segment != "" && c.position.Segment != filepath.Base(c.path) {
		if advanced, advanceErr := c.advance(); advanceErr != nil {
			return CommitRecord{}, false, advanceErr
		} else if advanced {
			return c.nextLocked(ctx)
		}
	}
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return CommitRecord{}, false, nil
	}
	if err != nil {
		return CommitRecord{}, false, err
	}
	var r CommitRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return r, false, err
	}
	c.position.Offset += int64(len(line))
	c.position.LastID = r.ID
	return r, true, nil
}

// recoverRotation relocates a cursor whose active commits.jsonl was sealed and
// truncated while the pipeline lagged behind it.
func (c *fileHistoryCursor) recoverRotation() (bool, error) {
	active := filepath.Base(c.path)
	if c.position.Segment != "" && c.position.Segment != active {
		return false, nil
	}
	segments, err := c.segments()
	if err != nil || len(segments) <= 1 {
		return false, err
	}
	if segments[len(segments)-2] == c.sealedTail {
		return false, nil
	}
	if c.position.LastID == "" {
		c.position.Segment, c.position.Offset = segments[0], 0
		return true, c.openCurrent()
	}
	for _, segment := range segments[:len(segments)-1] {
		offset, found, err := historySegmentPosition(filepath.Join(filepath.Dir(c.path), segment), c.position.LastID)
		if err != nil {
			return false, err
		}
		if found {
			c.position.Segment, c.position.Offset = segment, offset
			return true, c.openCurrent()
		}
	}
	// Retention removed the checkpointed record. Resume at the retained active
	// tail rather than replaying every retained segment.
	c.position.Segment, c.position.Offset = active, 0
	return true, c.openCurrent()
}

func historySegmentPosition(file, id string) (int64, bool, error) {
	reader, closer, err := openHistorySegment(file)
	if err != nil {
		return 0, false, err
	}
	defer closer.Close()
	scanner := bufio.NewScanner(reader)
	var offset int64
	for scanner.Scan() {
		offset += int64(len(scanner.Bytes()) + 1)
		var record CommitRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return 0, false, err
		}
		if record.ID == id {
			return offset, true, nil
		}
	}
	return 0, false, scanner.Err()
}

func openHistorySegment(file string) (io.Reader, io.Closer, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	if !strings.HasSuffix(file, ".zst") {
		return f, f, nil
	}
	zr, err := zstd.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return zr, multiCloser{decoderCloser{zr}, f}, nil
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
	c.position = position
	if c.position.Segment == "" && (position.Offset != 0 || position.LastID != "") {
		c.position.Segment = filepath.Base(c.path)
	}
	return c.openCurrent()
}

// HistoryCursorLagBytes reports the retained logical journal bytes after a
// cursor position. Compressed segments are measured after decompression.
func HistoryCursorLagBytes(file string, position HistoryPosition) (int64, error) {
	c := &fileHistoryCursor{path: file}
	segments, err := c.segments()
	if err != nil {
		return 0, err
	}
	current := position.Segment
	if current == "" {
		current = filepath.Base(file)
	}
	start := 0
	for i, segment := range segments {
		if segment == current {
			start = i
			break
		}
	}
	var lag int64
	for i := start; i < len(segments); i++ {
		size, err := historySegmentLogicalSize(filepath.Join(filepath.Dir(file), segments[i]))
		if err != nil {
			return 0, err
		}
		if i == start && segments[i] == current {
			size -= min(position.Offset, size)
		}
		lag += size
	}
	return lag, nil
}

func historySegmentLogicalSize(file string) (int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if !strings.HasSuffix(file, ".zst") {
		st, err := f.Stat()
		if err != nil {
			return 0, err
		}
		return st.Size(), nil
	}
	zr, err := zstd.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	return io.Copy(io.Discard, zr)
}

// RotateCommitJournal seals commits.jsonl into a zstd segment and removes
// segments older than retention. A zero retention keeps all segments.
func RotateCommitJournal(ctx context.Context, file string, now time.Time, retention time.Duration) error {
	commitJournalMu.Lock()
	defer commitJournalMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := os.Stat(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	segmentDay := now.UTC()
	if err == nil && st.Size() > 0 {
		segmentDay = st.ModTime().UTC()
		f, openErr := os.Open(file)
		if openErr != nil {
			return openErr
		}
		line, readErr := bufio.NewReader(f).ReadBytes('\n')
		_ = f.Close()
		if readErr != nil {
			return fmt.Errorf("reading commit journal for rotation: %w", readErr)
		}
		var first CommitRecord
		if decodeErr := json.Unmarshal(line, &first); decodeErr != nil {
			return fmt.Errorf("reading commit journal for rotation: %w", decodeErr)
		}
		if !first.Time.IsZero() {
			segmentDay = first.Time.UTC()
		}
	}
	if err == nil && st.Size() > 0 && !sameUTCDate(segmentDay, now) {
		name := fmt.Sprintf("commits-%s.jsonl.zst", segmentDay.Format("2006-01-02"))
		dst := filepath.Join(filepath.Dir(file), name)
		for i := 1; ; i++ {
			if _, e := os.Stat(dst); errors.Is(e, os.ErrNotExist) {
				break
			}
			dst = filepath.Join(filepath.Dir(file), fmt.Sprintf("commits-%s-%d.jsonl.zst", segmentDay.Format("2006-01-02"), i))
		}
		src, e := os.Open(file)
		if e != nil {
			return e
		}
		tmp := dst + ".tmp"
		out, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if e != nil {
			src.Close()
			return e
		}
		zw, e := zstd.NewWriter(out)
		if e == nil {
			_, e = copyContext(ctx, zw, src)
			if ce := zw.Close(); e == nil {
				e = ce
			}
		}
		if syncErr := out.Sync(); e == nil {
			e = syncErr
		}
		if ce := out.Close(); e == nil {
			e = ce
		}
		src.Close()
		if e != nil {
			os.Remove(tmp)
			return e
		}
		if e = os.Rename(tmp, dst); e != nil {
			return e
		}
		if e = syncDirectory(filepath.Dir(file)); e != nil {
			return e
		}
		if e = os.Truncate(file, 0); e != nil {
			return e
		}
	}
	return pruneHistory(ctx, filepath.Dir(file), now, retention)
}
func sameUTCDate(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}
func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	return io.Copy(w, readerWithContext{ctx, r})
}

type readerWithContext struct {
	context.Context
	io.Reader
}

func (r readerWithContext) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
func pruneHistory(ctx context.Context, dir string, now time.Time, retention time.Duration) error {
	if retention <= 0 {
		return ctx.Err()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := e.Name()
		if !strings.HasPrefix(n, "commits-") || (!strings.HasSuffix(n, ".jsonl") && !strings.HasSuffix(n, ".jsonl.zst")) {
			continue
		}
		dayText := strings.TrimPrefix(n, "commits-")
		if len(dayText) < len("2006-01-02") {
			continue
		}
		day, err := time.Parse("2006-01-02", dayText[:len("2006-01-02")])
		if err != nil {
			continue
		}
		if day.Add(24 * time.Hour).Before(now.UTC().Add(-retention)) {
			if err = os.Remove(filepath.Join(dir, n)); err != nil {
				return err
			}
		}
	}
	return nil
}

// GarbageCollectHistoryBlobs first validates every retained journal record,
// then deletes objects not referenced by any record. Corruption causes no deletes.
func GarbageCollectHistoryBlobs(ctx context.Context, historyDir string) (HistoryGCStats, error) {
	active := filepath.Join(historyDir, "commits.jsonl")
	if b, err := os.ReadFile(active); err != nil && !errors.Is(err, os.ErrNotExist) {
		return HistoryGCStats{}, err
	} else if len(b) > 0 && b[len(b)-1] != '\n' {
		return HistoryGCStats{}, errors.New("history commit journal has a partial record")
	}
	c, err := OpenHistoryCursor(active, HistoryPosition{})
	if err != nil {
		return HistoryGCStats{}, err
	}
	refs := map[string]struct{}{}
	for {
		r, ok, e := c.Next(ctx)
		if e != nil {
			return HistoryGCStats{}, e
		}
		if !ok {
			break
		}
		for _, l := range r.Leaves {
			if l.BeforeHash != "" {
				refs[l.BeforeHash] = struct{}{}
			}
			if l.AfterHash != "" {
				refs[l.AfterHash] = struct{}{}
			}
		}
	}
	var stats HistoryGCStats
	root := filepath.Join(historyDir, "objects")
	err = filepath.WalkDir(root, func(p string, e os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			return nil
		}
		if _, ok := refs[e.Name()]; ok {
			return nil
		}
		st, err := e.Info()
		if err != nil {
			return err
		}
		if err = os.Remove(p); err != nil {
			return err
		}
		stats.Objects++
		stats.Bytes += st.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return stats, err
}
