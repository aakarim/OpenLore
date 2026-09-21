package analytics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

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
	first, err := service.DashboardUsage(context.Background(), "same", compute)
	if err != nil || first.Analytics.State != "cold" || !first.Analytics.Updating || first.Reads != 0 {
		t.Fatalf("cold result = %#v, err=%v", first, err)
	}
	<-started
	for range 8 {
		result, err := service.DashboardUsage(context.Background(), "same", compute)
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
		result, err := service.DashboardUsage(context.Background(), "same", compute)
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
	go index.run(ctx, log, filepath.Join(dir, "event-index.checkpoint"))
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
	go failed.run(failedCtx, log, failedCheckpoint)
	time.Sleep(20 * time.Millisecond)
	failedCancel()
	<-failed.done
	if failed.caughtUp.Load() {
		t.Fatal("failed projection marked caught up")
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
