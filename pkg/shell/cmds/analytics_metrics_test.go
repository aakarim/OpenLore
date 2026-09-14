package cmds_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/shell"
)

func runWithMetrics(t *testing.T, command string) []analytics.Event {
	t.Helper()
	sh := shell.NewShell(testFS())
	var events []analytics.Event
	sh.SetCommandObserver(func(shell.CommandExecution) {})
	sh.SetMetricEmitter(func(ctx context.Context, eventType string, fields map[string]any) {
		invocationID, parentID, ok := analytics.InvocationFromContext(ctx)
		if !ok || invocationID == "" || parentID == "" {
			t.Fatalf("metric %q was not correlated to its command", eventType)
		}
		events = append(events, analytics.Event{Type: eventType, InvocationID: invocationID, ParentID: parentID, Fields: fields})
	})
	var out bytes.Buffer
	if code := sh.ExecPipeline(command, &out, &out, nil); code > 1 {
		t.Fatalf("%q exited %d: %s", command, code, out.String())
	}
	return events
}

func metricLines(t *testing.T, event analytics.Event) (int, int) {
	t.Helper()
	unit := event.Fields["unit"].(map[string]any)
	lines := unit["lines"].(map[string]any)
	return lines["start"].(int), lines["end"].(int)
}

func TestGrepEmitsSearchAndDocumentHitMetrics(t *testing.T) {
	events := runWithMetrics(t, "grep apple /docs/notes.txt")
	if len(events) != 2 || events[0].Type != "doc.hit" || events[1].Type != "search.query" {
		t.Fatalf("events = %#v", events)
	}
	start, end := metricLines(t, events[0])
	sum := sha256.Sum256([]byte("banana\napple\ncherry\napple\ndate\nbanana\n"))
	if start != 2 || end != 4 || events[0].Fields["path"] != "/docs/notes.txt" || events[0].Fields["docset"] != "docs" || events[0].Fields["content_hash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("doc.hit fields = %#v", events[0].Fields)
	}
	query := events[1].Fields
	if query["pattern"] != "apple" || query["matched_files"] != 1 || query["matched_lines"] != 2 || query["filled"] != true {
		t.Fatalf("search.query fields = %#v", query)
	}
}

func TestUnfilledGrepAndFindEmitSearchMetrics(t *testing.T) {
	grepEvents := runWithMetrics(t, "grep absent /docs/notes.txt")
	if len(grepEvents) != 1 || grepEvents[0].Type != "search.query" || grepEvents[0].Fields["filled"] != false {
		t.Fatalf("grep events = %#v", grepEvents)
	}
	findEvents := runWithMetrics(t, "find /docs -name '*.md'")
	if len(findEvents) != 2 || findEvents[0].Type != "doc.hit" || findEvents[1].Type != "search.query" || findEvents[1].Fields["matched_files"] != 1 {
		t.Fatalf("find events = %#v", findEvents)
	}
}

func TestContentCommandsEmitBestEffortLineRanges(t *testing.T) {
	tests := []struct {
		command   string
		wantStart int
		wantEnd   int
	}{
		{"cat /docs/readme.md", 1, 5},
		{"head -n 2 /docs/readme.md", 1, 2},
		{"tail -n 2 /docs/readme.md", 5, 5},
		{"sed -n '2,3p' /docs/readme.md", 2, 3},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			events := runWithMetrics(t, test.command)
			if len(events) != 1 || events[0].Type != "doc.read" {
				t.Fatalf("events = %#v", events)
			}
			start, end := metricLines(t, events[0])
			if start != test.wantStart || end != test.wantEnd {
				t.Fatalf("line range = %d-%d, want %d-%d", start, end, test.wantStart, test.wantEnd)
			}
		})
	}
}
