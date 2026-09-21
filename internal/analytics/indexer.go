package analytics

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sync"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

const (
	factsBatchSize      = 32
	maxIndexedFileBytes = 64 << 20
)

// factsIndexer persists its traversal queue in SQLite and processes only one
// bounded batch per processor turn. Requested dashboard work can therefore
// overtake warming without a second expensive worker competing for memory.
type factsIndexer struct {
	service *Service

	mu         sync.Mutex
	pending    map[string]struct{}
	scopes     []KnowledgeScope
	generation int64
}

func newFactsIndexer(service *Service, _ ...int) *factsIndexer {
	return &factsIndexer{service: service, pending: map[string]struct{}{}}
}

func (x *factsIndexer) setScopes(scopes []KnowledgeScope) error {
	return x.configureScopes(scopes, true)
}

func (x *factsIndexer) stageScopes(scopes []KnowledgeScope) error {
	return x.configureScopes(scopes, false)
}

func (x *factsIndexer) configureScopes(scopes []KnowledgeScope, schedule bool) error {
	x.mu.Lock()
	x.scopes = append([]KnowledgeScope(nil), scopes...)
	x.mu.Unlock()
	if schedule {
		return x.restart(context.Background())
	}
	generation, err := x.service.index.StartScan(context.Background(), scopes)
	if err == nil {
		x.mu.Lock()
		x.generation = generation
		x.mu.Unlock()
	}
	return err
}

func (x *factsIndexer) restart(ctx context.Context) error {
	x.mu.Lock()
	scopes := append([]KnowledgeScope(nil), x.scopes...)
	x.mu.Unlock()
	generation, err := x.service.index.StartScan(ctx, scopes)
	if err != nil {
		return err
	}
	x.mu.Lock()
	x.generation = generation
	x.pending = map[string]struct{}{}
	x.mu.Unlock()
	x.schedule(false)
	return nil
}

func (x *factsIndexer) resume() {
	state, err := x.service.index.ScanState(context.Background())
	if err != nil || state.Generation == 0 || state.State == "ready" {
		return
	}
	x.mu.Lock()
	x.generation = state.Generation
	x.mu.Unlock()
	x.schedule(false)
}

func (x *factsIndexer) enqueue(p string) {
	p = vfs.CleanPath(p)
	if x.service == nil {
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
		return
	}
	state, err := x.service.index.ScanState(context.Background())
	if err == nil && state.Generation > 0 && state.State == "updating" {
		if err := x.service.index.QueueScanPath(context.Background(), state.Generation, p); err == nil {
			x.schedule(false)
			return
		}
	}
	_ = x.restart(context.Background())
}

func (x *factsIndexer) schedule(priority bool) {
	x.service.processor.enqueueFollowup("facts", priority, x.run)
}

func (x *factsIndexer) run(ctx context.Context) {
	state, err := x.service.index.ScanState(ctx)
	if err != nil || state.Generation == 0 || state.State == "ready" {
		return
	}
	paths, err := x.service.index.NextScanPaths(ctx, state.Generation, factsBatchSize)
	if err != nil {
		_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
		return
	}
	for _, p := range paths {
		if err := x.scanPath(ctx, state.Generation, p); err != nil {
			_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
			return
		}
	}
	remaining, err := x.service.index.NextScanPaths(ctx, state.Generation, 1)
	if err != nil {
		_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
		return
	}
	if len(remaining) > 0 {
		x.schedule(false)
		return
	}
	if complete, err := x.service.index.FinishScan(ctx, state.Generation); err != nil {
		_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
	} else if !complete {
		x.schedule(false)
	}
}

func (x *factsIndexer) scanPath(ctx context.Context, generation int64, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := x.service.fs.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return x.service.index.CompleteScanPath(ctx, generation, p, nil)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", p, err)
	}
	if info.Dir {
		entries, err := x.service.fs.ReadDir(p)
		if err != nil {
			return fmt.Errorf("list %s: %w", p, err)
		}
		children := make([]string, 0, len(entries))
		for i := range entries {
			children = append(children, path.Join(p, entries[i].Name()))
		}
		return x.service.index.CompleteScanPath(ctx, generation, p, children)
	}
	if info.Size() > maxIndexedFileBytes {
		return fmt.Errorf("index %s: file exceeds %d byte analytics limit", p, maxIndexedFileBytes)
	}
	_, err = x.service.currentFacts(ctx, p, info, func() ([]byte, error) {
		content, err := x.service.fs.ReadFile(p)
		if err == nil && int64(len(content)) > maxIndexedFileBytes {
			return nil, fmt.Errorf("file exceeds %d byte analytics limit", maxIndexedFileBytes)
		}
		return content, err
	}, generation)
	if err != nil {
		return fmt.Errorf("index %s: %w", p, err)
	}
	return x.service.index.CompleteScanPath(ctx, generation, p, nil)
}
