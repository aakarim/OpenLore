package analytics

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

type Segment struct {
	Day        time.Time
	Path       string
	Compressed bool
	Size       int64
}
type LogOptions struct {
	Rotate    time.Duration
	Compress  string
	Retention time.Duration
}
type EventLog interface {
	EventSource
	Append(context.Context, Event) error
	Segments() []Segment
	Seal(context.Context, time.Time) error
	Close() error
}
type fileEventLog struct {
	dir            string
	opts           LogOptions
	mu             sync.Mutex
	closed         bool
	activePathName string
	active         *os.File
}

type logCursor map[string]int64
type openLogSegment struct {
	segment Segment
	file    *os.File
	offset  int64
}

func OpenEventLog(dir string, opts LogOptions) (EventLog, error) {
	if opts.Rotate == 0 {
		opts.Rotate = 24 * time.Hour
	}
	if opts.Compress == "" {
		opts.Compress = "zstd"
	}
	if opts.Compress != "zstd" && opts.Compress != "none" {
		return nil, fmt.Errorf("unsupported analytics compression %q", opts.Compress)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileEventLog{dir: dir, opts: opts}, nil
}
func (l *fileEventLog) activePath(t time.Time) string {
	start := t.UTC().Truncate(l.opts.Rotate)
	name := strconv.FormatInt(start.UnixNano(), 10)
	if l.opts.Rotate == 24*time.Hour {
		name = start.Format("2006-01-02")
	}
	return filepath.Join(l.dir, "events-"+name+".jsonl")
}
func (l *fileEventLog) Append(ctx context.Context, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("analytics event log closed")
	}
	active := l.activePath(e.Time)
	if _, err := os.Stat(active + ".zst"); err == nil {
		// A late event must never recreate a sealed segment: a subsequent seal
		// would otherwise replace the existing compressed file.
		active = strings.TrimSuffix(active, ".jsonl") + "-late-" + NewID() + ".jsonl"
	}
	if l.active == nil || l.activePathName != active {
		if l.active != nil {
			if err := l.active.Sync(); err != nil {
				return err
			}
			if err := l.active.Close(); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(active, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		l.active, l.activePathName = f, active
	}
	encErr := json.NewEncoder(l.active).Encode(e)
	if encErr == nil {
		encErr = l.active.Sync()
	}
	return encErr
}
func (l *fileEventLog) Segments() []Segment {
	entries, _ := os.ReadDir(l.dir)
	var out []Segment
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "events-") || !(strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.zst")) {
			continue
		}
		startText := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(n, "events-"), ".zst"), ".jsonl")
		startText = strings.SplitN(startText, "-late-", 2)[0]
		nanos, err := strconv.ParseInt(startText, 10, 64)
		var day time.Time
		if err == nil {
			day = time.Unix(0, nanos).UTC()
		} else {
			// Continue to recognize logs written by the original daily format.
			day, _ = time.Parse("2006-01-02", startText)
		}
		info, _ := e.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		out = append(out, Segment{Day: day, Path: filepath.Join(l.dir, n), Compressed: strings.HasSuffix(n, ".zst"), Size: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out
}
func (l *fileEventLog) Scan(ctx context.Context, f EventFilter, fn func(Event) error) error {
	l.mu.Lock()
	segments, err := l.openSegmentsLocked(nil)
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return scanOpenSegments(ctx, segments, f, fn)
}

func (l *fileEventLog) openSegmentsLocked(cursor logCursor) ([]openLogSegment, error) {
	if l.active != nil {
		if err := l.active.Sync(); err != nil {
			return nil, err
		}
	}
	var opened []openLogSegment
	for _, seg := range l.Segments() {
		offset := int64(0)
		if cursor != nil {
			offset = cursor[seg.Path]
			logical := strings.TrimSuffix(seg.Path, ".zst")
			if seg.Compressed {
				if _, sealedBefore := cursor[logical]; sealedBefore {
					cursor[seg.Path] = seg.Size
					continue
				}
				offset = 0
			}
			if offset >= seg.Size {
				continue
			}
		}
		file, err := os.Open(seg.Path)
		if err != nil {
			closeOpenSegments(opened)
			return nil, err
		}
		if offset > 0 {
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				file.Close()
				closeOpenSegments(opened)
				return nil, err
			}
		}
		opened = append(opened, openLogSegment{segment: seg, file: file, offset: offset})
	}
	return opened, nil
}

func closeOpenSegments(segments []openLogSegment) {
	for _, segment := range segments {
		_ = segment.file.Close()
	}
}

func scanOpenSegments(ctx context.Context, segments []openLogSegment, f EventFilter, fn func(Event) error) error {
	defer closeOpenSegments(segments)
	types, principals := map[string]bool{}, map[string]bool{}
	for _, v := range f.Types {
		types[v] = true
	}
	for _, v := range f.Principals {
		principals[v] = true
	}
	for _, opened := range segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		seg := opened.segment
		var reader io.Reader = io.LimitReader(opened.file, seg.Size-opened.offset)
		var decoder *zstd.Decoder
		if seg.Compressed {
			var err error
			decoder, err = zstd.NewReader(reader)
			if err != nil {
				return err
			}
			reader = decoder
		}
		scanErr := func() error {
			if decoder != nil {
				defer decoder.Close()
			}
			scan := bufio.NewScanner(reader)
			scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for scan.Scan() {
				if err := ctx.Err(); err != nil {
					return err
				}
				var e Event
				if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
					return err
				}
				if !f.From.IsZero() && e.Time.Before(f.From) || !f.To.IsZero() && e.Time.After(f.To) || len(types) > 0 && !types[e.Type] || len(principals) > 0 && !principals[e.Principal] {
					continue
				}
				if err := fn(e); err != nil {
					return err
				}
			}
			return scan.Err()
		}()
		if scanErr != nil {
			return scanErr
		}
	}
	return nil
}

// scanIncremental snapshots only bytes not observed by this cursor. It is a
// private optimization used by Pipeline; EventLog's stable interface remains
// unchanged.
func (l *fileEventLog) scanIncremental(ctx context.Context, cursor logCursor, fn func(Event) error) error {
	l.mu.Lock()
	segments, err := l.openSegmentsLocked(cursor)
	l.mu.Unlock()
	if err != nil {
		return err
	}
	for i, opened := range segments {
		if err := scanOpenSegments(ctx, []openLogSegment{opened}, EventFilter{}, fn); err != nil {
			closeOpenSegments(segments[i+1:])
			return err
		}
		cursor[opened.segment.Path] = opened.segment.Size
	}
	return nil
}
func (l *fileEventLog) Seal(ctx context.Context, before time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// A file selected for sealing must not remain writable through the active
	// descriptor. Closing it under the same lock also makes scans and sealing
	// observe a complete, synced prefix.
	if l.active != nil {
		activeDay := segmentDay(l.activePathName)
		if activeDay.Add(l.opts.Rotate).Before(before.UTC()) || activeDay.Add(l.opts.Rotate).Equal(before.UTC()) {
			if err := l.active.Sync(); err != nil {
				return err
			}
			if err := l.active.Close(); err != nil {
				return err
			}
			l.active, l.activePathName = nil, ""
		}
	}
	if l.opts.Compress == "zstd" {
		for _, seg := range l.Segments() {
			if seg.Compressed || seg.Day.Add(l.opts.Rotate).After(before.UTC()) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			src, err := os.Open(seg.Path)
			if err != nil {
				return err
			}
			tmp := seg.Path + ".zst.tmp"
			dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				src.Close()
				return err
			}
			zw, err := zstd.NewWriter(dst)
			if err == nil {
				_, err = io.Copy(zw, src)
				if closeErr := zw.Close(); err == nil {
					err = closeErr
				}
			}
			src.Close()
			if syncErr := dst.Sync(); err == nil {
				err = syncErr
			}
			dst.Close()
			if err != nil {
				os.Remove(tmp)
				return err
			}
			if err = os.Rename(tmp, seg.Path+".zst"); err != nil {
				return err
			}
			if err = os.Remove(seg.Path); err != nil {
				return err
			}
		}
	}
	if l.opts.Retention > 0 {
		cutoff := time.Now().UTC().Add(-l.opts.Retention)
		for _, seg := range l.Segments() {
			if seg.Day.Before(cutoff) {
				_ = os.Remove(seg.Path)
			}
		}
	}
	return nil
}
func segmentDay(path string) time.Time {
	name := filepath.Base(path)
	text := strings.TrimSuffix(strings.TrimPrefix(name, "events-"), ".jsonl")
	text = strings.SplitN(text, "-late-", 2)[0]
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return time.Unix(0, n).UTC()
	}
	t, _ := time.Parse("2006-01-02", text)
	return t
}

func (l *fileEventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.active == nil {
		return nil
	}
	err := l.active.Sync()
	if closeErr := l.active.Close(); err == nil {
		err = closeErr
	}
	l.active, l.activePathName = nil, ""
	return err
}

type Recorder struct {
	log                      EventLog
	queue                    chan Event
	handoff                  chan<- Event
	live                     Consumer
	dropped, droppedShutdown atomic.Int64
	mu                       sync.RWMutex
	closed                   bool
	started                  bool
	done                     chan struct{}
	start                    sync.Once
}

func NewRecorder(log EventLog, buffer int) *Recorder {
	if buffer <= 0 {
		buffer = 1024
	}
	return &Recorder{log: log, queue: make(chan Event, buffer), done: make(chan struct{})}
}
func (r *Recorder) SetHandoff(ch chan<- Event)        { r.handoff = ch }
func (r *Recorder) SetLiveConsumer(consumer Consumer) { r.live = consumer }
func (r *Recorder) Start(ctx context.Context) {
	r.start.Do(func() {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return
		}
		r.started = true
		r.mu.Unlock()
		go func() {
			defer close(r.done)
			for e := range r.queue {
				appendCtx := context.WithoutCancel(ctx)
				if err := r.log.Append(appendCtx, e); err != nil {
					r.dropped.Add(1)
					continue
				}
				if r.live != nil {
					r.live.Consume(appendCtx, e)
				}
				if r.handoff != nil {
					select {
					case r.handoff <- e:
					default:
					}
				}
			}
		}()
	})
}
func (r *Recorder) Record(_ context.Context, e Event) {
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		r.droppedShutdown.Add(1)
		return
	}
	select {
	case r.queue <- e:
	default:
		r.dropped.Add(1)
	}
}
func (r *Recorder) Dropped() int64           { return r.dropped.Load() }
func (r *Recorder) DroppedAtShutdown() int64 { return r.droppedShutdown.Load() }
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
		if !r.started {
			close(r.done)
		}
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.dropped.Add(int64(len(r.queue)))
		return ctx.Err()
	}
}

// OpenAggregationStore opens the legacy file-backed materialization store.
// New services default to OpenSQLiteAggregationStore.
func OpenAggregationStore(dir string) (AggregationStore, error) {
	return OpenFileAggregationStore(dir)
}

type fileAggregationStore struct {
	dir string
	mu  sync.Mutex
}

func OpenFileAggregationStore(dir string) (AggregationStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileAggregationStore{dir: dir}, nil
}

type SQLiteAggregationStore struct {
	db *sql.DB
	mu sync.Mutex
}

// OpenSQLiteAggregationStore opens (or creates) a SQLite aggregation store at
// path. SQLite WAL mode and a busy timeout permit concurrent readers/writers.
func OpenSQLiteAggregationStore(path string) (*SQLiteAggregationStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000; PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS materializations (
key TEXT PRIMARY KEY, name TEXT NOT NULL, value BLOB NOT NULL
); CREATE INDEX IF NOT EXISTS materializations_name ON materializations(name);
CREATE TABLE IF NOT EXISTS files (
path TEXT PRIMARY KEY, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL,
content_hash TEXT NOT NULL, computed_at INTEGER NOT NULL, owner TEXT NOT NULL DEFAULT '',
generation INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS file_scalars (
path TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
source TEXT NOT NULL, scalar TEXT NOT NULL, value REAL NOT NULL,
PRIMARY KEY(path, source, scalar)
) ;
CREATE TABLE IF NOT EXISTS directory_facts (
path TEXT NOT NULL, owner TEXT NOT NULL, files INTEGER NOT NULL,
bytes REAL NOT NULL, lines REAL NOT NULL, characters REAL NOT NULL, tokens REAL NOT NULL,
computed_at INTEGER NOT NULL, PRIMARY KEY(path, owner)
);
CREATE TABLE IF NOT EXISTS facts_scan_state (
id INTEGER PRIMARY KEY CHECK(id=1), generation INTEGER NOT NULL,
state TEXT NOT NULL, started_at INTEGER NOT NULL, completed_at INTEGER NOT NULL DEFAULT 0,
error TEXT NOT NULL DEFAULT '', scope_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS facts_scan_queue (
generation INTEGER NOT NULL, path TEXT NOT NULL, PRIMARY KEY(generation,path)
);
CREATE TABLE IF NOT EXISTS dashboard_fact_state (
key TEXT PRIMARY KEY, generation INTEGER NOT NULL, cursor TEXT NOT NULL DEFAULT '',
state TEXT NOT NULL, computed_at INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS dashboard_fact_paths (
key TEXT NOT NULL REFERENCES dashboard_fact_state(key) ON DELETE CASCADE,
path TEXT NOT NULL, PRIMARY KEY(key,path)
);
CREATE TABLE IF NOT EXISTS dashboard_views (
key TEXT PRIMARY KEY, value BLOB, computed_at INTEGER NOT NULL, error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS analytics_events (
id TEXT PRIMARY KEY, time_ns INTEGER NOT NULL, type TEXT NOT NULL, principal TEXT NOT NULL, value BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS analytics_events_time ON analytics_events(time_ns);`); err != nil {
		db.Close()
		return nil, err
	}
	// SQLite has no IF NOT EXISTS form for ADD COLUMN. Existing stores are
	// upgraded in place; duplicate-column is the expected no-op on new stores.
	if _, alterErr := db.Exec(`ALTER TABLE files ADD COLUMN owner TEXT NOT NULL DEFAULT ''`); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column") {
		db.Close()
		return nil, alterErr
	}
	if _, alterErr := db.Exec(`ALTER TABLE files ADD COLUMN generation INTEGER NOT NULL DEFAULT 0`); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column") {
		db.Close()
		return nil, alterErr
	}
	return &SQLiteAggregationStore{db: db}, nil
}
func paramsKey(name string, p Params) string {
	// Absolute endpoints make a periodic moving window unique forever. Key the
	// cache by its stable shape instead: whole-second duration, limit and Extra
	// (encoding/json sorts string map keys).
	duration := p.Until.Sub(p.Since).Round(time.Second)
	stable := struct {
		Duration int64             `json:"duration_seconds"`
		Limit    int               `json:"limit"`
		Extra    map[string]string `json:"extra,omitempty"`
	}{int64(duration / time.Second), p.Limit, p.Extra}
	b, _ := json.Marshal(stable)
	var sum uint64 = 1469598103934665603
	for _, v := range b {
		sum ^= uint64(v)
		sum *= 1099511628211
	}
	return fmt.Sprintf("%s-%016x.json", name, sum)
}
func (s *fileAggregationStore) Put(_ context.Context, name string, p Params, m Materialized) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, paramsKey(name, p)+".tmp")
	if err = os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, paramsKey(name, p)))
}
func (s *fileAggregationStore) Get(_ context.Context, name string, p Params) (Materialized, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.dir, paramsKey(name, p)))
	if errors.Is(err, os.ErrNotExist) {
		return Materialized{}, false, nil
	}
	if err != nil {
		return Materialized{}, false, err
	}
	var m Materialized
	err = json.Unmarshal(b, &m)
	return m, err == nil, err
}
func (s *fileAggregationStore) Invalidate(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(s.dir, name+"-*.json"))
	for _, p := range matches {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}
func (s *fileAggregationStore) Close() error { return nil }

func (s *SQLiteAggregationStore) Put(ctx context.Context, name string, p Params, m Materialized) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO materializations(key,name,value) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET name=excluded.name,value=excluded.value`, paramsKey(name, p), name, b)
	return err
}
func (s *SQLiteAggregationStore) Get(ctx context.Context, name string, p Params) (Materialized, bool, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM materializations WHERE key=?`, paramsKey(name, p)).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Materialized{}, false, nil
	}
	if err != nil {
		return Materialized{}, false, err
	}
	var m Materialized
	err = json.Unmarshal(b, &m)
	return m, err == nil, err
}
func (s *SQLiteAggregationStore) Invalidate(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM materializations WHERE name=?`, name)
	return err
}
func (s *SQLiteAggregationStore) Close() error { return s.db.Close() }
