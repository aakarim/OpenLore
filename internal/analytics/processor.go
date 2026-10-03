package analytics

import (
	"context"
	"sync"
)

// maxPriorityStreak bounds how many priority items may run while background
// work waits. Continuously requested views must not starve event indexing,
// content facts, or refreshes of views that already have a result.
const maxPriorityStreak = 2

// workProcessor is the single bounded lane for content indexing and requested
// dashboard materializations. A key can be queued or running only once.
// Items run in arrival order within their class; priority items run first,
// but background items still get a turn after maxPriorityStreak priority runs.
type workProcessor struct {
	mu      sync.Mutex
	pending map[string]workItem
	queues  [2][]string // [0] priority, [1] background, in arrival order
	running map[string]struct{}
	streak  int
	wake    chan struct{}
	done    chan struct{}
	gate    chan struct{}
}

type workItem struct {
	key      string
	priority bool
	run      func(context.Context)
}

func newWorkProcessor(gates ...chan struct{}) *workProcessor {
	var gate chan struct{}
	if len(gates) > 0 {
		gate = gates[0]
	}
	return &workProcessor{pending: map[string]workItem{}, running: map[string]struct{}{}, wake: make(chan struct{}, 1), done: make(chan struct{}), gate: gate}
}

func (p *workProcessor) enqueue(key string, priority bool, run func(context.Context)) bool {
	return p.enqueueWithFollowup(key, priority, false, run)
}

func (p *workProcessor) enqueueFollowup(key string, priority bool, run func(context.Context)) bool {
	return p.enqueueWithFollowup(key, priority, true, run)
}

func queueIndex(priority bool) int {
	if priority {
		return 0
	}
	return 1
}

func (p *workProcessor) enqueueWithFollowup(key string, priority, followup bool, run func(context.Context)) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, running := p.running[key]
	if running && !followup {
		return false
	}
	if old, ok := p.pending[key]; ok {
		if priority && !old.priority {
			p.removeBackground(key)
			old.priority = true
			p.pending[key] = old
			p.queues[0] = append(p.queues[0], key)
		}
		return false
	}
	// A running key keeps one follow-up run. The active item may already have
	// observed its source queue empty; dropping this enqueue would strand work.
	p.pending[key] = workItem{key: key, priority: priority, run: run}
	p.queues[queueIndex(priority)] = append(p.queues[queueIndex(priority)], key)
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return !running
}

func (p *workProcessor) removeBackground(key string) {
	for i, queued := range p.queues[1] {
		if queued == key {
			p.queues[1] = append(p.queues[1][:i:i], p.queues[1][i+1:]...)
			return
		}
	}
}

func (p *workProcessor) pop() (workItem, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	class := 0
	if len(p.queues[0]) == 0 || len(p.queues[1]) > 0 && p.streak >= maxPriorityStreak {
		class = 1
	}
	for _, index := range []int{class, 1 - class} {
		if len(p.queues[index]) == 0 {
			continue
		}
		key := p.queues[index][0]
		p.queues[index] = p.queues[index][1:]
		item := p.pending[key]
		delete(p.pending, key)
		p.running[key] = struct{}{}
		if index == 0 {
			p.streak++
		} else {
			p.streak = 0
		}
		return item, true
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
				if p.gate != nil {
					select {
					case p.gate <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
				item.run(ctx)
				if p.gate != nil {
					<-p.gate
				}
				p.finish(item.key)
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}
