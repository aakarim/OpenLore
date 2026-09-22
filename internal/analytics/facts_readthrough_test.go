package analytics

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type controlledFactsFS struct {
	testFS
	mu       sync.Mutex
	reads    int
	blockAt  int
	entered  chan struct{}
	release  chan struct{}
	failPath string
	large    string
}

type failingUpsertIndex struct {
	FactsIndex
	mu   sync.Mutex
	fail bool
}

func (x *failingUpsertIndex) Upsert(ctx context.Context, fact IndexedFacts) error {
	x.mu.Lock()
	fail := x.fail
	x.mu.Unlock()
	if fail {
		return errors.New("injected upsert failure")
	}
	return x.FactsIndex.Upsert(ctx, fact)
}

func (x *failingUpsertIndex) setFail(fail bool) {
	x.mu.Lock()
	x.fail = fail
	x.mu.Unlock()
}

func (f *controlledFactsFS) Stat(p string) (*vfs.FileInfo, error) {
	if vfs.CleanPath(p) == f.large {
		return &vfs.FileInfo{FileName: path.Base(p), FilePath: p, FileSize: maxIndexedFileBytes + 1}, nil
	}
	return f.testFS.Stat(p)
}

func (f *controlledFactsFS) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	read := f.reads
	f.mu.Unlock()
	if vfs.CleanPath(p) == f.failPath {
		return nil, fs.ErrPermission
	}
	if read == f.blockAt {
		close(f.entered)
		<-f.release
	}
	return f.testFS.ReadFile(p)
}

func (f *controlledFactsFS) ReadFileBounded(p string, maxBytes int64) ([]byte, error) {
	b, err := f.ReadFile(p)
	if err == nil && int64(len(b)) > maxBytes {
		return nil, errors.New("file exceeds read limit")
	}
	return b, err
}

func (f *controlledFactsFS) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

type countingFactsFS struct {
	testFS
	mu    sync.Mutex
	reads int
}

func (f *countingFactsFS) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	return f.testFS.ReadFile(p)
}

func (f *countingFactsFS) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

type fixedTokenizer struct {
	name  string
	count int
}

func (t fixedTokenizer) Name() string     { return t.name }
func (t fixedTokenizer) Count([]byte) int { return t.count }

type laterProvider struct{}

func (laterProvider) Name() string { return "later" }
func (laterProvider) Scalars(string, []byte) map[string]float64 {
	return map[string]float64{"readability": 42}
}

type mutableFactsFS struct {
	data     []byte
	mtime    time.Time
	reads    int
	vanished bool
}

func (f *mutableFactsFS) Stat(p string) (*vfs.FileInfo, error) {
	return &vfs.FileInfo{FileName: path.Base(p), FilePath: p, FileSize: int64(len(f.data)), FileModTime: f.mtime}, nil
}
func (f *mutableFactsFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, nil }
func (f *mutableFactsFS) ReadFile(string) ([]byte, error) {
	f.reads++
	if f.vanished {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), f.data...), nil
}

func newIndexedTestService(t *testing.T, fs vfs.FileSystem) *Service {
	t.Helper()
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, Deps{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	return service
}

func TestContentFactsTokenizerSwitchReplacesCompatibleProjection(t *testing.T) {
	fsys := &countingFactsFS{testFS: testFS{"/a.md": []byte("abcd")}}
	service := newIndexedTestService(t, fsys)
	facts := func() DocScalars {
		doc, err := service.Facts().Stat(context.Background(), "/a.md")
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	if got := facts(); got.Scalars["tokens"] != 1 || fsys.readCount() != 1 {
		t.Fatalf("initial facts=%#v reads=%d", got.Scalars, fsys.readCount())
	}
	service.SetTokenizer(fixedTokenizer{name: "exact", count: 7})
	if got := facts(); got.Scalars["tokens"] != 7 || fsys.readCount() != 2 {
		t.Fatalf("switched facts=%#v reads=%d", got.Scalars, fsys.readCount())
	}
	service.SetTokenizer(ApproxTokenizer())
	if got := facts(); got.Scalars["tokens"] != 1 || fsys.readCount() != 3 {
		t.Fatalf("switch-back did not rebuild compatible facts: facts=%#v reads=%d", got.Scalars, fsys.readCount())
	}
}

func TestContentFactsProviderAddedLaterReadsOnceAndPreservesBuiltins(t *testing.T) {
	fsys := &countingFactsFS{testFS: testFS{"/a.md": []byte("one two")}}
	service := newIndexedTestService(t, fsys)
	first, err := service.Facts().Stat(context.Background(), "/a.md")
	if err != nil {
		t.Fatal(err)
	}
	service.AddContentScalarProvider(laterProvider{})
	second, err := service.Facts().Stat(context.Background(), "/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if fsys.readCount() != 2 || first.Scalars["words"] != second.Scalars["words"] || second.Scalars["readability"] != 42 {
		t.Fatalf("first=%#v second=%#v reads=%d", first.Scalars, second.Scalars, fsys.readCount())
	}
}

func TestContentFactsSameSizeNewMTimeRecomputes(t *testing.T) {
	fsys := &mutableFactsFS{data: []byte("same"), mtime: time.Unix(1, 0)}
	service := newIndexedTestService(t, fsys)
	if _, err := service.Facts().Stat(context.Background(), "/a.md"); err != nil {
		t.Fatal(err)
	}
	fsys.mtime = time.Unix(2, 0)
	if _, err := service.Facts().Stat(context.Background(), "/a.md"); err != nil {
		t.Fatal(err)
	}
	if fsys.reads != 2 {
		t.Fatalf("reads=%d, want recompute after mtime change", fsys.reads)
	}
}

func TestContentFactsErrNotExistDeletesIndexedRow(t *testing.T) {
	fsys := &mutableFactsFS{data: []byte("same"), mtime: time.Unix(1, 0)}
	service := newIndexedTestService(t, fsys)
	if _, err := service.Facts().Stat(context.Background(), "/a.md"); err != nil {
		t.Fatal(err)
	}
	fsys.mtime = time.Unix(2, 0)
	fsys.vanished = true
	if _, err := service.Facts().Stat(context.Background(), "/a.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error=%v", err)
	}
	if _, hit, err := service.index.Lookup(context.Background(), "/a.md", 4, time.Unix(1, 0).UnixNano(), []string{"size"}); err != nil || hit {
		t.Fatalf("vanished row hit=%v err=%v", hit, err)
	}
}

func TestContentFactsReadsRawFilesystemOnMiss(t *testing.T) {
	raw := testFS{"/SKILL.md": []byte("raw")}
	service := newIndexedTestService(t, raw)
	scoped := transformedFactsFS{FileSystem: raw}
	doc, err := service.NewContentFacts(scoped).Stat(context.Background(), "/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Scalars["bytes"] != 3 {
		t.Fatalf("bytes=%v, want raw length 3", doc.Scalars["bytes"])
	}
}

type transformedFactsFS struct{ vfs.FileSystem }

func (f transformedFactsFS) ReadFile(p string) ([]byte, error) {
	b, err := f.FileSystem.ReadFile(p)
	return append(b, []byte("-injected")...), err
}

func TestIndexerStartupWarmPrunesDeletedRows(t *testing.T) {
	service := newIndexedTestService(t, testFS{})
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
	if err := service.index.Upsert(context.Background(), IndexedFacts{Path: "/docs/deleted.md", Owner: "docs", Size: 1, MTimeNS: 1, ContentHash: "old", Sources: map[string]map[string]float64{"size": {"bytes": 1}}}); err != nil {
		t.Fatal(err)
	}
	service.SetKnowledgeScopes(nil)
	service.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, hit, err := service.index.Lookup(context.Background(), "/docs/deleted.md", 1, 1, []string{"size"})
		if err != nil {
			t.Fatal(err)
		}
		if !hit {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup warm did not prune deleted row")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIndexerQueueDropsDescendantsOfQueuedAncestor(t *testing.T) {
	x := newFactsIndexer(nil, 1)
	x.enqueue("/docs/a.md")
	x.enqueue("/docs")
	x.enqueue("/docs/b.md")
	if len(x.pending) != 1 {
		t.Fatalf("pending=%v", x.pending)
	}
	if _, ok := x.pending["/docs"]; !ok {
		t.Fatalf("ancestor not retained: %v", x.pending)
	}
}

func TestFactsWarmingIsPartialBoundedAndYieldsToPriorityWork(t *testing.T) {
	files := testFS{}
	for i := 0; i < 80; i++ {
		files[fmt.Sprintf("/docs/%03d.md", i)] = []byte("small content\n")
	}
	fsys := &controlledFactsFS{testFS: files, blockAt: 2, entered: make(chan struct{}), release: make(chan struct{})}
	service := newIndexedTestService(t, fsys)
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
	service.Start(context.Background())
	<-fsys.entered
	rows, status, err := service.IndexedFacts(context.Background(), "/docs", 100)
	if err != nil || status.Complete || status.State != "updating" || len(rows) == 0 {
		t.Fatalf("mid-build rows=%d status=%+v err=%v", len(rows), status, err)
	}
	if status.Progress == nil || status.Progress.Phase != "content" || status.Progress.Processed != 2 || status.Progress.Unit != "paths" {
		t.Fatalf("mid-build progress=%+v", status.Progress)
	}
	priorityRan := make(chan struct{})
	service.processor.enqueue("requested-view", true, func(context.Context) { close(priorityRan) })
	close(fsys.release)
	select {
	case <-priorityRan:
	case <-time.After(2 * time.Second):
		t.Fatal("requested work was starved by warming")
	}
	if reads := fsys.readCount(); reads > factsBatchSize+1 {
		t.Fatalf("priority ran only after %d file reads, batch limit=%d", reads, factsBatchSize)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, status, err = service.IndexedFacts(context.Background(), "/docs", 100)
		if err == nil && status.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("warming did not complete: %+v err=%v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFactsScanReportsUnreadableAndSkipsOversizedFiles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*controlledFactsFS)
		want      string
	}{
		{name: "unreadable", configure: func(f *controlledFactsFS) { f.failPath = "/docs/bad.md" }, want: "permission denied"},
		{name: "oversized", configure: func(f *controlledFactsFS) { f.large = "/docs/bad.md" }, want: "Files over 64 MiB omitted from workspace knowledge totals: 1."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := &controlledFactsFS{testFS: testFS{"/docs/bad.md": []byte("data"), "/docs/later.md": []byte("valid")}}
			tc.configure(fsys)
			service := newIndexedTestService(t, fsys)
			service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
			if err := service.index.Upsert(context.Background(), IndexedFacts{Path: "/docs/bad.md", Owner: "docs", Size: 4, Sources: map[string]map[string]float64{"size": {"bytes": 4}}}); err != nil {
				t.Fatal(err)
			}
			service.Start(context.Background())
			deadline := time.Now().Add(2 * time.Second)
			for {
				rows, status, err := service.IndexedFacts(context.Background(), "/docs", 10)
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "oversized" && status.State == "ready" {
					if !status.Complete || status.Error != "" || !strings.Contains(status.Warning, tc.want) || len(rows) != 1 || rows[0].Path != "/docs/later.md" {
						t.Fatalf("oversized file blocked scan or retained old facts: rows=%+v status=%+v", rows, status)
					}
					if fsys.readCount() != 1 {
						t.Fatal("oversized file was read into memory")
					}
					_, totals, _, err := service.IndexedFactsForOwners(context.Background(), "/docs", []string{"docs"}, 10)
					if err != nil || totals.Files != 1 || totals.Bytes != 5 {
						t.Fatalf("stale oversized totals retained: %+v err=%v", totals, err)
					}
					break
				}
				if tc.name == "unreadable" && status.State == "failed" {
					if status.Complete || !strings.Contains(status.Error, tc.want) {
						t.Fatalf("failed status=%+v", status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("scan failure not surfaced: %+v", status)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestInternalContentExclusionsInvalidateOldFactsAndPendingWork(t *testing.T) {
	ctx := context.Background()
	fsys := &controlledFactsFS{testFS: testFS{
		"/data/history/commits.jsonl": []byte("internal journal"),
		"/data/small.json":            []byte("internal metadata"),
		"/data-guide/note.md":         []byte("keep"),
		"/history/guide.md":           []byte("history docs"),
	}}
	service := newIndexedTestService(t, fsys)
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "workspace", Root: "/"}})
	info, _ := fsys.Stat("/data/history/commits.jsonl")
	if _, err := service.CurrentFacts(ctx, info.FilePath, info, func() ([]byte, error) { return fsys.ReadFile(info.FilePath) }); err != nil {
		t.Fatal(err)
	}
	old, _ := service.index.ScanState(ctx)
	if err := service.index.QueueScanPath(ctx, old.Generation, "/data/history/commits.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := service.index.FailScan(ctx, old.Generation, errors.New("oversized journal")); err != nil {
		t.Fatal(err)
	}
	// This is a content-policy change, even though docset ownership is unchanged.
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "workspace", Root: "/", Exclude: []string{"/data"}}})
	state, _ := service.index.ScanState(ctx)
	if state.Generation <= old.Generation || state.OwnershipCompatible {
		t.Fatalf("exclusions reused incompatible cached work: %+v", state)
	}
	rows, totals, status, err := service.IndexedFactsForOwners(ctx, "/", []string{"workspace"}, 10)
	if err != nil || len(rows) != 0 || totals.Files != 0 || status.Complete {
		t.Fatalf("internal cached totals remained visible: rows=%v totals=%+v status=%+v err=%v", rows, totals, status, err)
	}
	fsys.large, fsys.failPath = "/data/history/commits.jsonl", "/data/small.json"
	reads := fsys.readCount()
	for i := 0; i < 10; i++ {
		service.indexer.run(ctx)
		rows, totals, status, err = service.IndexedFactsForOwners(ctx, "/", []string{"workspace"}, 10)
		if err == nil && status.Complete {
			break
		}
	}
	if err != nil || !status.Complete || status.Warning != "" || len(rows) != 2 || totals.Files != 2 || totals.Bytes != 16 || fsys.readCount()-reads != 2 {
		t.Fatalf("internal files were scanned or real content omitted: rows=%v totals=%+v status=%+v reads=%d err=%v", rows, totals, status, fsys.readCount()-reads, err)
	}
	if stale, err := service.index.PrefixScan(ctx, "/data"); err != nil || len(stale) != 0 {
		t.Fatalf("internal facts were not pruned: %v err=%v", stale, err)
	}
	service.EnqueueFacts("/data/history/commits.jsonl")
	service.PromoteFacts("/data")
	if after, err := service.index.ScanState(ctx); err != nil || after.Generation != state.Generation || after.State != "ready" {
		t.Fatalf("internal content scheduled another scan: %+v err=%v", after, err)
	}
	if _, err := service.CurrentFacts(ctx, info.FilePath, info, func() ([]byte, error) {
		t.Fatal("excluded preview invoked its content reader")
		return nil, nil
	}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("excluded preview: %v", err)
	}
	for _, facts := range []ContentFacts{service.Facts(), service.NewContentFacts(fsys)} {
		if _, err := facts.Stat(ctx, "/data/small.json"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("excluded stat: %v", err)
		}
		var paths []string
		err := facts.Walk(ctx, "/", WalkOptions{StatOnly: true}, func(doc DocScalars) error {
			paths = append(paths, doc.Path)
			return nil
		})
		sort.Strings(paths)
		if err != nil || len(paths) != 2 || paths[0] != "/data-guide/note.md" || paths[1] != "/history/guide.md" {
			t.Fatalf("content walk=%v err=%v", paths, err)
		}
	}
}

func TestKnowledgeScopeChangeImmediatelyInvalidatesGeneration(t *testing.T) {
	service := newIndexedTestService(t, testFS{"/one/a.md": []byte("one"), "/two/b.md": []byte("two")})
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "one", Root: "/one"}})
	service.Start(context.Background())
	waitReady := func() SnapshotStatus {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			_, status, err := service.IndexedFacts(context.Background(), "/", 10)
			if err == nil && status.Complete {
				return status
			}
			if time.Now().After(deadline) {
				t.Fatalf("generation did not complete: %+v %v", status, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	first := waitReady()
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "two", Root: "/two"}})
	_, changing, err := service.IndexedFacts(context.Background(), "/", 10)
	if err != nil || changing.Complete || changing.State != "updating" || !changing.ComputedAt.Equal(first.ComputedAt) {
		t.Fatalf("scope change served as complete: first=%+v changing=%+v err=%v", first, changing, err)
	}
	waitReady()
	rows, _, err := service.IndexedFacts(context.Background(), "/", 10)
	if err != nil || len(rows) != 1 || rows[0].Owner != "two" {
		t.Fatalf("runtime scope rebuild rows=%+v err=%v", rows, err)
	}
}

func TestPathSensitiveFactsAreFilteredInBoundedBackgroundBatches(t *testing.T) {
	files := testFS{}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("/docs/%03d.md", i)] = []byte("x")
	}
	service := newIndexedTestService(t, files)
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
	service.Start(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, status, err := service.IndexedFacts(context.Background(), "/docs", 1)
		if err == nil && status.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("facts did not warm: %+v %v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
	readable := func(p string) bool { return p == "/docs/039.md" }
	rows, _, status, err := service.AuthorizedIndexedFacts(context.Background(), "policy-a", "/docs", nil, []string{"docs"}, 100, readable)
	if err != nil || status.Complete || !status.Updating || len(rows) != 0 {
		t.Fatalf("cold filtered view rows=%d status=%+v err=%v", len(rows), status, err)
	}
	for {
		rows, _, status, err = service.AuthorizedIndexedFacts(context.Background(), "policy-a", "/docs", nil, []string{"docs"}, 100, readable)
		if err == nil && status.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("filtered view did not complete: %+v %v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
	if len(rows) != 1 || rows[0].Path != "/docs/039.md" {
		t.Fatalf("filtered rows=%+v", rows)
	}
}

func TestFactsScanDoesNotPublishAfterUpsertFailureAndRecovers(t *testing.T) {
	service := newIndexedTestService(t, testFS{"/docs/a.md": []byte("content")})
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
	base := service.index
	failing := &failingUpsertIndex{FactsIndex: base, fail: true}
	service.index = failing
	service.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	var failed SnapshotStatus
	for {
		_, failed, _ = service.IndexedFacts(context.Background(), "/docs", 10)
		if failed.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("upsert failure was not published: %+v", failed)
		}
		time.Sleep(time.Millisecond)
	}
	if failed.Complete || !strings.Contains(failed.Error, "injected upsert failure") {
		t.Fatalf("failure was misleading: %+v", failed)
	}
	state, err := base.ScanState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	queued, err := base.NextScanPaths(context.Background(), state.Generation, 10)
	if err != nil || len(queued) == 0 || queued[0] != "/docs/a.md" {
		t.Fatalf("failed file did not remain durable work: %v err=%v", queued, err)
	}
	failing.setFail(false)
	service.PromoteFacts("/docs/a.md")
	deadline = time.Now().Add(2 * time.Second)
	for {
		rows, status, err := service.IndexedFacts(context.Background(), "/docs", 10)
		if err == nil && status.Complete {
			if len(rows) != 1 || rows[0].Path != "/docs/a.md" {
				t.Fatalf("recovered rows=%+v", rows)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not recover: %+v err=%v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIncompatibleOwnershipDoesNotServeOrScheduleFilteredFacts(t *testing.T) {
	ctx := context.Background()
	service := newIndexedTestService(t, testFS{})
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
	store := service.store.(*SQLiteAggregationStore)
	state, err := service.index.ScanState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.index.Upsert(ctx, IndexedFacts{Path: "/docs/private.md", Owner: "docs", Size: 7, Generation: state.Generation - 1, Sources: map[string]map[string]float64{"size": {"bytes": 7}}}); err != nil {
		t.Fatal(err)
	}
	// Seed an existing filter result as well as testing a brand-new view.
	if _, err := store.db.Exec(`INSERT INTO dashboard_fact_state(key,generation,state) VALUES('cached',?,'ready'); INSERT INTO dashboard_fact_paths(key,path) VALUES('cached','/docs/private.md')`, state.Generation); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cached", "new"} {
		rows, totals, status, err := service.AuthorizedIndexedFacts(ctx, key, "/docs", nil, []string{"docs"}, 10, func(string) bool {
			t.Error("incompatible ownership must not be filtered")
			return true
		})
		if err != nil || len(rows) != 0 || totals.Files != 0 || totals.Bytes != 0 || status.Complete {
			t.Fatalf("%s: rows=%+v totals=%+v status=%+v err=%v", key, rows, totals, status, err)
		}
		if service.processor.active("facts-filter:" + key) {
			t.Fatalf("%s: queued incompatible filter work", key)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM dashboard_fact_state`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("initialized incompatible filter: count=%d err=%v", count, err)
	}
}

func TestDisabledAnalyticsDoesNotInitializePathFilterWork(t *testing.T) {
	disabled := false
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Enabled: &disabled}}, Deps{FS: testFS{"/docs/a.md": []byte("a")}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	service.SetKnowledgeScopes([]KnowledgeScope{{Name: "docs", Root: "/docs"}})
	service.Start(context.Background())
	_, _, status, err := service.AuthorizedIndexedFacts(context.Background(), "disabled-policy", "/docs", nil, []string{"docs"}, 10, func(string) bool { return true })
	if err != nil || status.State != "disabled" || status.Updating {
		t.Fatalf("disabled filtered status=%+v err=%v", status, err)
	}
	store := service.store.(*SQLiteAggregationStore)
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM dashboard_fact_state WHERE key='disabled-policy'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled filtering initialized work: count=%d err=%v", count, err)
	}
}
