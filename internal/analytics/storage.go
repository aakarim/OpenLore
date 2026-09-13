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
	dir    string
	opts   LogOptions
	mu     sync.Mutex
	closed bool
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
	return filepath.Join(l.dir, "events-"+t.UTC().Format("2006-01-02")+".jsonl")
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
	f, err := os.OpenFile(l.activePath(e.Time), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encErr := json.NewEncoder(f).Encode(e)
	if encErr == nil {
		encErr = f.Sync()
	}
	closeErr := f.Close()
	if encErr != nil {
		return encErr
	}
	return closeErr
}
func (l *fileEventLog) Segments() []Segment {
	entries, _ := os.ReadDir(l.dir)
	var out []Segment
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "events-") || !(strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.zst")) {
			continue
		}
		dayText := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(n, "events-"), ".zst"), ".jsonl")
		day, _ := time.Parse("2006-01-02", dayText)
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
	types, principals := map[string]bool{}, map[string]bool{}
	for _, v := range f.Types {
		types[v] = true
	}
	for _, v := range f.Principals {
		principals[v] = true
	}
	for _, seg := range l.Segments() {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := os.Open(seg.Path)
		if err != nil {
			return err
		}
		var reader io.Reader = file
		var decoder *zstd.Decoder
		if seg.Compressed {
			decoder, err = zstd.NewReader(file)
			if err != nil {
				file.Close()
				return err
			}
			reader = decoder
		}
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scan.Scan() {
			var e Event
			if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
				file.Close()
				return err
			}
			if !f.From.IsZero() && e.Time.Before(f.From) || !f.To.IsZero() && e.Time.After(f.To) || len(types) > 0 && !types[e.Type] || len(principals) > 0 && !principals[e.Principal] {
				continue
			}
			if err := fn(e); err != nil {
				file.Close()
				return err
			}
		}
		err = scan.Err()
		if decoder != nil {
			decoder.Close()
		}
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (l *fileEventLog) Seal(ctx context.Context, before time.Time) error {
	if l.opts.Compress == "none" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, seg := range l.Segments() {
		if seg.Compressed || !seg.Day.Before(before.UTC()) {
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
	if l.opts.Retention > 0 {
		cutoff := time.Now().UTC().Add(-l.opts.Retention)
		for _, seg := range l.Segments() {
			if seg.Compressed && seg.Day.Before(cutoff) {
				_ = os.Remove(seg.Path)
			}
		}
	}
	return nil
}
func (l *fileEventLog) Close() error { l.mu.Lock(); l.closed = true; l.mu.Unlock(); return nil }

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
	b, _ := json.Marshal(p)
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
