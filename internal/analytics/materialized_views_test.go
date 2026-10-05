package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

type sourceFunc func(context.Context, EventFilter, func(Event) error) error

func (f sourceFunc) Scan(ctx context.Context, filter EventFilter, fn func(Event) error) error {
	return f(ctx, filter, fn)
}

func testUsageSummary(ctx context.Context, db *sql.DB, key string, since, until time.Time, source EventSource, charsPerToken int, deadline time.Time) (Summary, bool, error) {
	partial, done, err := usageDayPartials(db, key, source, charsPerToken).summarize(ctx, since, until, deadline)
	if err != nil || !done {
		return Summary{}, done, err
	}
	return partial.(*usagePartial).summary(), true, nil
}

// materializedView requests the non-incremental test aggregation "view".
func materializedView(t *testing.T, service *Service, window time.Duration) Materialized {
	t.Helper()
	if service.registry.Status("view") != StatusOK {
		if err := service.RegisterAggregations([]Aggregation{{Name: "view", Compute: func(context.Context, EventSource, ContentFacts, Params) (Table, error) { return Table{}, nil }}}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	view, err := service.DashboardMaterialized(context.Background(), "view", "view", Params{Since: now.Add(-window), Until: now}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

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
				if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,value,computed_at,error) VALUES('test:2592000',?,?,?)`, value, time.Now().UnixNano(), lastError); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.DashboardUsage(context.Background(), "test", 30*24*time.Hour, nil, 4)
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
	for _, key := range []string{"usage:604800", "aggregation:view:604800:0"} {
		if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,computed_at,error) VALUES(?,?,'build failed')`, key, time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, nil, 4)
	if err != nil || usage.Analytics.State != "failed" || usage.Analytics.Updating {
		t.Fatalf("usage retried immediately: %+v err=%v", usage.Analytics, err)
	}
	if view := materializedView(t, service, 7*24*time.Hour); view.Analytics.State != "failed" || view.Analytics.Updating {
		t.Fatalf("aggregation retried immediately: %+v", view.Analytics)
	}
	if service.processor.active("usage:usage:604800") || service.processor.active("aggregation:view:604800:0") {
		t.Fatal("failed view was queued during backoff")
	}
	old := time.Now().Add(-2 * dashboardRetryInterval).UnixNano()
	if _, err := store.db.Exec(`UPDATE dashboard_views SET computed_at=?`, old); err != nil {
		t.Fatal(err)
	}
	if usage, _ := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, nil, 4); !usage.Analytics.Updating {
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
	if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,computed_at,error) VALUES('usage:604800',?,'first failure')`, old); err != nil {
		t.Fatal(err)
	}
	fail := sourceFunc(func(context.Context, EventFilter, func(Event) error) error { return errors.New("second failure") })
	first, err := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, fail, 4)
	if err != nil || !first.Analytics.Updating {
		t.Fatalf("expired backoff did not retry: %+v err=%v", first.Analytics, err)
	}
	item, ok := service.processor.pop()
	if !ok {
		t.Fatal("retry was not queued")
	}
	item.run(context.Background())
	service.processor.finish(item.key)
	second, err := service.DashboardUsage(context.Background(), "usage", 7*24*time.Hour, fail, 4)
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
	var once sync.Once
	var runs atomic.Int32
	reads := summarySource{}
	for i := range 7 {
		reads = append(reads, Event{ID: fmt.Sprint(i), Time: time.Now().Add(-time.Hour), Type: "doc.read"})
	}
	source := sourceFunc(func(ctx context.Context, filter EventFilter, fn func(Event) error) error {
		once.Do(func() {
			close(started)
			<-release
		})
		if time.Since(filter.To) < time.Minute {
			runs.Add(1) // each run ends with one scan of the unsettled tail
		}
		return reads.Scan(ctx, filter, fn)
	})
	first, err := service.DashboardUsage(context.Background(), "same", 30*24*time.Hour, source, 4)
	if err != nil || first.Analytics.State != "cold" || !first.Analytics.Updating || first.Reads != 0 {
		t.Fatalf("cold result = %#v, err=%v", first, err)
	}
	<-started
	for range 8 {
		result, err := service.DashboardUsage(context.Background(), "same", 30*24*time.Hour, source, 4)
		if err != nil || result.Reads != 0 || !result.Analytics.Updating {
			t.Fatalf("in-flight result = %#v, err=%v", result, err)
		}
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := service.DashboardUsage(context.Background(), "same", 30*24*time.Hour, source, 4)
		if err != nil {
			t.Fatal(err)
		}
		if result.Reads == 7 && result.Analytics.Complete {
			if runs.Load() != 1 {
				t.Fatalf("duplicate usage runs = %d", runs.Load())
			}
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
	compute := sourceFunc(func(context.Context, EventFilter, func(Event) error) error {
		t.Fatal("usage must wait for catch-up")
		return nil
	})
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
			result, err := service.DashboardUsage(ctx, "progress", 365*24*time.Hour, compute, 4)
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
	result, err := service.DashboardUsage(ctx, "recent", 7*24*time.Hour, sourceFunc(func(context.Context, EventFilter, func(Event) error) error {
		computed = true
		return nil
	}), 4)
	if err != nil || result.Analytics.Coverage == "durable event index is catching up" {
		t.Fatalf("recent range waited for old history: %+v err=%v", result.Analytics, err)
	}
	old, err := service.DashboardUsage(ctx, "old", 365*24*time.Hour, sourceFunc(func(context.Context, EventFilter, func(Event) error) error {
		t.Fatal("old range computed before its history was indexed")
		return nil
	}), 4)
	if err != nil || old.Analytics.Coverage != "durable event index is catching up" {
		t.Fatalf("old range=%+v err=%v", old.Analytics, err)
	}
	item, ok := service.processor.pop()
	if !ok || item.key != "usage:recent:604800" {
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

func TestWorkProcessorRunsInArrivalOrderAndDoesNotStarveBackgroundWork(t *testing.T) {
	p := newWorkProcessor()
	var ran []string
	record := func(key string) func(context.Context) {
		return func(context.Context) { ran = append(ran, key) }
	}
	p.enqueue("background-a", false, record("background-a"))
	p.enqueue("background-b", false, record("background-b"))
	for _, key := range []string{"view-1", "view-2", "view-3", "view-4", "view-5"} {
		p.enqueue(key, true, record(key))
	}
	// A pending background item promoted by a request moves to the priority
	// queue rather than keeping its background position.
	p.enqueue("background-b", true, record("ignored"))
	for {
		item, ok := p.pop()
		if !ok {
			break
		}
		item.run(context.Background())
		p.finish(item.key)
	}
	want := []string{"view-1", "view-2", "background-a", "view-3", "view-4", "view-5", "background-b"}
	if strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Fatalf("ran %v, want %v", ran, want)
	}
}

// indexEvents appends events to the service log and indexes all of them.
func indexEvents(t *testing.T, service *Service, events ...Event) {
	t.Helper()
	ctx := context.Background()
	for _, event := range events {
		if err := service.log.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	index := service.eventIndex
	if index.log == nil {
		index.log, index.cursor, index.checkpoint = service.log, logCursor{}, filepath.Join(t.TempDir(), "cursor")
	}
	if err := index.catchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if err := index.refreshTails(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUsageSummaryReusesSettledDaysAndRebuildsDaysWithLateEvents(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	store := service.store.(*SQLiteAggregationStore)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	var events []Event
	for day := 0; day < 10; day++ {
		at := today.Add(-time.Duration(day)*24*time.Hour + time.Hour)
		events = append(events,
			Event{ID: fmt.Sprintf("read-%d", day), Time: at, Type: "doc.read", Fields: map[string]any{"characters": 8, "actor_kind": "human"}},
			Event{ID: fmt.Sprintf("command-%d", day), Time: at, Type: "command.exec", Fields: map[string]any{"actor_kind": "agent"}},
		)
	}
	// One commit recorded as scalars late on one day and as a write just after
	// midnight still counts once, as a write, across the two cached days.
	midnight := today.Add(-3 * 24 * time.Hour)
	events = append(events,
		Event{ID: "scalars", Time: midnight.Add(-time.Second), Type: "doc.scalars", Fields: map[string]any{"commit_id": "c", "path": "/a.md", "writer": "agent"}},
		Event{ID: "write", Time: midnight.Add(time.Second), Type: "doc.write", Fields: map[string]any{"commit_id": "c", "path": "/a.md", "writer": "human"}},
	)
	indexEvents(t, service, events...)

	var scans []EventFilter
	source := sourceFunc(func(ctx context.Context, filter EventFilter, fn func(Event) error) error {
		scans = append(scans, filter)
		return service.eventIndex.Scan(ctx, filter, fn)
	})
	since := now.Add(-7 * 24 * time.Hour)
	want, err := UsageSummary(ctx, service.eventIndex, Params{Since: since, Until: now}, 4)
	if err != nil {
		t.Fatal(err)
	}
	same := func(got Summary) bool {
		got.ComputedAt, want.ComputedAt = time.Time{}, time.Time{}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		return string(a) == string(b)
	}
	got, done, err := testUsageSummary(ctx, store.db, "scope", since, now, source, 4, time.Now().Add(time.Minute))
	if err != nil || !done || !same(got) || got.Writes != 1 || got.HumanWrites != 1 {
		t.Fatalf("incremental=%+v done=%v err=%v want %+v", got, done, err, want)
	}
	first, last := settledDays(since, now)
	if len(scans) != int(last-first)+2 {
		t.Fatalf("cold build scanned %d ranges, want %d days plus both edges", len(scans), last-first)
	}

	scans = nil
	if got, done, err = testUsageSummary(ctx, store.db, "scope", since, now, source, 4, time.Now().Add(time.Minute)); err != nil || !done || !same(got) {
		t.Fatalf("refresh=%+v done=%v err=%v", got, done, err)
	}
	if len(scans) != 2 {
		t.Fatalf("refresh scanned %d ranges, want only the two unsettled edges", len(scans))
	}

	// A late event for a settled day invalidates that day only.
	late := today.Add(-4*24*time.Hour + 2*time.Hour)
	indexEvents(t, service, Event{ID: "late", Time: late, Type: "doc.read", Fields: map[string]any{"actor_kind": "human"}})
	if want, err = UsageSummary(ctx, service.eventIndex, Params{Since: since, Until: now}, 4); err != nil {
		t.Fatal(err)
	}
	scans = nil
	if got, done, err = testUsageSummary(ctx, store.db, "scope", since, now, source, 4, time.Now().Add(time.Minute)); err != nil || !done || !same(got) {
		t.Fatalf("after late event=%+v done=%v err=%v want %+v", got, done, err, want)
	}
	if len(scans) != 3 || !scans[0].From.Equal(late.Truncate(24*time.Hour)) {
		t.Fatalf("late event rebuilt %v, want its day plus both edges", scans)
	}
}

func TestUsageSummaryBuildsMissingDaysAcrossBoundedTurns(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	store := service.store.(*SQLiteAggregationStore)
	ctx := context.Background()
	now := time.Now().UTC()
	var dayScans int
	source := sourceFunc(func(ctx context.Context, filter EventFilter, fn func(Event) error) error {
		dayScans++
		return nil
	})
	since := now.Add(-30 * 24 * time.Hour)
	// An expired deadline still builds one day per turn.
	for turn := 1; ; turn++ {
		dayScans = 0
		_, done, err := testUsageSummary(ctx, store.db, "scope", since, now, source, 4, time.Now().Add(-time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if done {
			first, last := settledDays(since, now)
			if turn != int(last-first) {
				t.Fatalf("finished after %d turns, want one per day", turn)
			}
			break
		}
		if dayScans != 1 {
			t.Fatalf("turn %d scanned %d days", turn, dayScans)
		}
	}
}

func TestPublishedDashboardViewsRefreshInBackgroundWithoutUpdating(t *testing.T) {
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir()}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	service.eventIndex.caughtUp.Store(true)
	store := service.store.(*SQLiteAggregationStore)
	window := 30 * 24 * time.Hour
	old := time.Now().Add(-time.Hour).UnixNano()
	for _, key := range []string{"usage:2592000", "aggregation:view:2592000:0"} {
		if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,value,computed_at,error) VALUES(?,'{}',?,'')`, key, old); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := service.DashboardUsage(context.Background(), "usage", window, summarySource{}, 4)
	if err != nil || usage.Analytics.State != "ready" || usage.Analytics.Updating || !usage.Analytics.Complete {
		t.Fatalf("published usage=%+v err=%v", usage.Analytics, err)
	}
	if view := materializedView(t, service, window); view.Analytics.State != "ready" || view.Analytics.Updating || !view.Analytics.Complete {
		t.Fatalf("published view=%+v", view.Analytics)
	}
	// Refreshes are background work: a cold view requested later runs first.
	service.DashboardUsage(context.Background(), "cold", window, summarySource{}, 4)
	for _, want := range []string{"usage:cold:2592000", "usage:usage:2592000", "aggregation:view:2592000:0"} {
		item, ok := service.processor.pop()
		if !ok || item.key != want {
			t.Fatalf("popped %q, want %q", item.key, want)
		}
		service.processor.finish(item.key)
	}

	// A recent result of a long window is not rebuilt on every request.
	if _, err := store.db.Exec(`UPDATE dashboard_views SET computed_at=? WHERE key='aggregation:view:2592000:0'`, time.Now().Add(-5*time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	materializedView(t, service, window)
	if service.processor.active("aggregation:view:2592000:0") {
		t.Fatal("30-day aggregation refreshed within its refresh interval")
	}
}

func TestPublishedDashboardViewsStayReadyDuringHistoryCatchUp(t *testing.T) {
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir()}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	store := service.store.(*SQLiteAggregationStore)
	window := 30 * 24 * time.Hour
	for _, key := range []string{"usage:2592000", "aggregation:view:2592000:0"} {
		if _, err := store.db.Exec(`INSERT INTO dashboard_views(key,value,computed_at,error) VALUES(?,'{}',?,'')`, key, time.Now().Add(-time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if service.eventIndex.covers(window) {
		t.Fatal("history unexpectedly covered")
	}
	usage, err := service.DashboardUsage(context.Background(), "usage", window, nil, 4)
	if err != nil || usage.Analytics.State != "ready" || usage.Analytics.Updating || usage.Analytics.Progress != nil {
		t.Fatalf("published usage during catch-up=%+v err=%v", usage.Analytics, err)
	}
	if view := materializedView(t, service, window); view.Analytics.State != "ready" || view.Analytics.Updating || view.Analytics.Progress != nil {
		t.Fatalf("published view during catch-up=%+v", view.Analytics)
	}
	cold, err := service.DashboardUsage(context.Background(), "cold", window, nil, 4)
	if err != nil || cold.Analytics.State != "cold" || !cold.Analytics.Updating || cold.Analytics.Progress == nil {
		t.Fatalf("cold usage during catch-up=%+v err=%v", cold.Analytics, err)
	}
}

func TestUsageSummaryMatchesFullScanWhenTokenTotalOverflowsAcrossDays(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	store := service.store.(*SQLiteAggregationStore)
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	characters := math.Exp2(62)
	indexEvents(t, service,
		Event{ID: "one", Time: today.Add(-3*24*time.Hour + time.Hour), Type: "doc.read", Fields: map[string]any{"characters": characters}},
		Event{ID: "two", Time: today.Add(-2*24*time.Hour + time.Hour), Type: "doc.read", Fields: map[string]any{"characters": characters}},
	)
	since := now.Add(-7 * 24 * time.Hour)
	want, err := UsageSummary(ctx, service.eventIndex, Params{Since: since, Until: now}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // cold build, then from cached days
		got, done, err := testUsageSummary(ctx, store.db, "scope", since, now, service.eventIndex, 1, time.Now().Add(time.Minute))
		if err != nil || !done || got.EstimatedTokens != want.EstimatedTokens || got.EstimatedReads != 1 || got.UnestimatedReads != 1 {
			t.Fatalf("overflow summary=%+v done=%v err=%v want %+v", got, done, err, want)
		}
	}
}

func TestIncrementalAggregationViewReusesSettledDays(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	ctx := context.Background()
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	var events []Event
	for day := range 10 {
		events = append(events, Event{ID: fmt.Sprint(day), Time: today.Add(-time.Duration(day)*24*time.Hour + time.Hour), Type: "command.exec", Principal: "ann", Fields: map[string]any{"command": "cat"}})
	}
	indexEvents(t, service, events...)
	service.eventIndex.caughtUp.Store(true)
	scans := 0
	source := sourceFunc(func(ctx context.Context, filter EventFilter, fn func(Event) error) error {
		scans++
		return service.eventIndex.Scan(ctx, filter, fn)
	})
	params := Params{Since: now.Add(-7 * 24 * time.Hour), Until: now}
	build := func() Materialized {
		t.Helper()
		view, err := service.DashboardMaterialized(ctx, "scope", "top-commands", params, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		for {
			item, ok := service.processor.pop()
			if !ok {
				break
			}
			item.run(ctx)
			service.processor.finish(item.key)
		}
		return view
	}
	if cold := build(); cold.Analytics.State != "cold" || !cold.Analytics.Updating {
		t.Fatalf("cold view=%+v", cold.Analytics)
	}
	first, last := settledDays(params.Since, now)
	if scans != int(last-first)+2 {
		t.Fatalf("cold build scanned %d ranges, want %d days plus both edges", scans, last-first)
	}
	view := build()
	want := 0
	for _, event := range events {
		// Fixture events can be in the future during the first UTC hour.
		if !event.Time.Before(view.Window.Since) && !event.Time.After(view.Window.Until) {
			want++
		}
	}
	if view.Analytics.State != "ready" || len(view.Table.Rows) != 1 || view.Table.Rows[0][1] != float64(want) {
		t.Fatalf("published view=%+v rows=%v", view.Analytics, view.Table.Rows)
	}
	// Another window of the same scope reuses the cached days.
	scans = 0
	params.Since = now.Add(-3 * 24 * time.Hour)
	build()
	if scans != 2 {
		t.Fatalf("3-day build scanned %d ranges, want only the two unsettled edges", scans)
	}
}

func TestAggregationWindowsBeyondDayRetentionRebuildInFull(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	ctx := context.Background()
	indexEvents(t, service, Event{ID: "one", Time: time.Now().UTC().Add(-time.Hour), Type: "command.exec", Fields: map[string]any{"command": "cat"}})
	service.eventIndex.caughtUp.Store(true)
	now := time.Now()
	params := Params{Since: now.Add(-400 * 24 * time.Hour), Until: now}
	if _, err := service.DashboardMaterialized(ctx, "scope", "top-commands", params, service.eventIndex, nil); err != nil {
		t.Fatal(err)
	}
	for {
		item, ok := service.processor.pop()
		if !ok {
			break
		}
		item.run(ctx)
		service.processor.finish(item.key)
	}
	view, err := service.DashboardMaterialized(ctx, "scope", "top-commands", params, service.eventIndex, nil)
	if err != nil || view.Analytics.State != "ready" || len(view.Table.Rows) != 1 {
		t.Fatalf("400-day view=%+v rows=%v err=%v", view.Analytics, view.Table.Rows, err)
	}
	var days int
	if err := service.store.(*SQLiteAggregationStore).db.QueryRow(`SELECT COUNT(*) FROM dashboard_days`).Scan(&days); err != nil || days != 0 {
		t.Fatalf("400-day window cached %d days err=%v", days, err)
	}
}
