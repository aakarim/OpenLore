package analytics

import (
	"context"
	"testing"
	"time"
)

func testFactsIndex(t *testing.T) (*SQLiteAggregationStore, FactsIndex) {
	t.Helper()
	store, err := OpenSQLiteAggregationStore(t.TempDir() + "/aggregations.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, newSQLiteFactsIndex(store)
}

func TestFactsIndexLookupMetadataAndSources(t *testing.T) {
	_, index := testFactsIndex(t)
	ctx := context.Background()
	fact := IndexedFacts{Path: "/a.md", Size: 4, MTimeNS: 10, ContentHash: "one", Sources: map[string]map[string]float64{"size": {"bytes": 4}}}
	if err := index.Upsert(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := index.Lookup(ctx, "/a.md", 4, 10, []string{"size"}); err != nil || !hit {
		t.Fatalf("matching row was not a hit: hit=%v err=%v", hit, err)
	}
	if _, hit, _ := index.Lookup(ctx, "/a.md", 4, 11, []string{"size"}); hit {
		t.Fatal("same size with new mtime was a hit")
	}
	if _, hit, _ := index.Lookup(ctx, "/a.md", 4, 10, []string{"size", "new-tokenizer"}); hit {
		t.Fatal("row missing an active source was a hit")
	}
}

func TestFactsIndexChangedHashDropsInactiveScalars(t *testing.T) {
	store, index := testFactsIndex(t)
	ctx := context.Background()
	if err := index.Upsert(ctx, IndexedFacts{Path: "/a.md", Size: 3, MTimeNS: 1, ContentHash: "old", Sources: map[string]map[string]float64{"old-tokenizer": {"tokens": 99}, "size": {"bytes": 3}}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, IndexedFacts{Path: "/a.md", Size: 3, MTimeNS: 2, ContentHash: "new", Sources: map[string]map[string]float64{"size": {"bytes": 3}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM file_scalars WHERE path=? AND source=?`, "/a.md", "old-tokenizer").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale source count=%d err=%v", count, err)
	}
}

func TestFactsIndexReplacesInactiveScalarsWhenHashUnchanged(t *testing.T) {
	_, index := testFactsIndex(t)
	ctx := context.Background()
	first := IndexedFacts{Path: "/a.md", Size: 3, MTimeNS: 1, ContentHash: "same", Sources: map[string]map[string]float64{"old": {"tokens": 1}}}
	if err := index.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	first.MTimeNS = 2
	first.Sources = map[string]map[string]float64{"new": {"tokens": 2}}
	if err := index.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, hit, err := index.Lookup(ctx, "/a.md", 3, 2, []string{"new"})
	if err != nil || !hit || len(got.Sources["old"]) != 0 || got.Sources["new"]["tokens"] != 2 {
		t.Fatalf("inactive scalar was retained: %#v hit=%v err=%v", got.Sources, hit, err)
	}
}

func TestFactsIndexPrefixScanAndPrune(t *testing.T) {
	_, index := testFactsIndex(t)
	ctx := context.Background()
	for _, p := range []string{"/docs/a.md", "/docs/nested/b.md", "/other.md"} {
		if err := index.Upsert(ctx, IndexedFacts{Path: p, Size: 1, MTimeNS: 1, ContentHash: p, ComputedAt: time.Now(), Sources: map[string]map[string]float64{"size": {"bytes": 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := index.PrefixScan(ctx, "/docs")
	if err != nil || len(rows) != 2 || rows[0].Sources["size"]["bytes"] != 1 {
		t.Fatalf("prefix rows=%v err=%v", rows, err)
	}
	if err := index.Prune(ctx, map[string]struct{}{rows[0].Path: {}, rows[1].Path: {}}); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := index.Lookup(ctx, "/other.md", 1, 1, []string{"size"}); err != nil || hit {
		t.Fatalf("pruned row hit=%v err=%v", hit, err)
	}
}
