package analytics

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// ProcessorDrainer is an optional extension for processors backed by a source
// independent of the event log (for example, commit history).
type ProcessorDrainer interface {
	Drain(context.Context) []Event
}

// ProcessorCheckpointer lets a processor include its source cursor in the
// pipeline's single durable checkpoint.
type ProcessorCheckpointer interface {
	MarshalCheckpointState() (json.RawMessage, error)
	RestoreCheckpointState(json.RawMessage) error
}

// ProcessorReplayer resets an independently cursor-backed processor before a
// replay. The processor must advance from its source origin and suppress
// derived events older than from.
type ProcessorReplayer interface {
	ResetForReplay(time.Time) error
}

// ConsumerResetter clears consumer state before replaying its input window.
type ConsumerResetter interface {
	Reset()
}

type pipelineCheckpoint struct {
	EventID    string                     `json:"event_id,omitempty"`
	Processors map[string]json.RawMessage `json:"processors,omitempty"`
}

type PipelineOptions struct {
	Processors []Processor
	Consumers  []Consumer
	Refresher  *Refresher
	Sink       Sink
	Buffer     int
}
type Pipeline struct {
	log         EventLog
	checkpoint  string
	opts        PipelineOptions
	handoff     chan Event
	seen        sync.Map
	cancel      context.CancelFunc
	done        chan struct{}
	once        sync.Once
	processMu   sync.Mutex
	lastEventID string
}

func NewPipeline(log EventLog, checkpoint string, opts PipelineOptions) *Pipeline {
	if opts.Buffer <= 0 {
		opts.Buffer = 1024
	}
	return &Pipeline{log: log, checkpoint: checkpoint, opts: opts, handoff: make(chan Event, opts.Buffer), done: make(chan struct{})}
}
func (p *Pipeline) Handoff() chan<- Event { return p.handoff }
func (p *Pipeline) handle(ctx context.Context, e Event) {
	p.processMu.Lock()
	defer p.processMu.Unlock()
	p.handleLocked(ctx, e)
}
func (p *Pipeline) handleLocked(ctx context.Context, e Event) {
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
	p.lastEventID = e.ID
	p.writeCheckpoint(e.ID)
}
func (p *Pipeline) consumePersisted(ctx context.Context, e Event) {
	p.seen.Store(e.ID, true)
	for _, consumer := range p.opts.Consumers {
		consumer.Consume(ctx, e)
	}
}
func (p *Pipeline) emitDerived(ctx context.Context, events []Event) {
	for _, derived := range events {
		if p.opts.Sink != nil {
			p.opts.Sink.Record(ctx, derived)
		}
		p.seen.Store(derived.ID, true)
		for _, consumer := range p.opts.Consumers {
			consumer.Consume(ctx, derived)
		}
	}
}
func (p *Pipeline) drainLocked(ctx context.Context) {
	for _, processor := range p.opts.Processors {
		if drainer, ok := processor.(ProcessorDrainer); ok {
			p.emitDerived(ctx, drainer.Drain(ctx))
		}
	}
}
func (p *Pipeline) writeCheckpoint(id string) {
	state := pipelineCheckpoint{EventID: id, Processors: map[string]json.RawMessage{}}
	for _, processor := range p.opts.Processors {
		if checkpointer, ok := processor.(ProcessorCheckpointer); ok {
			if raw, err := checkpointer.MarshalCheckpointState(); err == nil {
				state.Processors[processor.Name()] = raw
			}
		}
	}
	b, err := json.Marshal(state)
	if err != nil {
		return
	}
	tmp := p.checkpoint + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err == nil {
		_ = os.Rename(tmp, p.checkpoint)
	}
}
func (p *Pipeline) readCheckpoint() pipelineCheckpoint {
	b, err := os.ReadFile(p.checkpoint)
	if err != nil {
		return pipelineCheckpoint{}
	}
	var state pipelineCheckpoint
	if json.Unmarshal(b, &state) != nil {
		// Checkpoints written before processor state was introduced contained
		// only the last event ID.
		state.EventID = string(bytesTrimSpace(b))
	}
	for _, processor := range p.opts.Processors {
		if raw := state.Processors[processor.Name()]; raw != nil {
			if checkpointer, ok := processor.(ProcessorCheckpointer); ok {
				_ = checkpointer.RestoreCheckpointState(raw)
			}
		}
	}
	return state
}
func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
}
func (p *Pipeline) Run(ctx context.Context) {
	p.once.Do(func() {
		ctx, p.cancel = context.WithCancel(ctx)
		checkpointID := p.readCheckpoint().EventID
		p.processMu.Lock()
		p.lastEventID = checkpointID
		p.processMu.Unlock()
		go func() {
			defer close(p.done)
			var retained []Event
			cursor := logCursor{}
			scan := func(fn func(Event) error) error {
				if log, ok := p.log.(*fileEventLog); ok {
					return log.scanIncremental(ctx, cursor, fn)
				}
				return p.log.Scan(ctx, EventFilter{}, fn)
			}
			_ = scan(func(e Event) error { retained = append(retained, e); return nil })
			checkpointAt := -1
			for i := range retained {
				if retained[i].ID == checkpointID {
					checkpointAt = i
				}
			}
			for i, e := range retained {
				if checkpointAt >= 0 && i <= checkpointAt {
					p.consumePersisted(ctx, e)
				} else {
					p.handle(ctx, e)
				}
			}
			p.processMu.Lock()
			p.drainLocked(ctx)
			p.writeCheckpoint(p.lastEventID)
			p.processMu.Unlock()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case e := <-p.handoff:
					p.handle(ctx, e)
				case <-ticker.C:
					_ = scan(func(e Event) error { p.handle(ctx, e); return nil })
					p.processMu.Lock()
					p.drainLocked(ctx)
					p.writeCheckpoint(p.lastEventID)
					p.processMu.Unlock()
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
	p.processMu.Lock()
	p.drainLocked(ctx)
	p.writeCheckpoint(p.lastEventID)
	p.processMu.Unlock()
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
	p.processMu.Lock()
	defer p.processMu.Unlock()
	p.seen = sync.Map{}
	p.lastEventID = ""
	for _, processor := range p.opts.Processors {
		if replayer, ok := processor.(ProcessorReplayer); ok {
			if err := replayer.ResetForReplay(from); err != nil {
				return err
			}
		}
	}
	for _, consumer := range p.opts.Consumers {
		if resetter, ok := consumer.(ConsumerResetter); ok {
			resetter.Reset()
		}
	}
	if err := p.log.Scan(ctx, EventFilter{From: from}, func(e Event) error {
		p.handleLocked(ctx, e)
		return nil
	}); err != nil {
		return err
	}
	p.drainLocked(ctx)
	p.writeCheckpoint(p.lastEventID)
	return nil
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
