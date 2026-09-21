package analytics

import (
	"context"
	"sync"
)

// workProcessor is the single bounded lane for content indexing and requested
// dashboard materializations. A key can be queued or running only once.
type workProcessor struct {
	mu      sync.Mutex
	pending map[string]workItem
	running map[string]struct{}
	wake    chan struct{}
	done    chan struct{}
}

type workItem struct {
	key      string
	priority bool
	run      func(context.Context)
}

func newWorkProcessor() *workProcessor {
	return &workProcessor{pending: map[string]workItem{}, running: map[string]struct{}{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (p *workProcessor) enqueue(key string, priority bool, run func(context.Context)) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.running[key]; ok {
		return false
	}
	if old, ok := p.pending[key]; ok {
		if priority && !old.priority {
			old.priority = true
			p.pending[key] = old
		}
		return false
	}
	p.pending[key] = workItem{key: key, priority: priority, run: run}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return true
}

func (p *workProcessor) pop() (workItem, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, priority := range []bool{true, false} {
		for key, item := range p.pending {
			if item.priority != priority {
				continue
			}
			delete(p.pending, key)
			p.running[key] = struct{}{}
			return item, true
		}
	}
	return workItem{}, false
}

func (p *workProcessor) finish(key string) {
	p.mu.Lock()
	delete(p.running, key)
	p.mu.Unlock()
}

func (p *workProcessor) active(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, pending := p.pending[key]
	_, running := p.running[key]
	return pending || running
}

func (p *workProcessor) run(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			for {
				item, ok := p.pop()
				if !ok {
					break
				}
				item.run(ctx)
				p.finish(item.key)
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}
