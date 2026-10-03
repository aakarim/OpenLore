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

type boundedFactsReader interface {
	ReadFileBounded(string, int64) ([]byte, error)
}

// factsIndexer persists its traversal queue in SQLite and processes only one
// bounded batch per processor turn. Requested dashboard work can therefore
// overtake warming without a second expensive worker competing for memory.
type factsIndexer struct {
	service *Service

	mu      sync.Mutex
	pending map[string]struct{}
	// dirty records paths queued while their current queue entry may already
	// be in flight, so completing that entry cannot discard the newer request.
	dirty      map[string]struct{}
	scopes     []KnowledgeScope
	generation int64
}

func newFactsIndexer(service *Service, _ ...int) *factsIndexer {
	return &factsIndexer{service: service, pending: map[string]struct{}{}, dirty: map[string]struct{}{}}
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
	return x.reconfigure(schedule)
}

// reconfigure applies the current scopes and content sources. Before Start,
// server construction is still registering providers, so start applies the
// final configuration once instead of rebuilding for each intermediate step.
func (x *factsIndexer) reconfigure(schedule bool) error {
	if !x.service.started.Load() {
		return nil
	}
	return x.start(schedule)
}

func (x *factsIndexer) start(schedule bool) error {
	x.mu.Lock()
	scopes := append([]KnowledgeScope(nil), x.scopes...)
	x.mu.Unlock()
	generation, err := x.service.index.StartScan(context.Background(), scopes, x.sources()...)
	if err != nil {
		return err
	}
	x.mu.Lock()
	x.generation = generation
	x.pending = map[string]struct{}{}
	x.mu.Unlock()
	if schedule {
		// A compatible ready generation runs only incremental work left queued
		// by a previous process; otherwise this starts or resumes the scan.
		x.schedule(false)
	}
	return nil
}

func (x *factsIndexer) sources() []string {
	x.service.providersMu.RLock()
	defer x.service.providersMu.RUnlock()
	return sourceNames(x.service.providers)
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
	x.mu.Lock()
	x.dirty[p] = struct{}{}
	x.mu.Unlock()
	state, err := x.service.index.ScanState(context.Background())
	if err == nil && state.Generation > 0 {
		if err := x.service.index.QueueScanPath(context.Background(), state.Generation, p); err == nil {
			x.schedule(false)
			return
		}
	}
	_ = x.reconfigure(true)
}

func (x *factsIndexer) schedule(priority bool) {
	x.service.processor.enqueueFollowup("facts", priority, x.run)
}

func (x *factsIndexer) run(ctx context.Context) {
	state, err := x.service.index.ScanState(ctx)
	if err != nil || state.Generation == 0 {
		return
	}
	paths, err := x.service.index.NextScanPaths(ctx, state.Generation, factsBatchSize)
	if err != nil {
		_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
		return
	}
	ready := state.State == "ready"
	if ready && len(paths) == 0 {
		return
	}
	for _, p := range paths {
		x.mu.Lock()
		delete(x.dirty, p)
		x.mu.Unlock()
		if err := x.scanPath(ctx, state.Generation, p); err != nil {
			_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
			return
		}
		x.mu.Lock()
		_, requeue := x.dirty[p]
		x.mu.Unlock()
		if requeue {
			if err := x.service.index.QueueScanPath(ctx, state.Generation, p); err != nil {
				_ = x.service.index.FailScan(context.WithoutCancel(ctx), state.Generation, err)
				return
			}
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
	if ready {
		// Incremental updates do not prune the completed generation.
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
	if x.service.excludedContent(p) {
		return x.service.index.CompleteScanPath(ctx, generation, p, nil, false)
	}
	info, err := x.service.fs.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		// Incremental deletes do not run generation pruning, so remove the
		// deleted file or subtree directly.
		if err := x.service.index.DeletePrefix(ctx, p); err != nil {
			return err
		}
		return x.service.index.CompleteScanPath(ctx, generation, p, nil, false)
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
		return x.service.index.CompleteScanPath(ctx, generation, p, children, false)
	}
	if info.Size() > maxIndexedFileBytes {
		// Large logs and other non-indexable content must not strand the whole
		// workspace scan. Remove any old facts rather than showing stale totals.
		if err := x.service.index.Delete(ctx, p); err != nil {
			return err
		}
		return x.service.index.CompleteScanPath(ctx, generation, p, nil, true)
	}
	reader, ok := x.service.fs.(boundedFactsReader)
	if !ok {
		return fmt.Errorf("index %s: filesystem does not support bounded analytics reads", p)
	}
	_, err = x.service.currentFacts(ctx, p, info, func() ([]byte, error) {
		content, err := reader.ReadFileBounded(p, maxIndexedFileBytes)
		if err == nil && int64(len(content)) > maxIndexedFileBytes {
			return nil, fmt.Errorf("file exceeds %d byte analytics limit", maxIndexedFileBytes)
		}
		return content, err
	}, generation)
	if err != nil {
		return fmt.Errorf("index %s: %w", p, err)
	}
	return x.service.index.CompleteScanPath(ctx, generation, p, nil, false)
}
