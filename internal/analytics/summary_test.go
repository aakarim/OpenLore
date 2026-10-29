package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"hegel.dev/go/hegel"
)

type summarySource []Event

func (s summarySource) Scan(_ context.Context, filter EventFilter, fn func(Event) error) error {
	for _, event := range s {
		if !filter.From.IsZero() && event.Time.Before(filter.From) || !filter.To.IsZero() && event.Time.After(filter.To) {
			continue
		}
		if len(filter.Types) > 0 && !slices.Contains(filter.Types, event.Type) {
			continue
		}
		if err := fn(event); err != nil {
			return err
		}
	}
	return nil
}

func TestUsageSummaryPartialCoverageClassificationAndWriteDeduplication(t *testing.T) {
	day := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	source := summarySource{
		{ID: "read", Time: day, Type: "doc.read", Fields: map[string]any{"characters": 5, "bytes": 7, "actor_kind": "human"}},
		{ID: "legacy-hit", Time: day, Type: "doc.hit", Fields: map[string]any{"actor_kind": "unknown"}},
		{ID: "write", Time: day, Type: "doc.write", Fields: map[string]any{"commit_id": "one", "path": "/deleted.md", "writer": "human", "action": "delete"}},
		{ID: "same-scalar", Time: day, Type: "doc.scalars", Fields: map[string]any{"commit_id": "one", "path": "/deleted.md", "writer": "agent"}},
		{ID: "agent-scalar", Time: day, Type: "doc.scalars", Fields: map[string]any{"commit_id": "two", "path": "/other.md", "writer": "agent"}},
		{ID: "unknown-write", Time: day, Type: "doc.write", Fields: map[string]any{"commit_id": "three", "path": "/legacy.md"}},
		{ID: "command", Time: day, Type: "command.exec", Fields: map[string]any{"actor_kind": "agent"}},
	}

	summary, err := UsageSummary(context.Background(), source, Params{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reads != 1 || summary.Hits != 1 || summary.Writes != 3 || summary.HumanWrites != 1 || summary.AgentWrites != 1 || summary.UnknownWrites != 1 || summary.Commands != 1 {
		t.Fatalf("summary totals = %#v", summary)
	}
	if summary.EstimatedTokens != 2 || summary.EstimatedReads != 1 || summary.UnestimatedReads != 1 || !strings.Contains(summary.Note, "1 legacy") {
		t.Fatalf("coverage = %#v", summary)
	}
	if len(summary.Activity) != 1 || summary.Activity[0] != (SummaryActivity{Date: "2026-09-15", Human: 2, Agent: 2, Unknown: 2, Reads: 2, Writes: 3}) {
		t.Fatalf("activity = %#v", summary.Activity)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"human_writes"`, `"unknown_writes"`, `"estimated_tokens"`, `"unestimated_reads"`, `"computed_at"`} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("JSON missing %s: %s", key, encoded)
		}
	}
}

func TestUsageSummaryDoesNotCollapseLegacyWritesWithoutKeys(t *testing.T) {
	day := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	summary, err := UsageSummary(context.Background(), summarySource{
		{ID: "legacy-one", Time: day, Type: "doc.write", Fields: map[string]any{}},
		{ID: "legacy-two", Time: day, Type: "doc.write", Fields: map[string]any{}},
	}, Params{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Writes != 2 || summary.UnknownWrites != 2 {
		t.Fatalf("legacy writes collapsed: %#v", summary)
	}
}

func TestUsageSummaryWriteAttributionProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("Hegel property tests are excluded in short mode")
	}
	hegel.Test(t, func(t *hegel.T) {
		start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		kinds := []string{"human", "agent", "unknown"}
		var events summarySource
		var totals [3]int
		var daily [3][3]int
		// Construct logical writes first, so expected totals do not depend on
		// the production deduplication key or event-classification helpers.
		for i := range hegel.Draw(t, hegel.Integers(0, 18)) {
			shape := hegel.Draw(t, hegel.Integers(0, 2))   // scalar only, write only, both
			keyMode := hegel.Draw(t, hegel.Integers(0, 4)) // complete, missing/empty commit/path
			docKind := hegel.Draw(t, hegel.Integers(0, 2))
			for variant, eventType := range []string{"doc.scalars", "doc.write"} {
				if shape == 0 && variant == 1 || shape == 1 && variant == 0 {
					continue
				}
				kind := docKind
				if variant == 0 {
					kind = (docKind + 1) % 3 // conflicting attribution must not override doc.write
				}
				day := hegel.Draw(t, hegel.Integers(0, 2))
				fields := map[string]any{"commit_id": fmt.Sprintf("c%d", i/3), "path": fmt.Sprintf("/docs/%d.md", i%3)}
				switch keyMode {
				case 1:
					delete(fields, "commit_id")
				case 2:
					fields["commit_id"] = ""
				case 3:
					delete(fields, "path")
				case 4:
					fields["path"] = ""
				}
				if hegel.Draw(t, hegel.Booleans()) {
					fields["writer"] = kinds[kind]
					fields["actor_kind"] = kinds[(kind+1)%3]
				} else {
					fields["actor_kind"] = kinds[kind]
					if hegel.Draw(t, hegel.Booleans()) {
						fields["writer"] = ""
					}
				}
				// Omitted and unrecognised kinds are both unknown.
				if kind == 2 && hegel.Draw(t, hegel.Booleans()) {
					delete(fields, "writer")
					delete(fields, "actor_kind")
				}
				copies := hegel.Draw(t, hegel.Integers(1, 3))
				for copy := range copies {
					events = append(events, Event{ID: fmt.Sprintf("%d-%d-%d", i, variant, copy), Time: start.Add(time.Duration(day) * 24 * time.Hour), Type: eventType, Fields: fields})
				}
				if keyMode != 0 {
					// Without a complete key these are independent legacy events.
					totals[kind] += copies
					daily[day][kind] += copies
				} else if variant == 1 || shape == 0 {
					totals[kind]++
					daily[day][kind]++
				}
			}
		}
		// Randomise arrival order, including pairs on different days.
		for i := len(events) - 1; i > 0; i-- {
			j := hegel.Draw(t, hegel.Integers(0, i))
			events[i], events[j] = events[j], events[i]
		}
		got, err := UsageSummary(context.Background(), events, Params{}, 4)
		if err != nil {
			t.Fatal(err)
		}
		want := Summary{Writes: totals[0] + totals[1] + totals[2], HumanWrites: totals[0], AgentWrites: totals[1], UnknownWrites: totals[2], Activity: []SummaryActivity{}, ComputedAt: got.ComputedAt}
		for day, counts := range daily {
			total := counts[0] + counts[1] + counts[2]
			if total > 0 {
				want.Activity = append(want.Activity, SummaryActivity{Date: start.Add(time.Duration(day) * 24 * time.Hour).Format("2006-01-02"), Human: counts[0], Agent: counts[1], Unknown: counts[2], Writes: total})
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("write summary differs:\n got %+v\nwant %+v\nevents=%+v", got, want, events)
		}
	}, hegel.WithTestCases(1000))
}

func TestUsageSummaryRejectsInvalidCharacterCounts(t *testing.T) {
	day := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	summary, err := UsageSummary(context.Background(), summarySource{
		{ID: "valid", Time: day, Type: "doc.read", Fields: map[string]any{"characters": 5}},
		{ID: "negative", Time: day, Type: "doc.read", Fields: map[string]any{"characters": -1}},
		{ID: "nan", Time: day, Type: "doc.read", Fields: map[string]any{"characters": math.NaN()}},
		{ID: "infinite", Time: day, Type: "doc.read", Fields: map[string]any{"characters": math.Inf(1)}},
		{ID: "too-large", Time: day, Type: "doc.read", Fields: map[string]any{"characters": math.MaxFloat64}},
	}, Params{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reads != 5 || summary.EstimatedTokens != 2 || summary.EstimatedReads != 1 || summary.UnestimatedReads != 4 {
		t.Fatalf("invalid count coverage = %#v", summary)
	}
}

func TestUsageSummaryRejectsTokenTotalOverflow(t *testing.T) {
	day := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	characters := math.Exp2(62)
	summary, err := UsageSummary(context.Background(), summarySource{
		{ID: "one", Time: day, Type: "doc.read", Fields: map[string]any{"characters": characters}},
		{ID: "two", Time: day, Type: "doc.read", Fields: map[string]any{"characters": characters}},
	}, Params{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EstimatedTokens != int64(characters) || summary.EstimatedReads != 1 || summary.UnestimatedReads != 1 {
		t.Fatalf("overflow coverage = %#v", summary)
	}
}

func TestIsAuthoredWrite(t *testing.T) {
	for _, eventType := range []string{"doc.write", "doc.scalars"} {
		for _, tc := range []struct {
			name   string
			fields map[string]any
			want   bool
		}{
			{"legacy", nil, true},
			{"create", map[string]any{"action": "create"}, true},
			{"update", map[string]any{"action": "update", "generated": false}, true},
			{"delete", map[string]any{"action": "delete"}, true},
			{"move", map[string]any{"action": "move"}, false},
			{"move_source", map[string]any{"action": "move_source"}, false},
			{"generated", map[string]any{"action": "update", "generated": true}, false},
		} {
			t.Run(eventType+"/"+tc.name, func(t *testing.T) {
				if got := IsAuthoredWrite(Event{Type: eventType, Fields: tc.fields}); got != tc.want {
					t.Fatalf("IsAuthoredWrite = %v, want %v", got, tc.want)
				}
			})
		}
	}
	if IsAuthoredWrite(Event{Type: "doc.read"}) {
		t.Fatal("read classified as authored write")
	}
}

// The second day contains only moves and generated maintenance, including a
// cross-docset move and a same-docset move whose inventory deltas cancel out.
func authoredMaintenanceEvents() summarySource {
	day := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	var events summarySource
	add := func(commit, path, docset, writer, action string, generated bool, when time.Time, bytes, lines, tokens float64) {
		fields := map[string]any{"commit_id": commit, "path": path, "docset": docset, "writer": writer, "action": action,
			"delta": map[string]any{"bytes": bytes, "lines": lines, "tokens": tokens}}
		if generated {
			fields["generated"] = true
		}
		events = append(events, Event{ID: commit + path + "scalar", Time: when, Type: "doc.scalars", Fields: fields})
		if action != "move_source" {
			events = append(events, Event{ID: commit + path + "write", Time: when, Type: "doc.write", Fields: fields})
		}
	}
	add("create", "/a.md", "docs", "human", "create", false, day, 100, 10, 25)
	add("update", "/a.md", "docs", "agent", "update", false, day, -20, -2, -5)
	add("delete", "/legacy.md", "docs", "unknown", "delete", false, day, -10, -1, -2)
	maintenance := day.Add(24 * time.Hour)
	add("cross", "/a.md", "docs", "human", "move_source", false, maintenance, -70, -7, -18)
	add("cross", "/archive/a.md", "archive", "human", "move", false, maintenance, 70, 7, 18)
	add("same", "/b.md", "docs", "agent", "move_source", false, maintenance, -30, -3, -8)
	add("same", "/c.md", "docs", "agent", "move", false, maintenance, 30, 3, 8)
	add("index", "/index.md", "docs", "agent", "update", true, maintenance, 12, 2, 3)
	return events
}

func TestUsageSummaryExcludesMoveAndGeneratedMaintenanceIncludingReplay(t *testing.T) {
	source := authoredMaintenanceEvents()
	// Replayed scalars can have new IDs but retain their commit/path identity.
	for _, event := range append(summarySource(nil), source...) {
		event.ID += "-replay"
		source = append(source, event)
	}
	summary, err := UsageSummary(context.Background(), source, Params{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Writes != 3 || summary.HumanWrites != 1 || summary.AgentWrites != 1 || summary.UnknownWrites != 1 {
		t.Fatalf("authored totals = %+v", summary)
	}
	want := []SummaryActivity{{Date: "2026-09-15", Human: 1, Agent: 1, Unknown: 1, Writes: 3}}
	if !reflect.DeepEqual(summary.Activity, want) {
		t.Fatalf("authored activity = %+v, want %+v", summary.Activity, want)
	}
}

func TestBuiltinAuthorshipAndInventoryIncludeReplayAndPartialRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := authoredMaintenanceEvents()
	for _, name := range []string{"size-over-time", "write-ratio"} {
		t.Run(name, func(t *testing.T) {
			var aggregation Aggregation
			for _, candidate := range BuiltinAggregations() {
				if candidate.Name == name {
					aggregation = candidate
				}
			}
			want := [][]any{{"", 1, 1, 1, float64(1) / 3, float64(100), float64(-20), float64(-10)}}
			if name == "size-over-time" {
				want = [][]any{
					{"2026-09-15T00:00:00Z", "docs", float64(3), float64(70), float64(7), float64(18)},
					{"2026-09-16T00:00:00Z", "archive", float64(0), float64(70), float64(7), float64(18)},
					{"2026-09-16T00:00:00Z", "docs", float64(0), float64(-58), float64(-5), float64(-15)},
				}
			}
			replayed := append(summarySource(nil), source...)
			for _, event := range source {
				event.ID += "-replay"
				replayed = append(replayed, event)
			}
			table, err := aggregation.Compute(ctx, replayed, nil, Params{})
			if err != nil || !reflect.DeepEqual(table.Rows, want) {
				t.Fatalf("single scan rows=%#v want=%#v err=%v", table.Rows, want, err)
			}
			// Daily partials must preserve Count=false through serialization,
			// and merging a replay must not duplicate inventory or authorship.
			merged := aggregation.Incremental.New(Params{})
			for _, day := range []string{"2026-09-15", "2026-09-16", "2026-09-16"} {
				partial := aggregation.Incremental.New(Params{})
				for _, event := range source {
					if event.Type == "doc.scalars" && event.Time.Format("2006-01-02") == day {
						partial.Add(event)
					}
				}
				encoded, err := json.Marshal(partial)
				if err != nil {
					t.Fatal(err)
				}
				decoded := aggregation.Incremental.New(Params{})
				if err := json.Unmarshal(encoded, decoded); err != nil {
					t.Fatal(err)
				}
				merged.Merge(decoded)
			}
			table, err = aggregation.Incremental.Table(ctx, merged, nil, Params{})
			if err != nil || !reflect.DeepEqual(table.Rows, want) {
				t.Fatalf("merged rows=%#v want=%#v err=%v", table.Rows, want, err)
			}
		})
	}
}

func TestPrometheusExcludesMoveAndGeneratedAuthorActivity(t *testing.T) {
	a := NewAggregator(AggregatorOptions{})
	for _, event := range authoredMaintenanceEvents() {
		a.Consume(context.Background(), event)
		if event.Type == "doc.scalars" {
			event.ID += "-replay"
			a.Consume(context.Background(), event)
		}
	}
	response := httptest.NewRecorder()
	a.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for writer, bytes := range map[string]string{"human": "100", "agent": "-20", "unknown": "-10"} {
		for _, metric := range []string{
			fmt.Sprintf(`openlore_doc_writes_total{docset="docs",writer=%q} 1`, writer),
			fmt.Sprintf(`openlore_doc_write_scalar_total{docset="docs",writer=%q,scalar="bytes"} %s`, writer, bytes),
		} {
			if !strings.Contains(body, metric+"\n") {
				t.Errorf("missing metric %s:\n%s", metric, body)
			}
		}
	}
	if len(a.writes) != 3 || len(a.scalars) != 9 || strings.Contains(body, `docset="archive"`) {
		t.Fatalf("maintenance produced author metric series:\n%s", body)
	}
}
