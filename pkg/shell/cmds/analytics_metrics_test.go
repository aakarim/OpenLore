package cmds_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func runWithMetrics(t *testing.T, command string) []analytics.Event {
	t.Helper()
	return runWithMetricsFS(t, testFS(), command)
}

func runWithMetricsFS(t *testing.T, fs vfs.FileSystem, command string) []analytics.Event {
	t.Helper()
	sh := shell.NewShell(fs)
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
	if len(events) != 3 || events[0].Type != "doc.hit" || events[1].Type != "doc.hit" || events[2].Type != "search.query" {
		t.Fatalf("events = %#v", events)
	}
	firstStart, firstEnd := metricLines(t, events[0])
	secondStart, secondEnd := metricLines(t, events[1])
	sum := sha256.Sum256([]byte("banana\napple\ncherry\napple\ndate\nbanana\n"))
	if firstStart != 2 || firstEnd != 2 || secondStart != 4 || secondEnd != 4 || events[0].Fields["path"] != "/docs/notes.txt" || events[0].Fields["content_hash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("doc.hit fields = %#v", events[0].Fields)
	}
	if _, exists := events[0].Fields["docset"]; exists {
		t.Fatalf("command-layer metric unexpectedly assigned docset: %#v", events[0].Fields)
	}
	query := events[2].Fields
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
	if len(findEvents) != 1 || findEvents[0].Type != "search.query" || findEvents[0].Fields["matched_files"] != 1 {
		t.Fatalf("find events = %#v", findEvents)
	}
}

type trackingTestFS struct {
	*mapFS
	hash             string
	contentHash      string
	contentHashCalls int
}

func (f trackingTestFS) LastReadHash(string) (string, bool) { return f.hash, true }
func (f *trackingTestFS) ReadContentHash(string, []byte) string {
	f.contentHashCalls++
	return f.contentHash
}

func TestDocumentMetricReusesTrackedContentHash(t *testing.T) {
	const trackedHash = "already-computed"
	events := runWithMetricsFS(t, trackingTestFS{mapFS: testFS(), hash: trackedHash}, "cat /docs/readme.md")
	if len(events) != 1 || events[0].Fields["content_hash"] != trackedHash {
		t.Fatalf("events = %#v", events)
	}
}

func TestDocumentMetricPrefersSurfacedContentHashAndComputesItOnce(t *testing.T) {
	fs := &trackingTestFS{mapFS: testFS(), hash: "durable", contentHash: "surfaced"}
	events := runWithMetricsFS(t, fs, "grep apple /docs/notes.txt")
	if len(events) != 3 || events[0].Fields["content_hash"] != "surfaced" || fs.contentHashCalls != 1 {
		t.Fatalf("events=%#v content hash calls=%d", events, fs.contentHashCalls)
	}
}

type canonicalTestFS struct{ *mapFS }

func (f canonicalTestFS) ReadFile(p string) ([]byte, error) {
	return f.mapFS.ReadFile(strings.Replace(p, "/legacy/", "/docs/", 1))
}
func (canonicalTestFS) CanonicalPath(p string) string {
	return strings.Replace(p, "/legacy/", "/docs/", 1)
}

func TestDocumentMetricUsesCanonicalPath(t *testing.T) {
	events := runWithMetricsFS(t, canonicalTestFS{testFS()}, "cat /legacy/readme.md")
	if len(events) != 1 || events[0].Fields["path"] != "/docs/readme.md" {
		t.Fatalf("events = %#v", events)
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

func TestSedInPlaceDoesNotCountAsDocumentRead(t *testing.T) {
	if events := runWithMetrics(t, "sed -i 's/Hello/Goodbye/' /docs/readme.md"); len(events) != 0 {
		t.Fatalf("sed -i events = %#v, want none", events)
	}
}

func TestSedQuietEmitsSeparateSparseLineRanges(t *testing.T) {
	events := runWithMetrics(t, "sed -n '2p;4p' /docs/readme.md")
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	firstStart, firstEnd := metricLines(t, events[0])
	secondStart, secondEnd := metricLines(t, events[1])
	if firstStart != 2 || firstEnd != 2 || secondStart != 4 || secondEnd != 4 {
		t.Fatalf("ranges = %d-%d and %d-%d", firstStart, firstEnd, secondStart, secondEnd)
	}
}

func TestSedQuietMetricsFollowModifiedPatternSpace(t *testing.T) {
	events := runWithMetrics(t, "sed -n 's/Hello/Goodbye/;/Goodbye/p' /docs/readme.md")
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	start, end := metricLines(t, events[0])
	if start != 1 || end != 1 {
		t.Fatalf("range = %d-%d", start, end)
	}
}

func TestEmptySelectionsDoNotEmitDocumentReads(t *testing.T) {
	for _, command := range []string{"head -n 0 /docs/readme.md", "head -c 0 /docs/readme.md", "tail -n 0 /docs/readme.md", "tail -c 0 /docs/readme.md"} {
		t.Run(command, func(t *testing.T) {
			if events := runWithMetrics(t, command); len(events) != 0 {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestGrepMetricsExcludeTrailingNewlineSentinel(t *testing.T) {
	events := runWithMetrics(t, "grep -v apple /docs/notes.txt")
	query := events[len(events)-1]
	if query.Type != "search.query" || query.Fields["matched_lines"] != 4 {
		t.Fatalf("events = %#v", events)
	}
	_, end := metricLines(t, events[len(events)-2])
	if end != 6 {
		t.Fatalf("last hit ended at line %d", end)
	}
}

func TestGrepStdinMetricHasNoFilesystemScope(t *testing.T) {
	events := runWithMetrics(t, "echo apple | grep apple")
	if len(events) != 1 || events[0].Type != "search.query" {
		t.Fatalf("events = %#v", events)
	}
	scope, ok := events[0].Fields["scope"].([]string)
	if !ok || len(scope) != 0 {
		t.Fatalf("scope = %#v", events[0].Fields["scope"])
	}
}
