package analytics

import (
	"context"
	"encoding/json"
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
	a.Consume(context.Background(), Event{Type: "command.exec", Transport: "ssh", InvocationID: "invoke-1", Fields: map[string]any{"command": "cat", "exit_code": 0, "duration_ms": 1500}})
	a.Consume(context.Background(), Event{Type: "command.exec", Transport: "ssh", Fields: map[string]any{"command": "cat", "exit_code": 0, "duration_ms": 500}})
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rr.Body.String(), `openlore_commands_total{command="cat",transport="ssh",exit_class="success"} 2`) ||
		!strings.Contains(rr.Body.String(), `openlore_command_duration_seconds_sum{command="cat"} 2`) ||
		!strings.Contains(rr.Body.String(), `# {invocation_id="invoke-1"} 1.5`) ||
		strings.Contains(rr.Body.String(), `command="cat",invocation_id=`) ||
		a.durations["cat"].count != 2 {
		t.Fatal(rr.Body.String())
	}
}

func TestAggregatorDeduplicatesReplayedScalars(t *testing.T) {
	a := NewAggregator(AggregatorOptions{})
	e := Event{ID: "first", Type: "doc.scalars", Fields: map[string]any{"commit_id": "commit", "path": "/a.md", "docset": "docs", "writer": "human", "delta": map[string]any{"lines": float64(-2)}}}
	a.Consume(context.Background(), e)
	e.ID = "replayed"
	a.Consume(context.Background(), e)
	if got := a.scalars["docs\x00human\x00lines"]; got != -2 {
		t.Fatalf("scalar delta = %v, want -2", got)
	}
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rr.Body.String(), "# TYPE openlore_doc_write_scalar_total gauge") {
		t.Fatal(rr.Body.String())
	}
}

type depthCapturingFacts struct{ depth int }

func (*depthCapturingFacts) Stat(context.Context, string) (DocScalars, error) {
	return DocScalars{}, nil
}
func (f *depthCapturingFacts) Walk(_ context.Context, _ string, opts WalkOptions, _ func(DocScalars) error) error {
	f.depth = opts.Depth
	return nil
}

func TestTreeSizePassesDepthToContentWalk(t *testing.T) {
	facts := &depthCapturingFacts{}
	if _, err := treeSize(context.Background(), nil, facts, Params{Extra: map[string]string{"depth": "3"}}); err != nil {
		t.Fatal(err)
	}
	if facts.depth != 3 {
		t.Fatalf("walk depth = %d, want 3", facts.depth)
	}
}

func TestLimitRowsAppliesPageOffsetBeforeLimit(t *testing.T) {
	rows := [][]any{{"first"}, {"second"}, {"third"}, {"fourth"}}
	got := limitRows(rows, Params{Limit: 2, Extra: map[string]string{"_offset": "2"}})
	if len(got) != 2 || got[0][0] != "third" || got[1][0] != "fourth" {
		t.Fatalf("page = %#v", got)
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
		if got, err := os.ReadFile(checkpoint); err == nil {
			var state pipelineCheckpoint
			if json.Unmarshal(got, &state) == nil && state.EventID == want || strings.TrimSpace(string(got)) == want {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("checkpoint %q was not written", want)
}
