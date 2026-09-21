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
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventLogLargeScanStreamsCompleteSnapshot(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "events-2026-09-21.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 64*1024)
	payload := strings.Repeat("x", 2048)
	encoder := json.NewEncoder(writer)
	const total = 10000
	for i := range total {
		if err := encoder.Encode(Event{ID: fmt.Sprintf("event-%d", i), Time: time.Date(2026, 9, 21, 12, 0, 0, i, time.UTC), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "payload": payload}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	log, err := OpenEventLog(dir, LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := log.Scan(context.Background(), EventFilter{}, func(Event) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != total {
		t.Fatalf("streamed events = %d, want %d", count, total)
	}
}

func TestEventLogScanSurvivesConcurrentSegmentSeal(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenEventLog(dir, LogOptions{Rotate: time.Hour, Compress: "zstd"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	for _, id := range []string{"one", "two"} {
		if err := log.Append(context.Background(), Event{ID: id, Time: old, Type: "doc.read"}); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	count := 0
	go func() {
		result <- log.Scan(context.Background(), EventFilter{}, func(Event) error {
			count++
			if count == 1 {
				close(entered)
				<-release
			}
			return nil
		})
	}()
	<-entered
	if err := log.Seal(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err != nil || count != 2 {
		t.Fatalf("scan after seal count=%d err=%v", count, err)
	}
}

type retryRemote struct {
	mu      sync.Mutex
	fail    bool
	objects map[string][]byte
}

func (r *retryRemote) Put(_ context.Context, key string, src io.Reader, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		r.fail = false
		return errors.New("temporary")
	}
	b, err := io.ReadAll(src)
	if err == nil {
		r.objects[key] = b
	}
	return err
}
func (*retryRemote) List(context.Context, string) ([]string, error)     { return nil, nil }
func (*retryRemote) Get(context.Context, string) (io.ReadCloser, error) { return nil, os.ErrNotExist }

func TestShipperRetriesActiveRangeWithoutAdvancingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	log, _ := OpenEventLog(dir, LogOptions{Compress: "none"})
	if err := log.Append(context.Background(), Event{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	remote := &retryRemote{fail: true, objects: map[string][]byte{}}
	shipper := NewShipper(log, remote, time.Hour)
	if err := shipper.ShipNow(context.Background()); err == nil {
		t.Fatal("expected transient upload error")
	}
	if _, err := os.Stat(filepath.Join(dir, "shipper.checkpoint")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint advanced after failure: %v", err)
	}
	if err := shipper.ShipNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lag, _ := shipper.Lag(); lag != 0 {
		t.Fatalf("lag = %d, want 0", lag)
	}
	if len(remote.objects) != 1 {
		t.Fatalf("uploaded objects = %d, want 1", len(remote.objects))
	}
	for key := range remote.objects {
		if !strings.HasPrefix(key, "events/") || !strings.HasSuffix(key, ".jsonl") {
			t.Fatalf("active object key = %q, want events/<day>/<start>-<end>.jsonl", key)
		}
	}
}

func TestAggregationStoreMovingWindowKeyIsStable(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenAggregationStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := Params{Since: now.Add(-time.Hour), Until: now, Limit: 10, Extra: map[string]string{"b": "2", "a": "1"}}
	b := Params{Since: now.Add(time.Minute - time.Hour), Until: now.Add(time.Minute), Limit: 10, Extra: map[string]string{"a": "1", "b": "2"}}
	if err := store.Put(context.Background(), "test", a, Materialized{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "test", b, Materialized{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(context.Background(), "test", a); err != nil || !ok {
		t.Fatalf("stable moving-window key was not found: ok=%v err=%v", ok, err)
	}
}

func TestPipelineMissingCheckpointProcessesRetainedTail(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(context.Background(), Event{ID: "retained", Type: "source"}); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "checkpoint")
	if err := os.WriteFile(checkpoint, []byte("expired\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &countingProcessor{}
	pipeline := NewPipeline(log, checkpoint, PipelineOptions{Processors: []Processor{p}})
	pipeline.Run(context.Background())
	waitForCheckpoint(t, checkpoint, "retained")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("processor calls = %d, want 1", p.calls)
	}
}

type shutdownScanLog struct {
	EventLog
	entered  chan struct{}
	stopping <-chan struct{}
}

func (l *shutdownScanLog) Scan(ctx context.Context, filter EventFilter, fn func(Event) error) error {
	// Hold the startup scan until Close requests shutdown. Cancellation must
	// not invalidate the worker's context before this accepted work completes.
	select {
	case l.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
	case <-l.stopping:
	}
	return l.EventLog.Scan(ctx, filter, fn)
}

func TestPipelineCloseFinishesLiveWorkBeforeCanceling(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := log.Append(context.Background(), Event{ID: "source", Type: "source"}); err != nil {
		t.Fatal(err)
	}
	gated := &shutdownScanLog{EventLog: log, entered: make(chan struct{}, 1)}
	p := NewPipeline(gated, filepath.Join(t.TempDir(), "checkpoint"), PipelineOptions{
		Processors: []Processor{&countingProcessor{}},
		Sink: sinkFunc(func(ctx context.Context, e Event) {
			if err := log.Append(ctx, e); err != nil {
				t.Errorf("persist derived event: %v", err)
			}
		}),
	})
	gated.stopping = p.stop
	p.Run(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-gated.entered:
	case <-ctx.Done():
		t.Fatal("pipeline did not start scanning")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := log.Scan(context.Background(), EventFilter{}, func(e Event) error {
		ids = append(ids, e.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "source" || ids[1] != "source-derived" {
		t.Fatalf("shutdown lost or duplicated an event: %v", ids)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}

type blockingProcessor struct {
	mu                       sync.Mutex
	active, maxActive, calls int
}

func (p *blockingProcessor) Name() string { return "blocking" }
func (p *blockingProcessor) Process(context.Context, Event) []Event {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	p.mu.Lock()
	p.active--
	p.calls++
	p.mu.Unlock()
	return nil
}

func TestPipelineReplaySerializesWithLiveProcessing(t *testing.T) {
	log, _ := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	_ = log.Append(context.Background(), Event{ID: "one", Type: "source"})
	processor := &blockingProcessor{}
	p := NewPipeline(log, filepath.Join(t.TempDir(), "checkpoint"), PipelineOptions{Processors: []Processor{processor}})
	p.Run(context.Background())
	p.Handoff() <- Event{ID: "live", Type: "source"}
	if err := p.Replay(context.Background(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	if processor.maxActive != 1 {
		t.Fatalf("concurrent processor calls = %d", processor.maxActive)
	}
}

func TestEventLogRotationRetentionAndLateEvents(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenEventLog(dir, LogOptions{Rotate: time.Hour, Compress: "zstd", Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-3 * time.Hour)
	for _, at := range []time.Time{old, old.Add(90 * time.Minute)} {
		if err := log.Append(context.Background(), Event{ID: NewID(), Time: at}); err != nil {
			t.Fatal(err)
		}
	}
	if len(log.Segments()) != 2 {
		t.Fatalf("segments = %d, want 2", len(log.Segments()))
	}
	if err := log.Seal(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(log.Segments()) != 0 {
		t.Fatal("retention did not remove old compressed segments")
	}

	lateLog, _ := OpenEventLog(dir, LogOptions{Rotate: time.Hour, Compress: "zstd"})
	if err := lateLog.Append(context.Background(), Event{ID: "original", Time: old}); err != nil {
		t.Fatal(err)
	}
	if err := lateLog.Seal(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	sealed := lateLog.Segments()[0].Path
	before, _ := os.ReadFile(sealed)
	if err := lateLog.Append(context.Background(), Event{ID: "late", Time: old}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(sealed)
	if string(before) != string(after) {
		t.Fatal("late append changed sealed segment")
	}
}

func TestEventLogRetentionWithoutCompression(t *testing.T) {
	log, _ := OpenEventLog(t.TempDir(), LogOptions{Rotate: time.Hour, Compress: "none", Retention: time.Hour})
	_ = log.Append(context.Background(), Event{Time: time.Now().Add(-2 * time.Hour)})
	if err := log.Seal(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(log.Segments()) != 0 {
		t.Fatal("retention did not remove uncompressed segment")
	}
}

func TestEventLogDefaultRotationKeepsDailyFilename(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	if err := log.Append(context.Background(), Event{Time: at}); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(log.Segments()[0].Path); got != "events-2026-09-14.jsonl" {
		t.Fatalf("segment name = %q", got)
	}
}
