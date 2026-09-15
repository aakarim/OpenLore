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
	c := &fileHistoryCursor{path: file}
	if err := c.restorePositionLocked(from); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *fileHistoryCursor) segments() ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(c.path))
	if err != nil {
		return nil, err
	}
	segments := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && (strings.HasPrefix(n, "commits-") && (strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.zst"))) {
			logical := strings.TrimSuffix(n, ".zst")
			if previous, exists := segments[logical]; !exists || strings.HasSuffix(n, ".zst") && !strings.HasSuffix(previous, ".zst") {
				segments[logical] = n
			}
		}
	}
	out := make([]string, 0, len(segments)+1)
	for _, name := range segments {
		out = append(out, name)
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
	} else if strings.HasSuffix(name, ".jsonl") {
		for _, segment := range segments {
			if segment == name+".zst" {
				name = segment
				c.position.Segment = segment
				break
			}
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
func (c *fileHistoryCursor) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closer == nil {
		return nil
	}
	err := c.closer.Close()
	c.closer = nil
	c.reader = nil
	return err
}
func (c *fileHistoryCursor) advance() (bool, error) {
	segments, err := c.segments()
	if err != nil {
		return false, err
	}
	if strings.HasSuffix(c.position.Segment, ".jsonl") {
		for _, segment := range segments {
			if segment == c.position.Segment+".zst" {
				c.position.Segment = segment
				break
			}
		}
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
		if c.position.Segment != "" && c.position.Segment != filepath.Base(c.path) {
			return CommitRecord{}, false, io.ErrUnexpectedEOF
		}
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
	if errors.Is(err, io.EOF) {
		if c.position.Segment != "" && c.position.Segment != filepath.Base(c.path) {
			return CommitRecord{}, false, io.ErrUnexpectedEOF
		}
		if seekErr := c.openCurrent(); seekErr != nil {
			return CommitRecord{}, false, seekErr
		}
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
	buffered := bufio.NewReader(reader)
	var offset int64
	for {
		line, err := buffered.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return 0, false, nil
		}
		if errors.Is(err, io.EOF) {
			return 0, false, io.ErrUnexpectedEOF
		}
		if err != nil {
			return 0, false, err
		}
		offset += int64(len(line))
		var record CommitRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return 0, false, err
		}
		if record.ID == id {
			return offset, true, nil
		}
	}
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
	return c.restorePositionLocked(position)
}

func (c *fileHistoryCursor) restorePositionLocked(position HistoryPosition) error {
	c.position = position
	active := filepath.Base(c.path)
	wasActive := c.position.Segment == "" || c.position.Segment == active
	if c.position.Segment == "" && (position.Offset != 0 || position.LastID != "") {
		c.position.Segment = filepath.Base(c.path)
	}
	if wasActive && position.LastID != "" {
		segments, err := c.segments()
		if err != nil {
			return err
		}
		for _, segment := range segments[:len(segments)-1] {
			offset, found, err := historySegmentPosition(filepath.Join(filepath.Dir(c.path), segment), position.LastID)
			if err != nil {
				return err
			}
			if found {
				c.position.Segment = segment
				c.position.Offset = offset
				return c.openCurrent()
			}
		}
	}
	if err := c.openCurrent(); err != nil && errors.Is(err, io.EOF) && wasActive && position.LastID != "" {
		// Match live rotation recovery if retention removed a legacy checkpoint.
		c.position = HistoryPosition{Segment: active}
		return c.openCurrent()
	} else {
		return err
	}
}

// HistoryCursorLagBytes reports retained journal bytes after a cursor. New
// compressed segments carry their logical size in a sidecar; legacy segments
// without one use physical size rather than being decompressed on a health read.
func HistoryCursorLagBytes(file string, position HistoryPosition) (int64, error) {
	c := &fileHistoryCursor{path: file}
	segments, err := c.segments()
	if err != nil {
		return 0, err
	}
	current := position.Segment
	if current == "" {
		current = filepath.Base(file)
	} else if strings.HasSuffix(current, ".jsonl") {
		for _, segment := range segments {
			if segment == current+".zst" {
				current = segment
				break
			}
		}
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
	b, err := os.ReadFile(file + ".size")
	if err == nil {
		size, parseErr := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if parseErr != nil || size < 0 {
			return 0, fmt.Errorf("invalid history segment size %q", strings.TrimSpace(string(b)))
		}
		return size, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

var historyMaintenanceMu sync.Mutex

// RotateCommitJournal seals commits.jsonl into a zstd segment and removes
// segments older than retention, but never prunes the segment containing an
// optional protected cursor. A zero retention keeps all segments.
func RotateCommitJournal(ctx context.Context, file string, now time.Time, retention time.Duration, protected ...HistoryPosition) error {
	historyMaintenanceMu.Lock()
	defer historyMaintenanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := compressPendingHistorySegments(ctx, filepath.Dir(file)); err != nil {
		return err
	}
	segment, logicalSize, err := detachCommitJournal(file, now)
	if err != nil {
		return err
	}
	if segment != "" {
		if err := compressHistorySegment(ctx, segment, logicalSize); err != nil {
			return err
		}
	}
	var checkpoint *HistoryPosition
	if len(protected) > 0 {
		checkpoint = &protected[0]
		if segment != "" && (checkpoint.Segment == "" || checkpoint.Segment == filepath.Base(file)) {
			checkpoint.Segment = filepath.Base(segment) + ".zst"
		}
	}
	return pruneHistory(ctx, filepath.Dir(file), now, retention, checkpoint)
}

func compressPendingHistorySegments(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var pending []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, "commits-") && strings.HasSuffix(name, ".jsonl") {
			pending = append(pending, filepath.Join(dir, name))
		}
	}
	sort.Strings(pending)
	for _, segment := range pending {
		st, err := os.Stat(segment)
		if err != nil {
			return err
		}
		if err := compressHistorySegment(ctx, segment, st.Size()); err != nil {
			return err
		}
	}
	return nil
}

// detachCommitJournal atomically gives appenders a fresh active file. The
// potentially expensive compression runs afterwards without blocking commits.
func detachCommitJournal(file string, now time.Time) (string, int64, error) {
	commitJournalMu.Lock()
	defer commitJournalMu.Unlock()
	st, err := os.Stat(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}
	segmentDay := now.UTC()
	if err == nil && st.Size() > 0 {
		segmentDay = st.ModTime().UTC()
		f, openErr := os.Open(file)
		if openErr != nil {
			return "", 0, openErr
		}
		line, readErr := bufio.NewReader(f).ReadBytes('\n')
		_ = f.Close()
		if readErr != nil {
			return "", 0, fmt.Errorf("reading commit journal for rotation: %w", readErr)
		}
		var first CommitRecord
		if decodeErr := json.Unmarshal(line, &first); decodeErr != nil {
			return "", 0, fmt.Errorf("reading commit journal for rotation: %w", decodeErr)
		}
		if !first.Time.IsZero() {
			segmentDay = first.Time.UTC()
		}
	}
	if err != nil || st.Size() == 0 || sameUTCDate(segmentDay, now) {
		return "", 0, nil
	}
	name := fmt.Sprintf("commits-%s.jsonl", segmentDay.Format("2006-01-02"))
	dst := filepath.Join(filepath.Dir(file), name)
	for i := 1; ; i++ {
		if _, plainErr := os.Stat(dst); errors.Is(plainErr, os.ErrNotExist) {
			if _, compressedErr := os.Stat(dst + ".zst"); errors.Is(compressedErr, os.ErrNotExist) {
				break
			}
		}
		dst = filepath.Join(filepath.Dir(file), fmt.Sprintf("commits-%s-%d.jsonl", segmentDay.Format("2006-01-02"), i))
	}
	if err := os.Rename(file, dst); err != nil {
		return "", 0, err
	}
	active, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	if err := active.Close(); err != nil {
		return "", 0, err
	}
	if err := syncDirectory(filepath.Dir(file)); err != nil {
		return "", 0, err
	}
	return dst, st.Size(), nil
}

func compressHistorySegment(ctx context.Context, segment string, logicalSize int64) error {
	src, err := os.Open(segment)
	if err != nil {
		return err
	}
	dst := segment + ".zst"
	tmp := dst + ".tmp"
	sizeTmp := dst + ".size.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		src.Close()
		return err
	}
	zw, err := zstd.NewWriter(out)
	if err == nil {
		_, err = copyContext(ctx, zw, src)
		if closeErr := zw.Close(); err == nil {
			err = closeErr
		}
	}
	if syncErr := out.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	src.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.WriteFile(sizeTmp, []byte(strconv.FormatInt(logicalSize, 10)+"\n"), 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		os.Remove(sizeTmp)
		return err
	}
	if err = os.Rename(sizeTmp, dst+".size"); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Dir(segment)); err != nil {
		return err
	}
	if err = os.Remove(segment); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(segment))
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
func pruneHistory(ctx context.Context, dir string, now time.Time, retention time.Duration, protected *HistoryPosition) error {
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
			if !historySegmentBefore(n, protected) {
				continue
			}
			if err = os.Remove(filepath.Join(dir, n)); err != nil {
				return err
			}
			if err = os.Remove(filepath.Join(dir, n+".size")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func historySegmentBefore(name string, protected *HistoryPosition) bool {
	if protected == nil {
		return true
	}
	if protected.Segment == "" {
		return protected.Offset > 0 || protected.LastID != ""
	}
	if protected.Segment == "commits.jsonl" {
		return true
	}
	day, sequence := historySegmentOrder(name)
	protectedDay, protectedSequence := historySegmentOrder(protected.Segment)
	return day < protectedDay || day == protectedDay && sequence < protectedSequence
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
	if closer, ok := c.(io.Closer); ok {
		defer closer.Close()
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
