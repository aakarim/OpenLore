package analytics

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type PipelineOptions struct {
	Processors []Processor
	Consumers  []Consumer
	Refresher  *Refresher
	Sink       Sink
	Buffer     int
}
type Pipeline struct {
	log        EventLog
	checkpoint string
	opts       PipelineOptions
	handoff    chan Event
	seen       sync.Map
	cancel     context.CancelFunc
	done       chan struct{}
	once       sync.Once
}

func NewPipeline(log EventLog, checkpoint string, opts PipelineOptions) *Pipeline {
	if opts.Buffer <= 0 {
		opts.Buffer = 1024
	}
	return &Pipeline{log: log, checkpoint: checkpoint, opts: opts, handoff: make(chan Event, opts.Buffer), done: make(chan struct{})}
}
func (p *Pipeline) Handoff() chan<- Event { return p.handoff }
func (p *Pipeline) handle(ctx context.Context, e Event) {
	if _, loaded := p.seen.LoadOrStore(e.ID, true); loaded {
		return
	}
	for _, processor := range p.opts.Processors {
		for _, derived := range processor.Process(ctx, e) {
			if p.opts.Sink != nil {
				p.opts.Sink.Record(ctx, derived)
			}
			p.seen.Store(derived.ID, true)
			for _, consumer := range p.opts.Consumers {
				consumer.Consume(ctx, derived)
			}
		}
	}
	for _, consumer := range p.opts.Consumers {
		consumer.Consume(ctx, e)
	}
	p.writeCheckpoint(e.ID)
}
func (p *Pipeline) consumePersisted(ctx context.Context, e Event) {
	p.seen.Store(e.ID, true)
	for _, consumer := range p.opts.Consumers {
		consumer.Consume(ctx, e)
	}
}
func (p *Pipeline) writeCheckpoint(id string) {
	tmp := p.checkpoint + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err == nil {
		_ = os.Rename(tmp, p.checkpoint)
	}
}
func (p *Pipeline) Run(ctx context.Context) {
	p.once.Do(func() {
		ctx, p.cancel = context.WithCancel(ctx)
		go func() {
			defer close(p.done)
			checkpointBytes, _ := os.ReadFile(p.checkpoint)
			checkpointID := strings.TrimSpace(string(checkpointBytes))
			pastCheckpoint := checkpointID == ""
			_ = p.log.Scan(ctx, EventFilter{}, func(e Event) error {
				if !pastCheckpoint {
					p.consumePersisted(ctx, e)
					pastCheckpoint = e.ID == checkpointID
					return nil
				}
				p.handle(ctx, e)
				return nil
			})
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case e := <-p.handoff:
					p.handle(ctx, e)
				case <-ticker.C:
					_ = p.log.Scan(ctx, EventFilter{}, func(e Event) error { p.handle(ctx, e); return nil })
				case <-ctx.Done():
					return
				}
			}
		}()
	})
}
func (p *Pipeline) Close(ctx context.Context) error {
	for {
		select {
		case e := <-p.handoff:
			p.handle(ctx, e)
		case <-ctx.Done():
			return ctx.Err()
		default:
			goto drained
		}
	}

drained:
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *Pipeline) Replay(ctx context.Context, from time.Time) error {
	p.seen = sync.Map{}
	return p.log.Scan(ctx, EventFilter{From: from}, func(e Event) error { p.handle(ctx, e); return nil })
}
func (p *Pipeline) Lag() (int64, time.Time) { return int64(len(p.handoff)), time.Time{} }

type Refresher struct {
	registry *Registry
	src      EventSource
	store    AggregationStore
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func NewRefresher(reg *Registry, src EventSource, store AggregationStore, interval time.Duration) *Refresher {
	return &Refresher{registry: reg, src: src, store: store, interval: interval, done: make(chan struct{})}
}
func (r *Refresher) Run(ctx context.Context) {
	r.once.Do(func() {
		ctx, r.cancel = context.WithCancel(ctx)
		go func() {
			defer close(r.done)
			if r.interval <= 0 {
				<-ctx.Done()
				return
			}
			t := time.NewTicker(r.interval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					_ = r.Refresh(ctx)
				case <-ctx.Done():
					return
				}
			}
		}()
	})
}
func (r *Refresher) Refresh(ctx context.Context, names ...string) error {
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	for _, a := range r.registry.List() {
		if len(wanted) > 0 && !wanted[a.Name] || r.registry.Status(a.Name) != StatusOK {
			continue
		}
		if _, err := r.registry.Run(ctx, a.Name, Params{Since: time.Now().Add(-30 * 24 * time.Hour), Until: time.Now(), Limit: 100}, RunOptions{Fresh: true}); err != nil {
			return err
		}
	}
	return nil
}
func (r *Refresher) Close(ctx context.Context) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type RemoteEventStore interface {
	Put(context.Context, string, io.Reader, int64) error
	List(context.Context, string) ([]string, error)
	Get(context.Context, string) (io.ReadCloser, error)
}
type Shipper struct {
	log      EventLog
	remote   RemoteEventStore
	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func NewShipper(log EventLog, remote RemoteEventStore, interval time.Duration) *Shipper {
	return &Shipper{log: log, remote: remote, interval: interval, done: make(chan struct{})}
}
func (s *Shipper) Run(ctx context.Context) {
	s.once.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.cancel = cancel
		s.mu.Unlock()
		defer close(s.done)
		if s.interval <= 0 {
			<-ctx.Done()
			return
		}
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.ShipNow(ctx)
			case <-ctx.Done():
				return
			}
		}
	})
}
func (s *Shipper) Close(ctx context.Context) error {
	if err := s.ShipNow(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Shipper) ShipNow(ctx context.Context) error {
	// Phase 1's remote is deliberately "none". Sealing still runs so daily
	// segments are compressed and retention is enforced on every server.
	return s.log.Seal(ctx, time.Now().UTC().Truncate(24*time.Hour))
}
func (s *Shipper) Lag() (int64, time.Time) { return 0, time.Time{} }
