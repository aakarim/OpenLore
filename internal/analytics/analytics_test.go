package analytics

import (
	"context"
	"io/fs"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type countingProcessor struct{ calls int }

func (p *countingProcessor) Name() string { return "counting" }
func (p *countingProcessor) Process(_ context.Context, e Event) []Event {
	if e.Type != "source" {
		return nil
	}
	p.calls++
	return []Event{{ID: e.ID + "-derived", Type: "derived"}}
}

type testFS map[string][]byte

func (f testFS) Stat(p string) (*vfs.FileInfo, error) {
	p = vfs.CleanPath(p)
	if b, ok := f[p]; ok {
		return &vfs.FileInfo{FileName: path.Base(p), FilePath: p, FileSize: int64(len(b))}, nil
	}
	for name := range f {
		if strings.HasPrefix(name, strings.TrimSuffix(p, "/")+"/") {
			return &vfs.FileInfo{FileName: path.Base(p), FilePath: p, Dir: true}, nil
		}
	}
	return nil, fs.ErrNotExist
}
func (f testFS) ReadFile(p string) ([]byte, error) {
	b, ok := f[vfs.CleanPath(p)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f testFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	p = strings.TrimSuffix(vfs.CleanPath(p), "/") + "/"
	seen := map[string]bool{}
	var out []vfs.FileInfo
	for name, b := range f {
		if !strings.HasPrefix(name, p) {
			continue
		}
		rest := strings.TrimPrefix(name, p)
		part := strings.Split(rest, "/")[0]
		if seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, vfs.FileInfo{FileName: part, Dir: strings.Contains(rest, "/"), FileSize: int64(len(b))})
	}
	return out, nil
}

func TestRecorderPersistsAndDrains(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(log, 2)
	r.Start(context.Background())
	r.Record(context.Background(), Event{Type: "command.exec", Fields: map[string]any{"command": "cat"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var got []Event
	if err := log.Scan(context.Background(), EventFilter{}, func(e Event) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID == "" || got[0].Type != "command.exec" {
		t.Fatalf("unexpected events: %#v", got)
	}
}

func TestFactsAndTopCommands(t *testing.T) {
	facts := NewContentFacts(testFS{"/docs/a.md": []byte("one two\nthree")})
	d, err := facts.Stat(context.Background(), "/docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.Scalars["bytes"] != 13 || d.Scalars["lines"] != 2 || d.Scalars["words"] != 3 || d.Scalars["tokens"] != 4 {
		t.Fatalf("wrong facts: %#v", d.Scalars)
	}
	log, _ := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	for _, e := range []Event{{Type: "command.exec", Principal: "adil", SessionID: "s1", Transport: "ssh", Fields: map[string]any{"command": "cat", "exit_code": 0, "duration_ms": 9}}, {Type: "command.exec", Principal: "agent", SessionID: "s2", Transport: "mcp", Fields: map[string]any{"command": "cat", "exit_code": 1, "duration_ms": 3}}} {
		if err := log.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	table, err := topCommands(context.Background(), log, nil, Params{})
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 1 || table.Rows[0][1] != 2 || table.Rows[0][2] != 2 || table.Rows[0][4] != .5 {
		t.Fatalf("wrong rollup: %#v", table.Rows)
	}
}

func TestPrometheusExposition(t *testing.T) {
	a := NewAggregator(AggregatorOptions{})
	a.Consume(context.Background(), Event{Type: "command.exec", Transport: "ssh", Fields: map[string]any{"command": "cat", "exit_code": 0}})
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rr.Body.String(), `openlore_commands_total{command="cat",transport="ssh",exit_class="success"} 1`) {
		t.Fatal(rr.Body.String())
	}
}

func TestPipelineResumesAfterCheckpointWithoutRederivingEvents(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(context.Background(), Event{ID: "event-1", Type: "source"}); err != nil {
		t.Fatal(err)
	}
	checkpoint := path.Join(t.TempDir(), "pipeline.checkpoint")
	firstProcessor := &countingProcessor{}
	first := NewPipeline(log, checkpoint, PipelineOptions{Processors: []Processor{firstProcessor}, Sink: sinkFunc(func(ctx context.Context, e Event) { _ = log.Append(ctx, e) })})
	first.Run(context.Background())
	waitForCheckpoint(t, checkpoint, "event-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if firstProcessor.calls != 1 {
		t.Fatalf("first processor calls = %d", firstProcessor.calls)
	}

	secondProcessor := &countingProcessor{}
	second := NewPipeline(log, checkpoint, PipelineOptions{Processors: []Processor{secondProcessor}})
	second.Run(context.Background())
	waitForCheckpoint(t, checkpoint, "event-1-derived")
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if secondProcessor.calls != 0 {
		t.Fatalf("checkpointed events were rederived: calls = %d", secondProcessor.calls)
	}
}

func waitForCheckpoint(t *testing.T, checkpoint, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := os.ReadFile(checkpoint); err == nil && strings.TrimSpace(string(got)) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("checkpoint %q was not written", want)
}
