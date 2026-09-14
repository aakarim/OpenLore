package analytics

import (
	"bufio"
	"context"
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
type logSnapshot struct {
	segment Segment
	data    []byte
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
	segments := l.Segments()
	snapshots := make([]logSnapshot, 0, len(segments))
	for _, seg := range segments {
		data, err := os.ReadFile(seg.Path)
		if err != nil {
			l.mu.Unlock()
			return err
		}
		snapshots = append(snapshots, logSnapshot{seg, data})
	}
	l.mu.Unlock()
	return scanSnapshots(ctx, snapshots, f, fn)
}

func scanSnapshots(ctx context.Context, snapshots []logSnapshot, f EventFilter, fn func(Event) error) error {
	types, principals := map[string]bool{}, map[string]bool{}
	for _, v := range f.Types {
		types[v] = true
	}
	for _, v := range f.Principals {
		principals[v] = true
	}
	for _, snapshot := range snapshots {
		seg := snapshot.segment
		if err := ctx.Err(); err != nil {
			return err
		}
		var reader io.Reader = strings.NewReader(string(snapshot.data))
		var decoder *zstd.Decoder
		if seg.Compressed {
			var err error
			decoder, err = zstd.NewReader(reader)
			if err != nil {
				return err
			}
			reader = decoder
		}
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scan.Scan() {
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
		err := scan.Err()
		if decoder != nil {
			decoder.Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// scanIncremental snapshots only bytes not observed by this cursor. It is a
// private optimization used by Pipeline; EventLog's stable interface remains
// unchanged.
func (l *fileEventLog) scanIncremental(ctx context.Context, cursor logCursor, fn func(Event) error) error {
	l.mu.Lock()
	segments := l.Segments()
	snapshots := make([]logSnapshot, 0, len(segments))
	for _, seg := range segments {
		offset := cursor[seg.Path]
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
		file, err := os.Open(seg.Path)
		if err != nil {
			l.mu.Unlock()
			return err
		}
		if offset > 0 {
			_, err = file.Seek(offset, io.SeekStart)
		}
		data, readErr := io.ReadAll(file)
		file.Close()
		if err != nil || readErr != nil {
			l.mu.Unlock()
			if err != nil {
				return err
			}
			return readErr
		}
		cursor[seg.Path] = seg.Size
		snapshots = append(snapshots, logSnapshot{seg, data})
	}
	l.mu.Unlock()
	return scanSnapshots(ctx, snapshots, EventFilter{}, fn)
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
func (r *Recorder) SetHandoff(ch chan<- Event) { r.handoff = ch }
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

type fileAggregationStore struct {
	dir string
	mu  sync.Mutex
}

func OpenAggregationStore(dir string) (AggregationStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileAggregationStore{dir: dir}, nil
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
