package analytics

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sync"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type factsIndexer struct {
	service *Service
	workers int

	mu      sync.Mutex
	pending map[string]struct{}
	wake    chan struct{}
	wg      sync.WaitGroup
}

func newFactsIndexer(service *Service, workers int) *factsIndexer {
	if workers <= 0 {
		workers = 2
	}
	return &factsIndexer{service: service, workers: workers, pending: map[string]struct{}{}, wake: make(chan struct{}, 1)}
}

func (x *factsIndexer) enqueue(p string) {
	p = vfs.CleanPath(p)
	x.mu.Lock()
	for queued := range x.pending {
		if pathWithinPrefix(p, queued) {
			x.mu.Unlock()
			return
		}
		if pathWithinPrefix(queued, p) {
			delete(x.pending, queued)
		}
	}
	x.pending[p] = struct{}{}
	x.mu.Unlock()
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

func (x *factsIndexer) pop() (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for p := range x.pending {
		delete(x.pending, p)
		return p, true
	}
	return "", false
}

func (x *factsIndexer) start(ctx context.Context) {
	for range x.workers {
		x.wg.Add(1)
		go func() {
			defer x.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-x.wake:
					for {
						p, ok := x.pop()
						if !ok {
							break
						}
						x.reconcile(ctx, p)
					}
				}
			}
		}()
	}
	x.enqueue("/")
}

func (x *factsIndexer) wait() { x.wg.Wait() }

func (x *factsIndexer) reconcile(ctx context.Context, prefix string) {
	seen := map[string]struct{}{}
	var walk func(string) bool
	walk = func(p string) bool {
		if ctx.Err() != nil {
			return false
		}
		info, err := x.service.fs.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		if err != nil {
			return false
		}
		if !info.Dir {
			seen[vfs.CleanPath(p)] = struct{}{}
			_, _ = x.service.CurrentFacts(ctx, p, info, func() ([]byte, error) { return x.service.fs.ReadFile(p) })
			return true
		}
		entries, err := x.service.fs.ReadDir(p)
		if err != nil {
			return errors.Is(err, fs.ErrNotExist)
		}
		for i := range entries {
			if !walk(path.Join(p, entries[i].Name())) {
				return false
			}
		}
		return true
	}
	if !walk(prefix) || ctx.Err() != nil {
		return
	}
	if prefix == "/" {
		_ = x.service.index.Prune(ctx, seen)
		return
	}
	rows, err := x.service.index.PrefixScan(ctx, prefix)
	if err != nil {
		return
	}
	if info, err := x.service.fs.Stat(prefix); err != nil || info.Dir {
		_ = x.service.index.Delete(ctx, prefix)
	}
	for _, row := range rows {
		if _, ok := seen[row.Path]; !ok {
			_ = x.service.index.Delete(ctx, row.Path)
		}
	}
}
