package analytics

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

type summarySource []Event

func (s summarySource) Scan(_ context.Context, filter EventFilter, fn func(Event) error) error {
	for _, event := range s {
		if !filter.From.IsZero() && event.Time.Before(filter.From) || !filter.To.IsZero() && event.Time.After(filter.To) {
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
