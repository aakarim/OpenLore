package analytics

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

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
