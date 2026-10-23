package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"hegel.dev/go/hegel"
)

// builtin returns the Compute of a built-in aggregation.
func builtin(name string) func(context.Context, EventSource, ContentFacts, Params) (Table, error) {
	for _, aggregation := range BuiltinAggregations() {
		if aggregation.Name == name {
			return aggregation.Compute
		}
	}
	panic("unknown aggregation " + name)
}

func lineAggregation(most bool) string {
	if most {
		return "most-used-lines"
	}
	return "least-used-lines"
}

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
	table, err := builtin("least-used-files")(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 3 || table.Rows[0][0] != "/docs/sub/unused.md" || table.Rows[1][0] != "/docs/read.md" || table.Rows[1][4] != float64(2000) || table.Rows[1][5] != .5 || table.Rows[2][0] != "/docs/sub/hit.md" {
		t.Fatalf("file usage rows = %#v", table.Rows)
	}
	folders, err := builtin("least-used-folders")(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs", "depth": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(folders.Rows) != 2 || folders.Rows[0][0] != "/docs" || folders.Rows[0][2] != 3 || folders.Rows[0][3] != 1 || folders.Rows[1][0] != "/docs/sub" || folders.Rows[1][2] != 2 || folders.Rows[1][3] != 1 {
		t.Fatalf("folder usage rows = %#v", folders.Rows)
	}
}

func TestFileUsageRankingsFallBackToCurrentTokens(t *testing.T) {
	service := newIndexedTestService(t, testFS{
		"/docs/read.md":   []byte("abcdefgh"),
		"/docs/unused.md": []byte("abcdefghijkl"),
		"/docs/empty.md":  {},
		"/docs/event.md":  []byte("abcd"),
	})
	facts := service.Facts()
	// Populate the same current-facts cache used by tree-size and largest-docs.
	if _, err := builtin("largest-docs")(t.Context(), nil, facts, Params{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	source := summarySource{
		{Time: now.Add(-48 * time.Hour), Type: "doc.scalars", Fields: map[string]any{"path": "/docs/read.md", "after": map[string]any{"tokens": float64(99)}}},
		{Time: now, Type: "doc.read", Fields: map[string]any{"path": "/docs/read.md"}},
		// A scalar observation without tokens must also fall back to facts.
		{Time: now, Type: "doc.scalars", Fields: map[string]any{"path": "/docs/empty.md", "after": map[string]any{"bytes": float64(0)}}},
		{Time: now, Type: "doc.scalars", Fields: map[string]any{"path": "/docs/event.md", "after": map[string]any{"tokens": float64(2000)}}},
	}
	for _, name := range []string{"least-used-files", "most-used-files"} {
		t.Run(name, func(t *testing.T) {
			table, err := builtin(name)(t.Context(), source, facts, Params{Since: now.Add(-24 * time.Hour), Until: now, Extra: map[string]string{"path": "/docs"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(table.Rows) != 4 {
				t.Fatalf("rows = %#v", table.Rows)
			}
			for _, row := range table.Rows {
				var tokens float64
				var rate any
				switch row[0] {
				case "/docs/read.md":
					tokens, rate = 2, float64(500)
				case "/docs/unused.md":
					tokens, rate = 3, float64(0)
				case "/docs/empty.md":
					tokens = 0 // Empty files have tokens, but no per-token ratio.
				case "/docs/event.md":
					tokens, rate = 2000, float64(0) // Preserve in-window observations.
				default:
					t.Fatalf("unexpected row = %#v", row)
				}
				if row[4] != tokens || row[5] != rate {
					t.Errorf("%s: tokens=%v rate=%v, want %v %v", row[0], row[4], row[5], tokens, rate)
				}
			}
		})
	}
}

func TestFileUsageRankingsRetainUnreadableInventory(t *testing.T) {
	facts := NewContentFacts(&controlledFactsFS{
		testFS: testFS{
			"/docs/a.md": []byte("readable"),
			"/docs/b.md": []byte("unreadable"),
		},
		failPath: "/docs/b.md",
	})
	for _, name := range []string{"least-used-files", "most-used-files"} {
		for _, limit := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/limit=%d", name, limit), func(t *testing.T) {
				table, err := builtin(name)(t.Context(), summarySource{}, facts, Params{Limit: limit})
				if err != nil {
					t.Fatal(err)
				}
				if table.Total != 2 || table.Rows[0][0] != "/docs/a.md" || table.Rows[0][4] != float64(2) {
					t.Fatalf("table = %#v", table)
				}
				if limit == 1 {
					if len(table.Rows) != 1 || table.Note != "" {
						t.Fatalf("limited table = %#v", table)
					}
				} else if len(table.Rows) != 2 || table.Rows[1][0] != "/docs/b.md" || table.Rows[1][4] != nil || table.Rows[1][5] != nil || table.Note != "Token counts unavailable for unreadable files, first: /docs/b.md" {
					t.Fatalf("unreadable inventory = %#v", table)
				}
			})
		}
	}
}

func TestFileUsageRankingsDoNotAllocateLineCoverage(t *testing.T) {
	files := testFS{}
	for i := range 3500 {
		files[fmt.Sprintf("/docs/%04d.md", i)] = []byte("content")
	}
	now := time.Now().UTC()
	source := summarySource{
		{Time: now.Add(-time.Hour), Type: "doc.read", Fields: map[string]any{"path": "/docs/0000.md", "content_hash": "old", "unit": map[string]any{"lines": map[string]any{"start": 2, "end": 3}}}},
		{Time: now, Type: "doc.hit", Fields: map[string]any{"path": "/docs/0001.md", "content_hash": "new"}},
		// Rankings need tokens, not a bitmap of every line in a document.
		{Time: now, Type: "doc.scalars", Fields: map[string]any{"path": "/docs/0000.md", "content_hash": "new", "after": map[string]any{"lines": float64(1 << 60), "tokens": float64(2000)}}},
	}
	for _, name := range []string{"least-used-files", "most-used-files", "least-used-folders"} {
		t.Run(name, func(t *testing.T) {
			table, err := builtin(name)(t.Context(), source, NewContentFacts(files), Params{Since: now.Add(-7 * 24 * time.Hour), Until: now, Limit: 0, Extra: map[string]string{"path": "/docs"}})
			if err != nil {
				t.Fatal(err)
			}
			if name == "least-used-folders" {
				if len(table.Rows) != 1 || table.Rows[0][2] != 3500 || table.Rows[0][3] != 3498 {
					t.Fatalf("folder rows = %#v", table.Rows)
				}
				return
			}
			if table.Total != 3500 || len(table.Rows) != 3500 {
				t.Fatalf("got %d rows, total %d", len(table.Rows), table.Total)
			}
			index := 3498
			if name == "most-used-files" {
				index = 1
			}
			row := table.Rows[index]
			if row[0] != "/docs/0000.md" || row[2] != 1 || row[4] != float64(2000) || row[5] != .5 {
				t.Fatalf("read row = %#v", row)
			}
		})
	}
}

func TestFileUsageRankingsEnforceBudget(t *testing.T) {
	for _, inventory := range []bool{false, true} {
		t.Run(fmt.Sprintf("inventory=%t", inventory), func(t *testing.T) {
			files := testFS{"/docs/keep.md": []byte("keep")}
			var events summarySource
			count := maxFileUsagePaths
			if inventory {
				count-- // keep.md also counts toward the inventory budget.
			}
			for i := range count {
				path := fmt.Sprintf("/archive/%05d.md", i)
				if inventory {
					files[path] = []byte("archived")
				} else {
					events = append(events, Event{Type: "doc.read", Fields: map[string]any{"path": path}})
				}
			}
			p := Params{Extra: map[string]string{"path": "/"}}
			if _, err := builtin("least-used-files")(t.Context(), events, NewContentFacts(files), p); err != nil {
				t.Fatalf("at budget: %v", err)
			}
			if inventory {
				files["/extra.md"] = []byte("extra")
			} else {
				events = append(events, Event{Type: "doc.read", Fields: map[string]any{"path": "/extra.md"}})
			}
			if _, err := builtin("least-used-files")(t.Context(), events, NewContentFacts(files), p); err == nil || !strings.Contains(err.Error(), "narrow --param path or --since") {
				t.Fatalf("over budget: %v", err)
			}
			p.Extra["path"] = "/docs"
			table, err := builtin("least-used-files")(t.Context(), events, NewContentFacts(files), p)
			if err != nil || len(table.Rows) != 1 || table.Rows[0][0] != "/docs/keep.md" {
				t.Fatalf("narrowed query: table=%+v err=%v", table, err)
			}
		})
	}
}

func TestFileUsageRankingsPreserveBudgetErrorsAcrossCachedMerges(t *testing.T) {
	inc := builtinIncremental("least-used-files")
	p := Params{Extra: map[string]string{"path": "/"}}
	for _, budget := range []struct {
		name      string
		paths     int
		pathBytes int
	}{
		{"path-count", maxFileUsagePaths, 32},
		{"path-bytes", 128, maxFileUsagePathBytes / 128},
	} {
		t.Run(budget.name, func(t *testing.T) {
			path := func(i int) string {
				return fmt.Sprintf("/archive/%0*d.md", budget.pathBytes-len("/archive/.md"), i)
			}
			facts := NewContentFacts(testFS{path(0): []byte("keep")})
			build := func(start, end int) Partial {
				partial := inc.New(p)
				for i := start; i < end; i++ {
					partial.Add(Event{Type: "doc.read", Fields: map[string]any{"path": path(i)}})
				}
				return partial
			}
			cached := func(t *testing.T, partial Partial) Partial {
				t.Helper()
				encoded, err := json.Marshal(partial)
				if err != nil {
					t.Fatal(err)
				}
				decoded := inc.New(p)
				if err := json.Unmarshal(encoded, decoded); err != nil {
					t.Fatal(err)
				}
				return decoded
			}
			for _, scenario := range []string{"combined", "overflowed-receiver", "overflowed-later"} {
				t.Run(scenario, func(t *testing.T) {
					var receiver, later Partial
					if scenario == "combined" {
						receiver = cached(t, build(0, budget.paths-1))
						later = cached(t, build(budget.paths-1, budget.paths+1))
						// Both daily partials fit independently. A single added
						// path reaches the boundary and must still produce rows.
						for _, partial := range []Partial{receiver, later} {
							if _, err := inc.Table(t.Context(), partial, facts, p); err != nil {
								t.Fatalf("individually valid partial: %v", err)
							}
						}
						boundary := cached(t, receiver)
						boundary.Merge(cached(t, build(budget.paths-1, budget.paths)))
						table, err := inc.Table(t.Context(), boundary, facts, p)
						if err != nil || len(table.Rows) != 1 || table.Rows[0][0] != path(0) || table.Rows[0][2] != 1 {
							t.Fatalf("at budget: rows=%d err=%v", len(table.Rows), err)
						}
					} else {
						// The overflowed partial retains only the paths that fit.
						// Losing its overflow marker would silently return rows.
						receiver = cached(t, build(0, budget.paths+1))
						later = cached(t, build(0, 1))
						if scenario == "overflowed-later" {
							receiver, later = later, receiver
						}
					}
					receiver.Merge(later)
					table, err := inc.Table(t.Context(), cached(t, receiver), facts, p)
					if err == nil || !strings.Contains(err.Error(), "memory budget") || len(table.Rows) != 0 {
						t.Fatalf("expected a budget error, not truncated rows: rows=%d err=%v", len(table.Rows), err)
					}
				})
			}
		})
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
	table, err := builtin("least-used-lines")(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs/a.md"}})
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
	table, err := builtin("most-used-lines")(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/docs/a.md"}})
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

func TestCurrentRevisionLineUsageProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("Hegel property tests are excluded in short mode")
	}
	hegel.Test(t, func(t *hegel.T) {
		ctx := context.Background()
		lineCount := hegel.Draw(t, hegel.Integers(1, 24))
		facts := NewContentFacts(testFS{"/docs/a.md": []byte(strings.Repeat("line\n", lineCount))})
		current, err := facts.Stat(ctx, "/docs/a.md")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		// FileUsage uses recorded scalar observations for line coverage.
		events := summarySource{{Type: "doc.scalars", Time: start, Fields: map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash, "after": map[string]any{"lines": float64(lineCount)}}}}
		counts := make([]int, lineCount)
		lastRead := make([]time.Time, lineCount)
		for i := range hegel.Draw(t, hegel.Integers(0, 50)) {
			first := hegel.Draw(t, hegel.Integers(1, lineCount+3))
			last := hegel.Draw(t, hegel.Integers(first, lineCount+5))
			whole := hegel.Draw(t, hegel.Integers(0, 4)) == 0
			stale := hegel.Draw(t, hegel.Booleans())
			otherFile := hegel.Draw(t, hegel.Integers(0, 4)) == 0
			at := start.Add(time.Duration(hegel.Draw(t, hegel.Integers(0, 20))) * time.Minute)
			fields := map[string]any{"path": "/docs/a.md", "content_hash": current.ContentHash}
			if stale {
				fields["content_hash"] = "old-revision"
			}
			if otherFile {
				fields["path"] = "/docs/other.md"
			}
			if !whole {
				fields["unit"] = map[string]any{"lines": map[string]any{"start": first, "end": last}}
			}
			eventType := hegel.Draw(t, hegel.SampledFrom([]string{"doc.read", "doc.hit"}))
			events = append(events, Event{ID: fmt.Sprint(i), Type: eventType, Time: at, Fields: fields})
			// A per-line oracle, independent of range coalescing and partials.
			for line := 1; line <= lineCount; line++ {
				if !stale && !otherFile && (whole || first <= line && line <= last) {
					counts[line-1]++
					if at.After(lastRead[line-1]) {
						lastRead[line-1] = at
					}
				}
			}
		}
		for _, name := range []string{"least-used-lines", "most-used-lines"} {
			table, err := builtin(name)(ctx, events, facts, Params{Extra: map[string]string{"path": "/docs/a.md"}})
			if err != nil {
				t.Fatal(err)
			}
			if table.Total != len(table.Rows) {
				t.Fatalf("%s total=%d rows=%d", name, table.Total, len(table.Rows))
			}
			seen := make([]bool, lineCount)
			for _, row := range table.Rows {
				first, last := row[1].(int), row[2].(int)
				if row[0] != "/docs/a.md" || first < 1 || last > lineCount || first > last {
					t.Fatalf("%s invalid range: %v", name, row)
				}
				at := time.Time{}
				if recorded, _ := row[3].(*time.Time); recorded != nil {
					at = *recorded
				}
				for line := first; line <= last; line++ {
					if seen[line-1] || row[4] != counts[line-1] || !at.Equal(lastRead[line-1]) {
						t.Fatalf("%s line %d: row=%v want count=%d last=%v seen=%v events=%+v", name, line, row, counts[line-1], lastRead[line-1], seen[line-1], events)
					}
					seen[line-1] = true
				}
			}
			if slices.Contains(seen, false) {
				t.Fatalf("%s omitted lines: %v", name, seen)
			}
		}
		usage, err := FileUsage(ctx, events, facts, "/docs", EventFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(usage) != 1 || usage[0].Path != "/docs/a.md" || usage[0].ContentHash != current.ContentHash {
			t.Fatalf("unexpected file usage: %+v", usage)
		}
		cold := make([]bool, lineCount)
		for _, unit := range usage[0].ColdUnits {
			if unit.Lines == nil || unit.Lines.Start < 1 || unit.Lines.End > lineCount || unit.Lines.Start > unit.Lines.End {
				t.Fatalf("invalid cold unit: %+v", unit)
			}
			for line := unit.Lines.Start; line <= unit.Lines.End; line++ {
				if cold[line-1] {
					t.Fatalf("overlapping cold ranges at line %d", line)
				}
				cold[line-1] = true
			}
		}
		for line, count := range counts {
			if cold[line] != (count == 0) {
				t.Fatalf("line %d: cold=%v count=%d events=%+v", line+1, cold[line], count, events)
			}
		}
	}, hegel.WithTestCases(1000))
}

func TestLeastUsedLinesRejectsDirectory(t *testing.T) {
	facts := NewContentFacts(testFS{"/docs/a.md": []byte("one\n")})
	if _, err := builtin("least-used-lines")(context.Background(), nil, facts, Params{Extra: map[string]string{"path": "/docs"}}); err == nil {
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
		table, err := builtin(lineAggregation(most))(context.Background(), log, facts, Params{Extra: map[string]string{"path": "/at-limit"}})
		if err != nil {
			t.Fatal(err)
		}
		if table.Total != 1 || len(table.Rows) != 1 || table.Rows[0][1] != 1 || table.Rows[0][2] != 100_000 || table.Rows[0][4] != 0 {
			t.Fatalf("most=%v: expected complete at-limit range, got %#v", most, table)
		}
		// A nil source also proves rejection happens before any event scan.
		table, err = builtin(lineAggregation(most))(context.Background(), nil, facts, Params{Limit: 1, Extra: map[string]string{"path": "/over-limit"}})
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

// Every incremental aggregation must give the same table from merged,
// JSON round-tripped partials of consecutive ranges as from one scan.
func TestIncrementalAggregationsMergeExactly(t *testing.T) {
	if testing.Short() {
		t.Skip("Hegel property tests are excluded in short mode")
	}
	files := testFS{"/docs/a.md": []byte("one\ntwo\nthree\nfour\nfive\n"), "/docs/sub/b.md": []byte("b\n"), "/docs/c.md": []byte("c")}
	facts := NewContentFacts(files)
	current, err := facts.Stat(context.Background(), "/docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, aggregation := range BuiltinAggregations() {
		inc := aggregation.Incremental
		if inc == nil {
			continue
		}
		t.Run(aggregation.Name, func(t *testing.T) {
			hegel.Test(t, func(t *hegel.T) {
				pick := func(values ...string) string { return hegel.Draw(t, hegel.SampledFrom(values)) }
				first := hegel.Draw(t, hegel.Integers(0, 60))
				last := hegel.Draw(t, hegel.Integers(3*1440-60, 3*1440))
				p := Params{Since: start.Add(time.Duration(first) * time.Minute), Until: start.Add(time.Duration(last) * time.Minute), Limit: hegel.Draw(t, hegel.Integers(0, 4)), Extra: map[string]string{"path": pick("/", "/docs", "/docs/sub"), "transport": pick("", "ssh", "mcp"), "order": pick("asc", "desc"), "depth": pick("0", "1", "2")}}
				if strings.HasSuffix(aggregation.Name, "-lines") {
					p.Extra["path"] = "/docs/a.md"
				}
				boundaries := []time.Time{p.Since, start.Add(24 * time.Hour), start.Add(48 * time.Hour), p.Until.Add(time.Nanosecond)}
				for _, minute := range hegel.Draw(t, hegel.Lists(hegel.Integers(first, last)).MaxSize(5)) {
					boundaries = append(boundaries, start.Add(time.Duration(minute)*time.Minute))
				}
				slices.SortFunc(boundaries, time.Time.Compare)
				boundaries = slices.Compact(boundaries)
				eventGen := hegel.Composite(func(tc hegel.TestCase) Event {
					pick := func(values ...string) string { return hegel.Draw(tc, hegel.SampledFrom(values)) }
					at := start.Add(time.Duration(hegel.Draw(tc, hegel.Integers(-1, 3*1440+1))) * time.Minute)
					if hegel.Draw(tc, hegel.Booleans()) {
						at = hegel.Draw(tc, hegel.SampledFrom(boundaries)).Add(time.Duration(hegel.Draw(tc, hegel.Integers(-1, 1))))
					}
					e := Event{Time: at, Principal: pick("ann", "bob", "cy"), SessionID: pick("s1", "s2", "s3", ""), Transport: pick("ssh", "mcp")}
					switch hegel.Draw(tc, hegel.Integers(0, 6)) {
					case 0:
						e.Type, e.Fields = "command.exec", map[string]any{"command": pick("cat", "grep", "ls"), "exit_code": hegel.Draw(tc, hegel.Integers(0, 2)), "duration_ms": hegel.Draw(tc, hegel.Integers(0, 1000))}
					case 1:
						e.Type, e.Fields = pick("command.unknown", "syntax.unknown"), map[string]any{"command": pick("vim", ""), "syntax": "<<"}
					case 2:
						e.Type = "session.start"
					case 3:
						e.Type, e.Fields = "search.query", map[string]any{"pattern": pick("auth", " auth ", "billing"), "filled": hegel.Draw(tc, hegel.Booleans())}
					case 4, 5:
						unit := map[string]any{}
						if hegel.Draw(tc, hegel.Booleans()) {
							first := hegel.Draw(tc, hegel.Integers(1, 5))
							unit["lines"] = map[string]any{"start": first, "end": hegel.Draw(tc, hegel.Integers(first, 8))}
						}
						e.Type, e.Fields = pick("doc.read", "doc.hit"), map[string]any{"path": pick("/docs/a.md", "/docs/sub/b.md", "/elsewhere.md"), "content_hash": pick(current.ContentHash, "old"), "unit": unit}
					case 6:
						after := map[string]any{"lines": float64(5)}
						if hegel.Draw(tc, hegel.Booleans()) {
							after["tokens"] = float64(hegel.Draw(tc, hegel.Integers(0, 100)))
						}
						e.Type, e.Fields = "doc.scalars", map[string]any{"commit_id": pick("c1", "c2", "c3", "c4"), "path": pick("/docs/a.md", "/docs/c.md"), "docset": "docs", "writer": pick("human", "agent", "bot"), "content_hash": pick(current.ContentHash, "old"), "delta": map[string]any{"bytes": float64(hegel.Draw(tc, hegel.Integers(-9, 9))), "lines": float64(1)}, "after": after}
					}
					return e
				})
				events := hegel.Draw(t, hegel.Lists(eventGen).MaxSize(80))
				slices.SortStableFunc(events, func(a, b Event) int { return a.Time.Compare(b.Time) })
				for i := range events {
					events[i].ID = fmt.Sprint(i)
				}
				source := summarySource(events)
				want, err := aggregation.Compute(context.Background(), source, facts, p)
				if err != nil {
					t.Fatal(err)
				}
				var merged Partial
				for i := 0; i+1 < len(boundaries); i++ {
					partial := inc.New(p)
					if err := source.Scan(context.Background(), EventFilter{From: boundaries[i], To: boundaries[i+1].Add(-time.Nanosecond), Types: inc.Types}, func(e Event) error {
						partial.Add(e)
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					encoded, err := json.Marshal(partial)
					if err != nil {
						t.Fatal(err)
					}
					decoded := inc.New(p)
					if err := json.Unmarshal(encoded, decoded); err != nil {
						t.Fatal(err)
					}
					if merged == nil {
						merged = decoded
					} else {
						merged.Merge(decoded)
					}
				}
				got, err := inc.Table(context.Background(), merged, facts, p)
				if err != nil {
					t.Fatal(err)
				}
				wantJSON, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				gotJSON, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(wantJSON, gotJSON) {
					t.Fatalf("merged table differs:\n got %s\nwant %s\nparams=%+v boundaries=%v events=%+v", gotJSON, wantJSON, p, boundaries, events)
				}
			}, hegel.WithTestCases(1000))
		})
	}
}

func TestRepeatedReadsAreStoredOnce(t *testing.T) {
	p := Params{Extra: map[string]string{"path": "/docs/a.md"}}
	lines := builtinIncremental("most-used-lines").New(p).(*lineReadsPartial)
	files := newFileUsagePartial("/docs/a.md")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range 1000 {
		unit := map[string]any{"lines": map[string]any{"start": float64(1 + i%2), "end": float64(2 + i%2)}}
		e := Event{Time: start.Add(time.Duration(i) * time.Second), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": "h", "unit": unit}}
		lines.Add(e)
		files.Add(e)
	}
	if len(lines.Ranges) != 2 {
		t.Fatalf("line reads kept %d ranges, want 2", len(lines.Ranges))
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"ranges":[[1,3]]`) {
		t.Fatalf("coverage was not coalesced: %s", encoded)
	}
}

func TestSessionsAreNotCachedPerDay(t *testing.T) {
	if builtinIncremental("sessions-over-time") != nil {
		t.Fatal("session visibility depends on the whole window and must not be cached per day")
	}
}

func builtinIncremental(name string) *Incremental {
	for _, aggregation := range BuiltinAggregations() {
		if aggregation.Name == name {
			return aggregation.Incremental
		}
	}
	panic("unknown aggregation " + name)
}
