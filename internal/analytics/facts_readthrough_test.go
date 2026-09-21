package analytics

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

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
	if err := service.index.Upsert(context.Background(), IndexedFacts{Path: "/deleted.md", Size: 1, MTimeNS: 1, ContentHash: "old", Sources: map[string]map[string]float64{"size": {"bytes": 1}}}); err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, hit, err := service.index.Lookup(context.Background(), "/deleted.md", 1, 1, []string{"size"})
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
