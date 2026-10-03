package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

type consumerFunc func(context.Context, Event)

func (f consumerFunc) Consume(ctx context.Context, event Event) { f(ctx, event) }

func TestDirectoryFactsKeepNestedDocsetOwnershipSeparate(t *testing.T) {
	store, index := testFactsIndex(t)
	ctx := context.Background()
	for _, fact := range []IndexedFacts{
		{Path: "/docs/a.md", Owner: "parent", Size: 5, MTimeNS: 1, ContentHash: "a", Sources: map[string]map[string]float64{"size": {"bytes": 5, "lines": 2, "characters": 4}, "approx": {"tokens": 1}}},
		{Path: "/docs/private/b.md", Owner: "nested", Size: 11, MTimeNS: 1, ContentHash: "b", Sources: map[string]map[string]float64{"size": {"bytes": 11, "lines": 3, "characters": 9}, "approx": {"tokens": 3}}},
	} {
		if err := index.Upsert(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(path, owner string, files int, bytes, lines, characters, tokens float64) {
		t.Helper()
		var gotFiles int
		var gotBytes, gotLines, gotCharacters, gotTokens float64
		if err := store.db.QueryRow(`SELECT files,bytes,lines,characters,tokens FROM directory_facts WHERE path=? AND owner=?`, path, owner).
			Scan(&gotFiles, &gotBytes, &gotLines, &gotCharacters, &gotTokens); err != nil {
			t.Fatal(err)
		}
		if gotFiles != files || gotBytes != bytes || gotLines != lines || gotCharacters != characters || gotTokens != tokens {
			t.Fatalf("%s/%s = (%d,%v,%v,%v,%v)", path, owner, gotFiles, gotBytes, gotLines, gotCharacters, gotTokens)
		}
	}
	assert("/docs", "parent", 1, 5, 2, 4, 1)
	assert("/docs", "nested", 1, 11, 3, 9, 3)
}

func TestLiveMetricsIgnoreAnalyticsProcessingAndDurableReplay(t *testing.T) {
	dir := t.TempDir()
	disabled := false
	open := func(enabled *bool) *Service {
		t.Helper()
		service, err := New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Enabled: enabled}}, Deps{})
		if err != nil {
			t.Fatal(err)
		}
		service.Start(context.Background())
		return service
	}
	metric := func(service *Service) string {
		t.Helper()
		response := httptest.NewRecorder()
		service.Aggregator().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return response.Body.String()
	}
	first := open(&disabled)
	first.Record(context.Background(), Event{ID: "live-disabled", Type: "command.exec", Transport: "ssh", Fields: map[string]any{"command": "cat"}})
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(metric(first), `openlore_commands_total{command="cat",transport="ssh",exit_class="success"} 1`) {
		if time.Now().After(deadline) {
			t.Fatalf("disabled processing stopped live metrics:\n%s", metric(first))
		}
		time.Sleep(time.Millisecond)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := open(nil)
	defer second.Close(context.Background())
	for deadline := time.Now().Add(2 * time.Second); second.pipeline != nil && !second.pipeline.CaughtUp(); {
		if time.Now().After(deadline) {
			t.Fatal("pipeline did not catch up")
		}
		time.Sleep(time.Millisecond)
	}
	if strings.Contains(metric(second), `command="cat"`) {
		t.Fatalf("restart replay contaminated resettable metrics:\n%s", metric(second))
	}
}

func TestDashboardUsageAlwaysSerializesActivityAsArray(t *testing.T) {
	for _, state := range []string{"cold", "failed", "disabled", "ready"} {
		t.Run(state, func(t *testing.T) {
			enabled := state != "disabled"
			service, err := New(config.AnalyticsConfig{Dir: t.TempDir(), Pipeline: config.AnalyticsPipelineConfig{Enabled: &enabled}}, Deps{})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close(context.Background())
			service.eventIndex.caughtUp.Store(true)
			store := service.store.(*SQLiteAggregationStore)
			if state == "ready" || state == "failed" {
				var value any
				lastError := "failed build"
				if state == "ready" {
					value, lastError = `{"activity":null,"reads":7}`, ""
				}
				if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,value,computed_at,error) VALUES('test',?,?,?)`, value, time.Now().UnixNano(), lastError); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.DashboardUsage(context.Background(), "test", 30*24*time.Hour, nil)
			if err != nil || result.Analytics.State != state || result.Analytics.Complete != (state == "ready") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			body, err := json.Marshal(result)
			if err != nil || !strings.Contains(string(body), `"activity":[]`) {
				t.Fatalf("activity must be an array: %s err=%v", body, err)
			}
		})
	}
}

func TestFailedDashboardViewsBackOffInsteadOfRebuildingEveryPoll(t *testing.T) {
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir()}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	service.eventIndex.caughtUp.Store(true)
	store := service.store.(*SQLiteAggregationStore)
	for _, key := range []string{"usage", "aggregation:view"} {
		if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,computed_at,error) VALUES(?,?,'build failed')`, key, time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, nil)
	if err != nil || usage.Analytics.State != "failed" || usage.Analytics.Updating {
		t.Fatalf("usage retried immediately: %+v err=%v", usage.Analytics, err)
	}
	view, err := service.DashboardMaterialized(context.Background(), "view", 7*24*time.Hour, nil)
	if err != nil || view.Analytics.State != "failed" || view.Analytics.Updating {
		t.Fatalf("aggregation retried immediately: %+v err=%v", view.Analytics, err)
	}
	if service.processor.active("usage:usage") || service.processor.active("aggregation:aggregation:view") {
		t.Fatal("failed view was queued during backoff")
	}
	old := time.Now().Add(-2 * dashboardRetryInterval).UnixNano()
	if _, err := store.db.Exec(`UPDATE dashboard_views SET computed_at=?`, old); err != nil {
		t.Fatal(err)
	}
	if usage, _ := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, nil); !usage.Analytics.Updating {
		t.Fatalf("failed usage was not retried after backoff: %+v", usage.Analytics)
	}
}

func TestConsecutiveDashboardFailuresRestartBackoff(t *testing.T) {
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir()}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	service.eventIndex.caughtUp.Store(true)
	store := service.store.(*SQLiteAggregationStore)
	old := time.Now().Add(-2 * dashboardRetryInterval).UnixNano()
	if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,computed_at,error) VALUES('usage',?,'first failure')`, old); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context) (Summary, error) { return Summary{}, errors.New("second failure") }
	first, err := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, fail)
	if err != nil || !first.Analytics.Updating {
		t.Fatalf("expired backoff did not retry: %+v err=%v", first.Analytics, err)
	}
	item, ok := service.processor.pop()
	if !ok {
		t.Fatal("retry was not queued")
	}
	item.run(context.Background())
	service.processor.finish(item.key)
	second, err := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, fail)
	if err != nil || second.Analytics.Updating || second.Analytics.Error != "second failure" {
		t.Fatalf("second failure did not restart backoff: %+v err=%v", second.Analytics, err)
	}
	if wait := time.Until(second.Analytics.RetryAt); wait < dashboardRetryInterval-5*time.Second || wait > dashboardRetryInterval {
		t.Fatalf("retry_at=%v is not one interval after the latest failure", second.Analytics.RetryAt)
	}

	// A failure while a saved result exists must not move its computed time.
	computed := time.Now().Add(-time.Hour).UnixNano()
	if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,value,computed_at,error) VALUES('saved','{}',?,'')`, computed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(failedViewSQL, "saved", time.Now().UnixNano(), "refresh failed"); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := store.db.QueryRow(`SELECT computed_at FROM dashboard_views WHERE key='saved'`).Scan(&got); err != nil || got != computed {
		t.Fatalf("saved result computed_at=%d want %d err=%v", got, computed, err)
	}
}

func TestDashboardUsageDeduplicatesAndPublishesOnlyCompleteResult(t *testing.T) {
	dir := t.TempDir()
	service, err := New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	defer service.Close(context.Background())
	for deadline := time.Now().Add(time.Second); !service.eventIndex.caughtUp.Load(); {
		if time.Now().After(deadline) {
			t.Fatal("pipeline did not finish initial event-index catch-up")
		}
		time.Sleep(time.Millisecond)
	}

	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	compute := func(context.Context) (Summary, error) {
		calls.Add(1)
		close(started)
		<-release
		return Summary{Reads: 7, Activity: []SummaryActivity{}, ComputedAt: time.Now().UTC()}, nil
	}
	first, err := service.DashboardUsage(context.Background(), "same", 30*24*time.Hour, compute)
	if err != nil || first.Analytics.State != "cold" || !first.Analytics.Updating || first.Reads != 0 {
		t.Fatalf("cold result = %#v, err=%v", first, err)
	}
	<-started
	for range 8 {
		result, err := service.DashboardUsage(context.Background(), "same", 30*24*time.Hour, compute)
		if err != nil || result.Reads != 0 || !result.Analytics.Updating {
			t.Fatalf("in-flight result = %#v, err=%v", result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate compute calls = %d", calls.Load())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := service.DashboardUsage(context.Background(), "same", 30*24*time.Hour, compute)
		if err != nil {
			t.Fatal(err)
		}
		if result.Reads == 7 && result.Analytics.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("complete view was not published: %#v", result)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkProcessorRunsOneExpensiveUnitAtATime(t *testing.T) {
	p := newWorkProcessor()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx)
	var active, maximum atomic.Int32
	var wg sync.WaitGroup
	wg.Add(3)
	for _, key := range []string{"facts", "usage:a", "usage:b"} {
		key := key
		p.enqueue(key, key != "facts", func(context.Context) {
			current := active.Add(1)
			for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
			}
			time.Sleep(15 * time.Millisecond)
			active.Add(-1)
			wg.Done()
		})
	}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent work = %d", maximum.Load())
	}
}

func TestPipelineCatchUpSharesExpensiveWorkGate(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenEventLog(filepath.Join(dir, "events"), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(context.Background(), Event{ID: "one", Type: "doc.read"}); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{}, 1)
	processor := newWorkProcessor(gate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go processor.run(ctx)
	entered, release := make(chan struct{}), make(chan struct{})
	pipeline := NewPipeline(log, filepath.Join(dir, "pipeline.checkpoint"), PipelineOptions{Gate: gate, Consumers: []Consumer{consumerFunc(func(context.Context, Event) {
		close(entered)
		<-release
	})}})
	pipeline.Run(ctx)
	<-entered
	requested := make(chan struct{})
	processor.enqueue("requested", true, func(context.Context) { close(requested) })
	select {
	case <-requested:
		t.Fatal("requested job overlapped pipeline catch-up")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("requested job did not run after pipeline released budget")
	}
	if err := pipeline.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledProcessingKeepsEventLogAndReenableCatchesUpIdempotently(t *testing.T) {
	dir := t.TempDir()
	disabled := false
	first, err := New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Enabled: &disabled}}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	first.Start(context.Background())
	first.Record(context.Background(), Event{ID: "durable-one", Time: time.Now().UTC(), Type: "doc.read", Fields: map[string]any{"path": "/docs/a.md"}})
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	openAndCatchUp := func() *Service {
		t.Helper()
		service, err := New(config.AnalyticsConfig{Dir: dir, Log: config.AnalyticsLogConfig{Compress: "none"}}, Deps{})
		if err != nil {
			t.Fatal(err)
		}
		service.Start(context.Background())
		deadline := time.Now().Add(2 * time.Second)
		for !service.eventIndex.caughtUp.Load() {
			if time.Now().After(deadline) {
				t.Fatal("event index did not catch up")
			}
			time.Sleep(time.Millisecond)
		}
		return service
	}
	assertOne := func(service *Service) {
		t.Helper()
		count := 0
		if err := service.IndexedEventSource().Scan(context.Background(), EventFilter{}, func(event Event) error {
			if event.ID == "durable-one" {
				count++
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("indexed durable event count = %d", count)
		}
	}

	second := openAndCatchUp()
	assertOne(second)
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	third := openAndCatchUp()
	defer third.Close(context.Background())
	assertOne(third)
}

func TestEventIndexProgressAndImmediateBatchFollowup(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	ctx, cancel := context.WithCancel(context.Background())
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i, id := range []string{"first", "second", "third"} {
		// One daily segment each: 100 days ago, 20 days ago, and today.
		at := today.Add(-[]time.Duration{100, 20, 0}[i] * 24 * time.Hour)
		if err := service.log.Append(ctx, Event{ID: id, Time: at, Type: "doc.read"}); err != nil {
			t.Fatal(err)
		}
	}
	index := service.eventIndex
	go index.run(ctx, service.log, filepath.Join(t.TempDir(), "cursor"), service.processor)
	defer func() { cancel(); <-index.done }()
	select {
	case <-service.processor.wake:
	case <-time.After(time.Second):
		t.Fatal("initial catch-up was not scheduled")
	}
	compute := func(context.Context) (Summary, error) {
		t.Fatal("usage must wait for catch-up")
		return Summary{}, nil
	}
	for i := int64(1); i <= 3; i++ {
		item, ok := service.processor.pop()
		if !ok || item.key != "event-index" {
			t.Fatalf("batch %d was not queued immediately: %+v", i, item)
		}
		item.run(ctx)
		service.processor.finish(item.key)
		if index.processed.Load() != i || index.caughtUp.Load() != (i == 3) {
			t.Fatalf("batch %d: processed=%d caughtUp=%v", i, index.processed.Load(), index.caughtUp.Load())
		}
		if i < 3 {
			// History is indexed newest first, so this range includes older
			// unprocessed history until the final batch.
			result, err := service.DashboardUsage(ctx, "progress", 365*24*time.Hour, compute)
			progress := result.Analytics.Progress
			if err != nil || !result.Analytics.Updating || result.Analytics.Complete || progress == nil || progress.Phase != "history" || progress.Processed != i || progress.Unit != "events" {
				t.Fatalf("catch-up result=%+v progress=%+v err=%v", result.Analytics, progress, err)
			}
			// Processed history starts after the newest unprocessed day.
			wantSince := today.Add(-[]time.Duration{0, 19, 99}[i] * 24 * time.Hour)
			if !progress.Since.Equal(wantSince) {
				t.Fatalf("batch %d processed since %v, want %v", i, progress.Since, wantSince)
			}
		}
		if i == 1 {
			if !index.covers(7*24*time.Hour) || index.covers(30*24*time.Hour) {
				t.Fatalf("newest batch coverage: 7d=%v 30d=%v", index.covers(7*24*time.Hour), index.covers(30*24*time.Hour))
			}
		}
		if i == 2 && (!index.covers(90*24*time.Hour) || index.covers(100*24*time.Hour)) {
			t.Fatalf("second batch coverage: 90d=%v 100d=%v", index.covers(90*24*time.Hour), index.covers(100*24*time.Hour))
		}
	}
}

func TestDashboardUsageServesRecentRangeBeforeOlderHistory(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, days := range []int{200, 1} {
		if err := service.log.Append(ctx, Event{ID: fmt.Sprint(days), Time: today.Add(-time.Duration(days) * 24 * time.Hour), Type: "doc.read"}); err != nil {
			t.Fatal(err)
		}
	}
	index := service.eventIndex
	index.log, index.cursor, index.checkpoint = service.log, logCursor{}, filepath.Join(t.TempDir(), "cursor")
	if more, err := index.catchUpBatch(ctx); err != nil || !more {
		t.Fatalf("first batch more=%v err=%v", more, err)
	}
	computed := false
	result, err := service.DashboardUsage(ctx, "recent", 7*24*time.Hour, func(context.Context) (Summary, error) {
		computed = true
		return Summary{Reads: 1, ComputedAt: time.Now()}, nil
	})
	if err != nil || result.Analytics.Coverage == "durable event index is catching up" {
		t.Fatalf("recent range waited for old history: %+v err=%v", result.Analytics, err)
	}
	old, err := service.DashboardUsage(ctx, "old", 365*24*time.Hour, func(context.Context) (Summary, error) {
		t.Fatal("old range computed before its history was indexed")
		return Summary{}, nil
	})
	if err != nil || old.Analytics.Coverage != "durable event index is catching up" {
		t.Fatalf("old range=%+v err=%v", old.Analytics, err)
	}
	item, ok := service.processor.pop()
	if !ok || item.key != "usage:recent" {
		t.Fatalf("recent range was not queued: %+v", item)
	}
	item.run(ctx)
	if !computed {
		t.Fatal("recent range was not computed")
	}
	if index.processed.Load() != 1 {
		t.Fatalf("recent range consumed older history: processed=%d", index.processed.Load())
	}
}

func TestEventIndexUsesIndependentCheckpointAndDoesNotAdvanceOnFailure(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenEventLog(filepath.Join(dir, "events"), LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(context.Background(), Event{ID: "historical", Time: time.Now().UTC(), Type: "doc.read"}); err != nil {
		t.Fatal(err)
	}
	// An existing pipeline cursor must not suppress migration of retained events
	// into a newly introduced projection.
	if err := os.WriteFile(filepath.Join(dir, "pipeline.checkpoint"), []byte(`{"event_id":"historical"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteAggregationStore(filepath.Join(dir, "views.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	index := &sqliteEventIndex{db: store.db, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go index.run(ctx, log, filepath.Join(dir, "event-index.checkpoint"), nil)
	deadline := time.Now().Add(time.Second)
	for !index.caughtUp.Load() {
		if time.Now().After(deadline) {
			t.Fatal("independent event index did not catch up")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-index.done
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM analytics_events WHERE id='historical'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical projection count=%d err=%v", count, err)
	}
	store.Close()

	failed := &sqliteEventIndex{db: store.db, done: make(chan struct{})}
	failedCtx, failedCancel := context.WithCancel(context.Background())
	failedCheckpoint := filepath.Join(dir, "failed.checkpoint")
	go failed.run(failedCtx, log, failedCheckpoint, nil)
	time.Sleep(20 * time.Millisecond)
	failedCancel()
	<-failed.done
	if failed.caughtUp.Load() {
		t.Fatal("failed projection marked caught up")
	}
	if message := failed.lastError.Load(); message == nil || !strings.Contains(*message, "closed") {
		t.Fatalf("failed projection did not expose its error: %v", message)
	}
	if _, err := os.Stat(failedCheckpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed projection advanced checkpoint: %v", err)
	}
}

func TestWorkProcessorRetainsFollowupQueuedAsRunFinishes(t *testing.T) {
	p := newWorkProcessor()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx)
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	p.enqueueFollowup("facts", false, func(context.Context) {
		close(entered)
		<-release
		completed <- struct{}{}
	})
	<-entered
	p.enqueueFollowup("facts", false, func(context.Context) { completed <- struct{}{} })
	close(release)
	for range 2 {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("follow-up work was stranded")
		}
	}
}
