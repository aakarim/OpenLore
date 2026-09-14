package openlore

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestCapturePreImagesStoresExactBlob(t *testing.T) {
	root := t.TempDir()
	fsys := NewDirFS(root, config.FilesConfig{Allowed: []string{"*.md"}})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.WriteFileAtomic("/doc.md", []byte("before\n"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	blobs, err := OpenBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs := vfs.ChangeSet{Target: "/doc.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("after")}}
	leaves, err := capturePreImages(context.Background(), fsys, blobs, cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 1 || leaves[0].BeforeHash != hashContent([]byte("before\n")) || leaves[0].BeforeSize != 7 {
		t.Fatalf("wrong leaves: %#v", leaves)
	}
	r, size, err := blobs.Get(context.Background(), leaves[0].BeforeHash)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil || size != 7 || string(b) != "before\n" {
		t.Fatalf("wrong blob: %q size=%d err=%v", b, size, err)
	}
	if _, err := os.Stat(root + "/doc.md"); err != nil {
		t.Fatal(err)
	}
}

func TestCapturePreImagesSeparatesExistenceFromUnknownContent(t *testing.T) {
	fsys := NewDirFS(t.TempDir(), config.FilesConfig{Allowed: []string{"*.md"}})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.WriteFileAtomic("/existing.md", []byte("old"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	cs := vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/new.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new")}},
		{Target: "/existing.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("changed")}},
	}}
	leaves, err := capturePreImages(context.Background(), fsys, nil, cs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 2 || leaves[0].BeforeExists || leaves[0].BeforeUnknown || !leaves[1].BeforeExists || !leaves[1].BeforeUnknown {
		t.Fatalf("existence/unknown metadata = %#v", leaves)
	}
}

func TestScalarProcessorDerivesExactBeforeAfterDelta(t *testing.T) {
	ctx := context.Background()
	before := []byte("before\n")
	after := []byte("after text\nnext")
	blobs, err := OpenBlobStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := hashContent(before)
	if err := blobs.Put(ctx, beforeHash, strings.NewReader(string(before))); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(t.TempDir(), "commits.jsonl")
	record := CommitRecord{
		ID:          "commit-1",
		Attribution: Attribution{Principal: "adil"},
		ChangeSet:   vfs.ChangeSet{Target: "/docs/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: after}},
		Leaves:      []LeafRecord{{Target: "/docs/a.md", Action: vfs.ChangeActionWrite, BeforeHash: beforeHash, AfterHash: hashContent(after)}},
	}
	if err := appendCommitRecord(historyPath, record); err != nil {
		t.Fatal(err)
	}
	cursor, err := OpenHistoryCursor(historyPath, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	processor := NewScalarProcessor(cursor, blobs, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer {
		return analytics.WriterHuman
	}))
	events := processor.Process(ctx, analytics.Event{ID: "write-1", Type: "doc.write", InvocationID: "invocation-1", Fields: map[string]any{"commit_id": "commit-1"}})
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.ParentID != "write-1" || event.InvocationID != "invocation-1" || event.Fields["action"] != "update" || event.Fields["writer"] != "human" {
		t.Fatalf("wrong correlation: %#v", event)
	}
	delta := event.Fields["delta"].(map[string]any)
	if delta["bytes"] != float64(8) || delta["lines"] != float64(1) || delta["words"] != float64(2) || delta["tokens"] != float64(2) {
		t.Fatalf("wrong delta: %#v", delta)
	}
}

type testScalarProvider struct{}

func (testScalarProvider) Name() string { return "custom" }
func (testScalarProvider) Scalars(_ string, b []byte) map[string]float64 {
	return map[string]float64{"custom": float64(len(b) * 10)}
}

func TestScalarProcessorUsesCustomProviders(t *testing.T) {
	record := CommitRecord{ID: "custom", ChangeSet: vfs.ChangeSet{Target: "/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("abc")}}, Leaves: []LeafRecord{{Target: "/a.md"}}}
	p := scalarProcessorForRecords(t, []CommitRecord{record}, testScalarProvider{})
	events := p.Process(context.Background(), writeEvent("write-custom", "custom"))
	after := events[0].Fields["after"].(map[string]float64)
	if after["custom"] != 30 || len(after) != 1 {
		t.Fatalf("custom scalars not used exclusively: %#v", after)
	}
}

func TestScalarProcessorProcessesHistoryBeforeCorrelatedTarget(t *testing.T) {
	records := []CommitRecord{
		scalarRecord("old", "/old.md", false),
		scalarRecord("new", "/new.md", false),
	}
	p := scalarProcessorForRecords(t, records)
	events := p.Process(context.Background(), writeEvent("write-new", "new"))
	if len(events) != 2 || events[0].Fields["commit_id"] != "old" || events[0].ParentID != "" || events[1].Fields["commit_id"] != "new" || events[1].ParentID != "write-new" {
		t.Fatalf("history was skipped or misattributed: %#v", events)
	}
}

type collectingSink struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (s *collectingSink) Record(_ context.Context, event analytics.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *collectingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestPipelineDrainsHistoryAndRestoresScalarCursor(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "commits.jsonl")
	for _, record := range []CommitRecord{scalarRecord("one", "/one.md", false), scalarRecord("two", "/two.md", false)} {
		if err := appendCommitRecord(historyPath, record); err != nil {
			t.Fatal(err)
		}
	}
	eventLog, err := analytics.OpenEventLog(t.TempDir(), analytics.LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "pipeline.checkpoint")
	newProcessor := func() analytics.Processor {
		cursor, err := OpenHistoryCursor(historyPath, HistoryPosition{})
		if err != nil {
			t.Fatal(err)
		}
		return NewScalarProcessor(cursor, nil, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer { return analytics.WriterAgent }))
	}
	firstSink := &collectingSink{}
	first := analytics.NewPipeline(eventLog, checkpoint, analytics.PipelineOptions{Processors: []analytics.Processor{newProcessor()}, Sink: firstSink})
	first.Run(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for firstSink.count() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if firstSink.count() != 2 {
		t.Fatalf("startup history events = %d, want 2", firstSink.count())
	}
	b, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Processors map[string]json.RawMessage `json:"processors"`
	}
	if json.Unmarshal(b, &state) != nil || len(state.Processors["doc-scalars"]) == 0 {
		t.Fatalf("checkpoint does not include history position: %s", b)
	}
	secondSink := &collectingSink{}
	second := analytics.NewPipeline(eventLog, checkpoint, analytics.PipelineOptions{Processors: []analytics.Processor{newProcessor()}, Sink: secondSink})
	second.Run(context.Background())
	time.Sleep(25 * time.Millisecond)
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if secondSink.count() != 0 {
		t.Fatalf("restart duplicated %d history events", secondSink.count())
	}
}

func TestScalarProcessorMultiLeafDocWritesAreIdempotent(t *testing.T) {
	multi := CommitRecord{ID: "multi", ChangeSet: vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("a")}},
		{Target: "/b.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("b")}},
	}}, Leaves: []LeafRecord{{Target: "/a.md"}, {Target: "/b.md"}}}
	p := scalarProcessorForRecords(t, []CommitRecord{multi, scalarRecord("later", "/later.md", false)})
	if got := p.Process(context.Background(), writeEvent("write-a", "multi")); len(got) != 2 {
		t.Fatalf("first event produced %d scalar events", len(got))
	}
	if got := p.Process(context.Background(), writeEvent("write-b", "multi")); len(got) != 0 {
		t.Fatalf("duplicate advanced history: %#v", got)
	}
	got := p.Process(context.Background(), writeEvent("write-later", "later"))
	if len(got) != 1 || got[0].Fields["commit_id"] != "later" || got[0].ParentID != "write-later" {
		t.Fatalf("later commit misattributed: %#v", got)
	}
}

func TestScalarProcessorReplayRewindsHistoryAndAppliesCutoff(t *testing.T) {
	cutoff := time.Now().UTC()
	records := []CommitRecord{
		scalarRecord("old", "/old.md", false),
		scalarRecord("new", "/new.md", false),
	}
	records[0].Time = cutoff.Add(-time.Hour)
	records[1].Time = cutoff.Add(time.Hour)
	p := scalarProcessorForRecords(t, records)
	if got := p.Drain(context.Background()); len(got) != 2 {
		t.Fatalf("initial drain = %d events", len(got))
	}
	if err := p.ResetForReplay(cutoff); err != nil {
		t.Fatal(err)
	}
	got := p.Drain(context.Background())
	if len(got) != 1 || got[0].Fields["commit_id"] != "new" {
		t.Fatalf("replay events = %#v", got)
	}
}

func TestScalarProcessorIgnoresMetadataLeaves(t *testing.T) {
	record := CommitRecord{ID: "metadata", Leaves: []LeafRecord{{Target: "/a.md", Action: vfs.ChangeActionSetXattr}}}
	if got := scalarProcessorForRecords(t, []CommitRecord{record}).Drain(context.Background()); len(got) != 0 {
		t.Fatalf("metadata produced scalar events: %#v", got)
	}
}

func TestScalarProcessorClassifiesCreateAndUnknownOverwrite(t *testing.T) {
	record := CommitRecord{ID: "actions", ChangeSet: vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/new.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new")}},
		{Target: "/existing.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("changed")}},
	}}, Leaves: []LeafRecord{
		{Target: "/new.md"},
		{Target: "/existing.md", BeforeExists: true, BeforeUnknown: true},
	}}
	events := scalarProcessorForRecords(t, []CommitRecord{record}).Process(context.Background(), writeEvent("write", "actions"))
	if events[0].Fields["action"] != "create" || events[0].Fields["first_seen"] != true || events[1].Fields["action"] != "update" || events[1].Fields["first_seen"] != true {
		t.Fatalf("actions/first_seen = %#v, %#v", events[0].Fields, events[1].Fields)
	}
}

func scalarRecord(id, target string, existed bool) CommitRecord {
	b := []byte(id)
	return CommitRecord{ID: id, ChangeSet: vfs.ChangeSet{Target: target, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: b}}, Leaves: []LeafRecord{{Target: target, BeforeExists: existed}}}
}

func writeEvent(id, commit string) analytics.Event {
	return analytics.Event{ID: id, Type: "doc.write", InvocationID: id + "-invocation", Fields: map[string]any{"commit_id": commit}}
}

func scalarProcessorForRecords(t *testing.T, records []CommitRecord, providers ...analytics.ContentScalarProvider) *ScalarProcessor {
	t.Helper()
	file := filepath.Join(t.TempDir(), "commits.jsonl")
	for _, record := range records {
		if err := appendCommitRecord(file, record); err != nil {
			t.Fatal(err)
		}
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	return NewScalarProcessor(cursor, nil, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer { return analytics.WriterAgent }), providers...).(*ScalarProcessor)
}
