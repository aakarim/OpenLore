package analytics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func moveUsageFixture() (summarySource, ContentFacts) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var events summarySource
	add := func(hour int, kind, path, hash string, fields map[string]any) {
		fields["path"], fields["content_hash"] = path, hash
		events = append(events, Event{Time: start.Add(time.Duration(hour) * time.Hour), Type: kind, Fields: fields})
	}
	scalars := func(lines, tokens int) map[string]any {
		return map[string]any{"after": map[string]any{"lines": lines, "tokens": tokens}}
	}
	rangeRead := func(line int) map[string]any {
		return map[string]any{"unit": ContentUnit{Lines: &LineRange{Start: line, End: line}}}
	}
	add(0, "doc.scalars", "/docs/a.md", "h", scalars(5, 2000))
	add(1, "doc.read", "/docs/a.md", "h", rangeRead(1))
	// Whole reads of an older hash must not warm the moved current hash.
	add(2, "doc.read", "/docs/a.md", "old", map[string]any{})
	add(3, "doc.scalars", "/docs/b.md", "overwritten", scalars(5, 9000))
	add(4, "doc.read", "/docs/b.md", "overwritten", map[string]any{})
	add(5, "doc.read", "/docs/sub/c.md", "h", map[string]any{})
	add(24, "doc.write", "/docs/b.md", "h", map[string]any{"action": "move", "from": "/docs/a.md", "commit_id": "first"})
	replay := scalars(5, 2000)
	replay["action"], replay["move_source"], replay["commit_id"] = "move", "/docs/a.md", "first"
	add(25, "doc.scalars", "/docs/b.md", "h", replay)
	add(26, "doc.read", "/docs/b.md", "h", rangeRead(3))
	add(27, "doc.hit", "/docs/b.md", "h", rangeRead(5))
	add(48, "doc.write", "/docs/sub/c.md", "h", map[string]any{"action": "move", "from": "/docs/b.md", "commit_id": "second"})
	add(49, "doc.scalars", "/docs/a.md", "reuse-a", scalars(3, 1000))
	add(50, "doc.read", "/docs/a.md", "reuse-a", rangeRead(2))
	add(51, "doc.scalars", "/docs/b.md", "reuse-b", scalars(2, 500))
	add(52, "doc.read", "/docs/b.md", "reuse-b", rangeRead(1))
	// Delayed replay must not overwrite the later occupant of b.
	add(72, "doc.scalars", "/docs/b.md", "h", replay)
	return events, NewContentFacts(testFS{
		"/docs/a.md":     []byte("a\nb\nc"),
		"/docs/b.md":     []byte("a\nb"),
		"/docs/sub/c.md": []byte("1\n2\n3\n4\n5"),
	})
}

func assertUsageJSON(t *testing.T, got, want any) {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("got %s\nwant %s", a, b)
	}
}

func restoreUsagePartial(t *testing.T, partial Partial, newPartial func() Partial) Partial {
	t.Helper()
	encoded, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	decoded := newPartial()
	if err := json.Unmarshal(encoded, decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestFileUsageMovesFollowIdentityAndExactCoverage(t *testing.T) {
	events, facts := moveUsageFixture()
	usage, err := FileUsage(t.Context(), events, facts, "/docs", EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 3 {
		t.Fatalf("usage = %+v", usage)
	}
	for i, want := range []struct {
		path, hash  string
		reads, hits int
		last        int
		tokens      float64
		cold        [][2]int
	}{
		{"/docs/a.md", "reuse-a", 1, 0, 12, 1000, [][2]int{{1, 1}, {3, 3}}},
		{"/docs/b.md", "reuse-b", 1, 0, 14, 500, [][2]int{{2, 2}}},
		{"/docs/sub/c.md", "h", 3, 1, 9, 2000, [][2]int{{2, 2}, {4, 4}}},
	} {
		got := usage[i]
		if got.Path != want.path || got.ContentHash != want.hash || got.Reads != want.reads || got.Hits != want.hits || got.LastReadAt == nil || !got.LastReadAt.Equal(events[want.last].Time) || got.Scalars["tokens"] != want.tokens {
			t.Fatalf("usage[%d] = %+v, want %+v", i, got, want)
		}
		var cold []ContentUnit
		for _, r := range want.cold {
			cold = append(cold, ContentUnit{Lines: &LineRange{Start: r[0], End: r[1]}})
		}
		assertUsageJSON(t, got.ColdUnits, cold)
	}

	// Every pair of cut points exercises both left- and right-associated
	// merges, including move-only days and boundaries immediately around a
	// replay, a source reuse, and a destination overwrite.
	for first := 0; first <= len(events); first++ {
		for second := first; second <= len(events); second++ {
			newPartial := func() Partial { return newFileUsagePartial("/docs") }
			build := func(start, end int) Partial {
				partial := newPartial()
				for _, event := range events[start:end] {
					partial.Add(event)
				}
				return restoreUsagePartial(t, partial, newPartial)
			}
			for _, right := range []bool{false, true} {
				a, b, c := build(0, first), build(first, second), build(second, len(events))
				if right {
					b.Merge(c)
					a.Merge(restoreUsagePartial(t, b, newPartial))
				} else {
					a.Merge(b)
					a = restoreUsagePartial(t, a, newPartial)
					a.Merge(c)
				}
				got, err := a.(*fileUsagePartial).usage(t.Context(), facts)
				if err != nil {
					t.Fatalf("cuts=%d,%d right=%t: %v", first, second, right, err)
				}
				assertUsageJSON(t, got, usage)
			}
			// A restored running partial must also accept live Add calls.
			resumed := build(0, first)
			for _, event := range events[first:] {
				resumed.Add(event)
			}
			got, err := resumed.(*fileUsagePartial).usage(t.Context(), facts)
			if err != nil {
				t.Fatal(err)
			}
			assertUsageJSON(t, got, usage)
		}
	}
}

func TestFileUsageMoveInventoryDoesNotEraseSourceScalars(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	events := summarySource{
		{Time: start, Type: "doc.scalars", Fields: map[string]any{"path": "/docs/a.md", "content_hash": "hash", "after": map[string]any{"lines": 2, "tokens": 17}}},
		{Time: start.Add(time.Hour), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md", "content_hash": "hash", "unit": ContentUnit{Lines: &LineRange{Start: 1, End: 1}}}},
		// Replay inventory is timestamped at commit time, before the live move
		// observer event. Neither side may replace the source's useful scalars.
		{Time: start.Add(2 * time.Hour), Type: "doc.scalars", Fields: map[string]any{"path": "/docs/a.md", "action": "move_source", "after": nil}},
		{Time: start.Add(2 * time.Hour), Type: "doc.scalars", Fields: map[string]any{"path": "/docs/b.md", "action": "move", "content_hash": "hash", "after": map[string]any{"lines": 2, "tokens": 17}}},
		{Time: start.Add(3 * time.Hour), Type: "doc.write", Fields: map[string]any{"path": "/docs/b.md", "from": "/docs/a.md", "action": "move", "content_hash": "hash"}},
	}
	usage, err := FileUsage(t.Context(), events, NewContentFacts(testFS{"/docs/b.md": []byte("a\nb")}), "/docs", EventFilter{})
	if err != nil || len(usage) != 1 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	file := usage[0]
	if file.Reads != 1 || file.Scalars["tokens"] != 17 || file.ContentHash != "hash" {
		t.Fatalf("move lost source scalars: %+v", file)
	}
	assertUsageJSON(t, file.ColdUnits, []ContentUnit{{Lines: &LineRange{Start: 2, End: 2}}})
}

func TestFileUsageMoveRankingsMergeAcrossDays(t *testing.T) {
	events, facts := moveUsageFixture()
	p := Params{Extra: map[string]string{"path": "/docs"}}
	for _, name := range []string{"least-used-files", "most-used-files", "least-used-folders"} {
		t.Run(name, func(t *testing.T) {
			want, err := builtin(name)(t.Context(), events, facts, p)
			if err != nil {
				t.Fatal(err)
			}
			if name == "most-used-files" {
				if want.Rows[0][0] != "/docs/b.md" || want.Rows[2][0] != "/docs/sub/c.md" || want.Rows[2][2] != 3 || want.Rows[2][3] != 1 || want.Rows[2][4] != float64(2000) || want.Rows[2][5] != 1.5 {
					t.Fatalf("ranked rows = %+v", want.Rows)
				}
			}
			inc := builtinIncremental(name)
			newPartial := func() Partial { return inc.New(p) }
			for cut := 0; cut <= len(events); cut++ {
				merged := newPartial()
				for _, window := range []summarySource{events[:cut], events[cut:]} {
					partial := newPartial()
					for _, event := range window {
						partial.Add(event)
					}
					merged.Merge(restoreUsagePartial(t, partial, newPartial))
					merged = restoreUsagePartial(t, merged, newPartial)
				}
				got, err := inc.Table(t.Context(), merged, facts, p)
				if err != nil {
					t.Fatal(err)
				}
				assertUsageJSON(t, got, want)
			}
		})
	}
}

func TestFileUsageMovesStayWithinPrefix(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	events := summarySource{
		{Time: start, Type: "doc.read", Fields: map[string]any{"path": "/docs/private/a.md", "content_hash": "h"}},
		{Time: start, Type: "doc.read", Fields: map[string]any{"path": "/docs/publicity/a.md", "content_hash": "h"}},
		{Time: start, Type: "doc.write", Fields: map[string]any{"action": "move", "from": "/docs/private/a.md", "path": "/docs/public/a.md", "content_hash": "h"}},
		{Time: start, Type: "doc.write", Fields: map[string]any{"action": "move", "from": "/docs/publicity/a.md", "path": "/docs/public/b.md", "content_hash": "h"}},
	}
	facts := NewContentFacts(testFS{"/docs/public/a.md": []byte("a"), "/docs/public/b.md": []byte("b")})
	usage, err := FileUsage(t.Context(), events, facts, "/docs/public", EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range usage {
		if file.Reads != 0 || file.Hits != 0 || file.LastReadAt != nil {
			t.Fatalf("cross-prefix usage leaked: %+v", file)
		}
	}
	// The same moves are legitimate within a broader readable query scope.
	broader, err := FileUsage(t.Context(), events, facts, "/docs", EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range broader {
		if file.Reads != 1 {
			t.Fatalf("in-scope move lost usage: %+v", file)
		}
	}
}

func TestFileUsageMoveCycleAndUnreadOverwrite(t *testing.T) {
	var events summarySource
	add := func(kind, path, from string) {
		events = append(events, Event{Time: time.Date(2026, 9, 1, 0, len(events), 0, 0, time.UTC), Type: kind, Fields: map[string]any{"path": path, "from": from, "action": "move", "content_hash": "h"}})
	}
	add("doc.read", "/docs/a", "")
	add("doc.hit", "/docs/b", "")
	add("doc.hit", "/docs/b", "")
	// Swap two files through a temporary name, then reuse that name.
	add("doc.write", "/docs/tmp", "/docs/a")
	add("doc.write", "/docs/a", "/docs/b")
	add("doc.write", "/docs/b", "/docs/tmp")
	add("doc.read", "/docs/tmp", "")
	// A source with no reads must still erase all destination usage.
	add("doc.write", "/docs/b", "/docs/unread")
	facts := NewContentFacts(testFS{"/docs/a": []byte("a"), "/docs/b": []byte("b"), "/docs/tmp": []byte("tmp")})
	want, err := FileUsage(t.Context(), events, facts, "/docs", EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 3 || want[0].Reads != 0 || want[0].Hits != 2 || want[1].Reads != 0 || want[1].Hits != 0 || want[1].LastReadAt != nil || want[2].Reads != 1 || want[2].Hits != 0 {
		t.Fatalf("cycle usage = %+v", want)
	}
	for first := 0; first <= len(events); first++ {
		for second := first; second <= len(events); second++ {
			newPartial := func() Partial { return newFileUsagePartial("/docs") }
			merged := newPartial()
			for _, window := range []summarySource{events[:first], events[first:second], events[second:]} {
				partial := newPartial()
				for _, event := range window {
					partial.Add(event)
				}
				merged.Merge(restoreUsagePartial(t, partial, newPartial))
				merged = restoreUsagePartial(t, merged, newPartial)
			}
			got, err := merged.(*fileUsagePartial).usage(t.Context(), facts)
			if err != nil {
				t.Fatal(err)
			}
			assertUsageJSON(t, got, want)
		}
	}
}

func TestFileUsageMoveTombstonesRespectBudgetAfterRestore(t *testing.T) {
	inc := builtinIncremental("most-used-files")
	p := Params{Extra: map[string]string{"path": "/docs"}}
	newPartial := func() Partial { return inc.New(p) }
	for _, mode := range []string{"path-count", "path-bytes"} {
		t.Run(mode, func(t *testing.T) {
			partial := newPartial()
			count := maxFileUsagePaths
			path := func(i int) string { return fmt.Sprintf("/docs/%05d.md", i) }
			if mode == "path-bytes" {
				count = 128
				path = func(i int) string {
					return "/docs/" + strings.Repeat("x", maxFileUsagePathBytes/count-len("/docs/00000.md")) + fmt.Sprintf("%05d.md", i)
				}
			}
			move := func(i int) Event {
				return Event{Type: "doc.write", Fields: map[string]any{"action": "move", "from": path(i - 1), "path": path(i)}}
			}
			for i := 1; i < count; i++ {
				partial.Add(move(i))
			}
			partial = restoreUsagePartial(t, partial, newPartial)
			facts := NewContentFacts(testFS{path(count - 1): []byte("current")})
			if _, err := inc.Table(t.Context(), partial, facts, p); err != nil {
				t.Fatalf("at budget: %v", err)
			}
			for _, merge := range []bool{false, true} {
				over := restoreUsagePartial(t, partial, newPartial)
				if merge {
					next := newPartial()
					next.Add(move(count))
					over.Merge(restoreUsagePartial(t, next, newPartial))
				} else {
					over.Add(move(count))
				}
				over = restoreUsagePartial(t, over, newPartial)
				if _, err := inc.Table(t.Context(), over, facts, p); err == nil || !strings.Contains(err.Error(), "memory budget") {
					t.Fatalf("over budget merge=%t: %v", merge, err)
				}
			}
		})
	}
}
