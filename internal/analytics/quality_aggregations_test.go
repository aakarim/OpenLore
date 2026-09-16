package analytics

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func appendEvents(t *testing.T, log EventLog, events ...Event) {
	t.Helper()
	for _, event := range events {
		if err := log.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSearchQueryAggregations(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	appendEvents(t, log,
		Event{Time: now.Add(-time.Minute), Type: "search.query", Principal: "one", Fields: map[string]any{"pattern": "  auth  ", "filled": true}},
		Event{Time: now, Type: "search.query", Principal: "two", Fields: map[string]any{"pattern": "auth", "filled": false}},
		Event{Time: now.Add(-time.Hour), Type: "search.query", Principal: "one", Fields: map[string]any{"pattern": "billing", "filled": false}},
	)
	queries, err := TopSearchQueries(context.Background(), log, EventFilter{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0].Pattern != "auth" || queries[0].Count != 2 || queries[0].FilledRatio != .5 || queries[0].Principals != 2 {
		t.Fatalf("queries = %#v", queries)
	}
	filled := false
	unfilled, err := TopSearchQueries(context.Background(), log, EventFilter{}, &filled, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfilled) != 2 || unfilled[0].FilledRatio != 0 {
		t.Fatalf("unfilled = %#v", unfilled)
	}
}

func TestFileAndFolderUsageIncludeNeverReadInventory(t *testing.T) {
	facts := NewContentFacts(testFS{
		"/docs/read.md":       []byte("read"),
		"/docs/sub/hit.md":    []byte("hit"),
		"/docs/sub/unused.md": []byte("unused"),
	})
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	old, recent := time.Now().Add(-time.Hour).UTC(), time.Now().UTC()
	appendEvents(t, log,
		Event{Time: old, Type: "doc.read", Fields: map[string]any{"path": "/docs/read.md", "content_hash": "read-hash"}},
		Event{Time: recent, Type: "doc.hit", Fields: map[string]any{"path": "/docs/sub/hit.md", "content_hash": "hit-hash"}},
		Event{Time: recent, Type: "doc.scalars", Fields: map[string]any{"path": "/docs/read.md", "after": map[string]any{"tokens": float64(2000)}}},
	)
	table, err := fileUsageTable("asc")(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 3 || table.Rows[0][0] != "/docs/sub/unused.md" || table.Rows[1][0] != "/docs/read.md" || table.Rows[1][4] != float64(2000) || table.Rows[1][5] != .5 || table.Rows[2][0] != "/docs/sub/hit.md" {
		t.Fatalf("file usage rows = %#v", table.Rows)
	}
	folders, err := leastUsedFolders(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs", "depth": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(folders.Rows) != 2 || folders.Rows[0][0] != "/docs" || folders.Rows[0][2] != 3 || folders.Rows[0][3] != 1 || folders.Rows[1][0] != "/docs/sub" || folders.Rows[1][2] != 2 || folders.Rows[1][3] != 1 {
		t.Fatalf("folder usage rows = %#v", folders.Rows)
	}
}

func TestLeastUsedLinesIgnoresStaleContentHashes(t *testing.T) {
	facts := NewContentFacts(testFS{"/docs/a.md": []byte("one\ntwo\nthree\nfour\nfive\n")})
	current, err := facts.Stat(context.Background(), "/docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	appendEvents(t, log,
		Event{Time: now, Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 2, "end": 3}}}},
		Event{Time: now.Add(time.Minute), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": "stale", "unit": map[string]any{}}},
		Event{Time: now.Add(2 * time.Minute), Type: "doc.scalars", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "after": map[string]any{"lines": float64(5), "tokens": float64(10)}}},
		Event{Time: now.Add(-time.Minute), Type: "doc.hit", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 2, "end": 3}}}},
	)
	table, err := leastUsedLines(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs/a.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 3 || table.Rows[0][1] != 1 || table.Rows[0][2] != 1 || table.Rows[0][4] != 0 || table.Rows[1][1] != 4 || table.Rows[1][2] != 5 || table.Rows[1][4] != 0 || table.Rows[2][1] != 2 || table.Rows[2][2] != 3 || table.Rows[2][4] != 2 {
		t.Fatalf("line usage rows = %#v", table.Rows)
	}
	if last, ok := table.Rows[2][3].(*time.Time); !ok || !last.Equal(now) {
		t.Fatalf("last read = %#v, want %s", table.Rows[2][3], now)
	}
	usage, err := FileUsage(context.Background(), log, facts, "/docs", EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || len(usage[0].ColdUnits) != 2 || *usage[0].ColdUnits[0].Lines != (LineRange{Start: 1, End: 1}) || *usage[0].ColdUnits[1].Lines != (LineRange{Start: 4, End: 5}) {
		t.Fatalf("cold units = %#v", usage)
	}
}

func TestMostUsedLinesSortsCurrentRevisionByReadCount(t *testing.T) {
	facts := NewContentFacts(testFS{"/docs/a.md": []byte("one\ntwo\nthree\nfour\n")})
	current, err := facts.Stat(context.Background(), "/docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := []Event{
		{Time: now, Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 2, "end": 3}}}},
		{Time: now.Add(time.Minute), Type: "doc.hit", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 2, "end": 2}}}},
		{Time: now.Add(2 * time.Minute), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 2, "end": 2}}}},
		{Time: now, Type: "doc.hit", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 4, "end": 4}}}},
		{Time: now.Add(time.Minute), Type: "doc.hit", Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "unit": map[string]any{"lines": map[string]any{"start": 4, "end": 4}}}},
	}
	// A heavily read prior revision must not affect current-line rankings.
	for i := 0; i < 10; i++ {
		events = append(events, Event{Time: now.Add(time.Duration(i) * time.Minute), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": "prior-revision", "unit": map[string]any{}}})
	}
	appendEvents(t, log, events...)
	table, err := mostUsedLines(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs/a.md"}})
	if err != nil {
		t.Fatal(err)
	}
	wantColumns := []string{"path", "start", "end", "last_read_at", "reads"}
	if len(table.Columns) != len(wantColumns) {
		t.Fatalf("columns = %#v", table.Columns)
	}
	for i := range wantColumns {
		if table.Columns[i] != wantColumns[i] {
			t.Fatalf("columns = %#v", table.Columns)
		}
	}
	if len(table.Rows) != 4 || table.Rows[0][1] != 2 || table.Rows[0][4] != 3 || table.Rows[1][1] != 4 || table.Rows[1][4] != 2 || table.Rows[2][1] != 3 || table.Rows[2][4] != 1 || table.Rows[3][1] != 1 || table.Rows[3][4] != 0 {
		t.Fatalf("most-used line rows = %#v", table.Rows)
	}
}

func TestLeastUsedLinesRejectsDirectory(t *testing.T) {
	facts := NewContentFacts(testFS{"/docs/a.md": []byte("one\n")})
	if _, err := leastUsedLines(context.Background(), nil, facts, Params{Extra: map[string]string{"path": "/docs"}}); err == nil {
		t.Fatal("directory path was accepted")
	}
}

func TestUsedLinesBoundsNewlineHeavyFiles(t *testing.T) {
	log, err := OpenEventLog(t.TempDir(), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	facts := NewContentFacts(testFS{
		"/at-limit":   bytes.Repeat([]byte{'\n'}, 100_000),
		"/over-limit": bytes.Repeat([]byte{'\n'}, 100_001),
	})
	for _, most := range []bool{false, true} {
		table, err := usedLines(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/at-limit"}}, most)
		if err != nil {
			t.Fatal(err)
		}
		if table.Total != 1 || len(table.Rows) != 1 || table.Rows[0][1] != 1 || table.Rows[0][2] != 100_000 || table.Rows[0][4] != 0 {
			t.Fatalf("most=%v: expected complete at-limit range, got %#v", most, table)
		}
		// A nil source also proves rejection happens before any event scan.
		table, err = usedLines(context.Background(), nil, facts, Params{Limit: 1, Extra: map[string]string{"path": "/over-limit"}}, most)
		if err == nil || !strings.Contains(err.Error(), "exceeding 100000 lines") || len(table.Rows) != 0 {
			t.Fatalf("most=%v: expected explicit rejection, not truncation: %#v, %v", most, table, err)
		}
	}
}

func TestPhaseTwoAggregationsAreRegisteredAsLive(t *testing.T) {
	registry := NewRegistry(nil, func() []string { return []string{"search.query", "doc.read", "doc.hit"} })
	registry.Bind(nil, NewContentFacts(testFS{"/a.md": []byte("a")}))
	for _, aggregation := range BuiltinAggregations() {
		if err := registry.Register(aggregation); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"top-search-queries", "top-unfilled-queries", "least-used-files", "most-used-files", "least-used-folders", "least-used-lines", "most-used-lines"} {
		if got := registry.Status(name); got != StatusOK {
			t.Errorf("%s status = %s, want ok", name, got)
		}
	}
}
