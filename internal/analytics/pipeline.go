package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
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

type namespacedProcessor struct {
	base   Processor
	prefix string
}

// NamespacedProcessor confines every derived event and the checkpoint key to
// one plugin namespace while preserving optional processor capabilities.
func NamespacedProcessor(base Processor, pluginName string) Processor {
	if base == nil {
		return nil
	}
	return namespacedProcessor{base: base, prefix: "plugin." + pluginName + "."}
}

func (p namespacedProcessor) Name() string {
	name, ok := namespacedType(p.base.Name(), p.prefix)
	if !ok {
		return p.prefix + "processor"
	}
	return name
}

func (p namespacedProcessor) Process(ctx context.Context, event Event) []Event {
	return p.namespace(p.base.Process(ctx, event))
}

func (p namespacedProcessor) Drain(ctx context.Context) []Event {
	if drainer, ok := p.base.(ProcessorDrainer); ok {
		return p.namespace(drainer.Drain(ctx))
	}
	return nil
}

func (p namespacedProcessor) MarshalCheckpointState() (json.RawMessage, error) {
	if checkpointer, ok := p.base.(ProcessorCheckpointer); ok {
		return checkpointer.MarshalCheckpointState()
	}
	return nil, nil
}

func (p namespacedProcessor) RestoreCheckpointState(state json.RawMessage) error {
	if checkpointer, ok := p.base.(ProcessorCheckpointer); ok {
		return checkpointer.RestoreCheckpointState(state)
	}
	return nil
}

func (p namespacedProcessor) ResetForReplay(from time.Time) error {
	if replayer, ok := p.base.(ProcessorReplayer); ok {
		return replayer.ResetForReplay(from)
	}
	return nil
}

func (p namespacedProcessor) namespace(events []Event) []Event {
	out := events[:0]
	for _, event := range events {
		var ok bool
		event.Type, ok = namespacedType(event.Type, p.prefix)
		if ok {
			out = append(out, event)
		}
	}
	return out
}

// ConsumerResetter clears consumer state before replaying its input window.
type ConsumerResetter interface {
	Reset()
}

type pipelineCheckpoint struct {
	EventID    string                     `json:"event_id,omitempty"`
	EventTypes []string                   `json:"event_types,omitempty"`
	Processors map[string]json.RawMessage `json:"processors,omitempty"`
	Segments   logCursor                  `json:"segments,omitempty"`
}

type PipelineOptions struct {
	Processors []Processor
	Consumers  []Consumer
	Refresher  *Refresher
	Sink       Sink
	Buffer     int
	Gate       chan struct{}
}
type Pipeline struct {
	log         EventLog
	checkpoint  string
	opts        PipelineOptions
	handoff     chan Event
	seenMu      sync.Mutex
	seen        map[string]struct{}
	seenOrder   []string
	typesMu     sync.RWMutex
	eventTypes  map[string]struct{}
	cursor      logCursor
	cancel      context.CancelFunc
	done        chan struct{}
	stop        chan struct{}
	stopOnce    sync.Once
	once        sync.Once
	processMu   sync.Mutex
	lastEventID string
	caughtUp    atomic.Bool
}

func NewPipeline(log EventLog, checkpoint string, opts PipelineOptions) *Pipeline {
	if opts.Buffer <= 0 {
		opts.Buffer = 1024
	}
	return &Pipeline{log: log, checkpoint: checkpoint, opts: opts, handoff: make(chan Event, opts.Buffer), done: make(chan struct{}), stop: make(chan struct{}), seen: map[string]struct{}{}, eventTypes: map[string]struct{}{}, cursor: logCursor{}}
}
func (p *Pipeline) Handoff() chan<- Event { return p.handoff }
func (p *Pipeline) AddProcessor(processor Processor) {
	if processor == nil {
		return
	}
	p.processMu.Lock()
	defer p.processMu.Unlock()
	state := p.loadCheckpoint()
	if raw := state.Processors[processor.Name()]; raw != nil {
		if checkpointer, ok := processor.(ProcessorCheckpointer); ok {
			_ = checkpointer.RestoreCheckpointState(raw)
		}
	}
	p.opts.Processors = append(p.opts.Processors, processor)
}
func (p *Pipeline) AddConsumer(consumer Consumer) {
	if consumer == nil {
		return
	}
	p.processMu.Lock()
	p.opts.Consumers = append(p.opts.Consumers, consumer)
	p.processMu.Unlock()
}
func (p *Pipeline) EventTypes() []string {
	p.typesMu.RLock()
	defer p.typesMu.RUnlock()
	types := make([]string, 0, len(p.eventTypes))
	for eventType := range p.eventTypes {
		types = append(types, eventType)
	}
	sort.Strings(types)
	return types
}
func (p *Pipeline) observeEventType(eventType string) {
	if eventType == "" {
		return
	}
	p.typesMu.Lock()
	p.eventTypes[eventType] = struct{}{}
	p.typesMu.Unlock()
}
func (p *Pipeline) markSeen(id string) bool {
	if id == "" {
		return false
	}
	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	if _, ok := p.seen[id]; ok {
		return true
	}
	p.seen[id] = struct{}{}
	p.seenOrder = append(p.seenOrder, id)
	if len(p.seenOrder) > 4096 {
		delete(p.seen, p.seenOrder[0])
		p.seenOrder = p.seenOrder[1:]
	}
	return false
}
func (p *Pipeline) handle(ctx context.Context, e Event) {
	p.processMu.Lock()
	defer p.processMu.Unlock()
	p.handleLocked(ctx, e)
}
func (p *Pipeline) handleLocked(ctx context.Context, e Event) {
	if p.markSeen(e.ID) {
		return
	}
	p.observeEventType(e.Type)
	for _, processor := range p.opts.Processors {
		for _, derived := range processor.Process(ctx, e) {
			if p.opts.Sink != nil {
				p.opts.Sink.Record(ctx, derived)
			}
			p.markSeen(derived.ID)
			p.observeEventType(derived.Type)
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
	p.processMu.Lock()
	defer p.processMu.Unlock()
	p.markSeen(e.ID)
	p.observeEventType(e.Type)
	for _, consumer := range p.opts.Consumers {
		consumer.Consume(ctx, e)
	}
}
func (p *Pipeline) emitDerived(ctx context.Context, events []Event) {
	for _, derived := range events {
		if p.opts.Sink != nil {
			p.opts.Sink.Record(ctx, derived)
		}
		p.markSeen(derived.ID)
		p.observeEventType(derived.Type)
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
	state := pipelineCheckpoint{EventID: id, EventTypes: p.EventTypes(), Processors: map[string]json.RawMessage{}, Segments: logCursor{}}
	for path, offset := range p.cursor {
		state.Segments[path] = offset
	}
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
func (p *Pipeline) loadCheckpoint() pipelineCheckpoint {
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
	return state
}
func (p *Pipeline) readCheckpoint() pipelineCheckpoint {
	state := p.loadCheckpoint()
	for _, eventType := range state.EventTypes {
		p.observeEventType(eventType)
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
		state := p.readCheckpoint()
		checkpointID := state.EventID
		p.processMu.Lock()
		p.lastEventID = checkpointID
		if len(state.Segments) > 0 {
			p.cursor = state.Segments
		}
		cursor := logCursor{}
		for path, offset := range p.cursor {
			cursor[path] = offset
		}
		p.processMu.Unlock()
		go func() {
			defer close(p.done)
			scan := func(fn func(Event) error) error {
				if p.opts.Gate != nil {
					select {
					case p.opts.Gate <- struct{}{}:
						defer func() { <-p.opts.Gate }()
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				if log, ok := p.log.(*fileEventLog); ok {
					return log.scanIncremental(ctx, cursor, fn)
				}
				return p.log.Scan(ctx, EventFilter{}, fn)
			}
			// New checkpoints resume directly at durable segment offsets. For a
			// legacy ID-only checkpoint, stream (rather than retain) until the ID.
			legacy := len(state.Segments) == 0 && checkpointID != ""
			legacyFound := false
			if legacy {
				// Determine whether retention removed the old checkpoint without
				// buffering the log. If it did, the retained log is all new work.
				_ = p.log.Scan(ctx, EventFilter{}, func(e Event) error {
					if e.ID == checkpointID {
						legacyFound = true
					}
					return nil
				})
			}
			pastLegacy := !legacy || !legacyFound
			initialErr := scan(func(e Event) error {
				if !pastLegacy {
					p.consumePersisted(ctx, e)
					if e.ID == checkpointID {
						pastLegacy = true
					}
					return nil
				}
				p.handle(ctx, e)
				return nil
			})
			if initialErr == nil {
				p.processMu.Lock()
				p.cursor = cloneCursor(cursor)
				p.drainLocked(ctx)
				p.writeCheckpoint(p.lastEventID)
				p.processMu.Unlock()
				p.caughtUp.Store(true)
			}
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case e := <-p.handoff:
					p.handle(ctx, e)
				case <-ticker.C:
					if err := scan(func(e Event) error { p.handle(ctx, e); return nil }); err != nil {
						p.caughtUp.Store(false)
						continue
					}
					p.processMu.Lock()
					p.cursor = cloneCursor(cursor)
					p.drainLocked(ctx)
					p.writeCheckpoint(p.lastEventID)
					p.processMu.Unlock()
					p.caughtUp.Store(true)
				case <-p.stop:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	})
}
func cloneCursor(cursor logCursor) logCursor {
	clone := logCursor{}
	for path, offset := range cursor {
		clone[path] = offset
	}
	return clone
}
func (p *Pipeline) Close(ctx context.Context) error {
	if p.cancel != nil {
		// Stop the live worker between operations before draining its queue.
		// Canceling first can invalidate an event it already received but has
		// not processed yet, losing derived writes while marking them seen.
		p.stopOnce.Do(func() { close(p.stop) })
		select {
		case <-p.done:
			p.cancel()
		case <-ctx.Done():
			p.cancel()
			return ctx.Err()
		}
	}
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
	return nil
}
func (p *Pipeline) Replay(ctx context.Context, from time.Time) error {
	p.processMu.Lock()
	defer p.processMu.Unlock()
	p.seenMu.Lock()
	p.seen = map[string]struct{}{}
	p.seenOrder = nil
	p.seenMu.Unlock()
	p.cursor = logCursor{}
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
func (p *Pipeline) CaughtUp() bool          { return p.caughtUp.Load() }

type Refresher struct {
	registry *Registry
	src      EventSource
	store    AggregationStore
	interval time.Duration
	mu       sync.RWMutex
	last     time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	schedule func(func(context.Context))
}

func NewRefresher(reg *Registry, src EventSource, store AggregationStore, interval time.Duration) *Refresher {
	return &Refresher{registry: reg, src: src, store: store, interval: interval, done: make(chan struct{})}
}
func (r *Refresher) SetScheduler(schedule func(func(context.Context))) { r.schedule = schedule }
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
					if r.schedule != nil {
						r.schedule(func(jobCtx context.Context) { _ = r.Refresh(jobCtx) })
					} else {
						_ = r.Refresh(ctx)
					}
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
		requiresFacts := false
		for _, requirement := range a.Requires {
			requiresFacts = requiresFacts || requirement == "facts"
		}
		// Current-content facts do not change when event-log refresh ticks. Never
		// schedule them, and for an explicit request compute without creating a
		// shared moving-window materialization.
		if requiresFacts && len(wanted) == 0 {
			continue
		}
		now := time.Now()
		params := Params{Since: now.Add(-30 * 24 * time.Hour), Until: now, Limit: 100}
		var err error
		if requiresFacts {
			_, err = r.registry.RunWithFacts(ctx, a.Name, params, r.registry.facts)
		} else {
			_, err = r.registry.Run(ctx, a.Name, params, RunOptions{Fresh: true})
		}
		if err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.last = time.Now().UTC()
	r.mu.Unlock()
	return nil
}
func (r *Refresher) LastRefresh() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.last
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
	shipMu   sync.Mutex
	offsets  map[string]int64
	loaded   bool
}

func NewShipper(log EventLog, remote RemoteEventStore, interval time.Duration) *Shipper {
	return &Shipper{log: log, remote: remote, interval: interval, done: make(chan struct{}), offsets: map[string]int64{}}
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
	s.shipMu.Lock()
	defer s.shipMu.Unlock()
	if err := s.log.Seal(ctx, time.Now().UTC().Truncate(24*time.Hour)); err != nil {
		return err
	}
	// remote:none deliberately retains local sealing/retention behavior without
	// creating a checkpoint or pretending that bytes were shipped.
	if s.remote == nil {
		return nil
	}
	s.loadCheckpoint()
	segments := s.log.Segments()
	for _, seg := range segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		base := filepath.Base(seg.Path)
		day := seg.Day.UTC().Format("2006-01-02")
		offset := s.offsets[base]
		if offset > seg.Size {
			offset = 0
		}
		if offset == seg.Size {
			continue
		}
		f, err := os.Open(seg.Path)
		if err != nil {
			return err
		}
		key := "events/" + day + "/" + base
		if !seg.Compressed {
			// Each active upload is an immutable byte-range object. Retrying uses
			// the same key and bytes; the checkpoint advances only after Put.
			key = fmt.Sprintf("events/%s/%020d-%020d.jsonl", day, offset, seg.Size)
		}
		_, seekErr := f.Seek(offset, io.SeekStart)
		if seekErr == nil {
			err = s.remote.Put(ctx, key, io.LimitReader(f, seg.Size-offset), seg.Size-offset)
		}
		_ = f.Close()
		if seekErr != nil {
			return seekErr
		}
		if err != nil {
			return err
		}
		s.offsets[base] = seg.Size
		if err := s.saveCheckpoint(); err != nil {
			return err
		}
	}
	return nil
}
func (s *Shipper) checkpointPath() string {
	if l, ok := s.log.(*fileEventLog); ok {
		return filepath.Join(l.dir, "shipper.checkpoint")
	}
	return ""
}
func (s *Shipper) loadCheckpoint() {
	if s.loaded {
		return
	}
	s.loaded = true
	if b, err := os.ReadFile(s.checkpointPath()); err == nil {
		_ = json.Unmarshal(b, &s.offsets)
	}
}
func (s *Shipper) saveCheckpoint() error {
	path := s.checkpointPath()
	if path == "" {
		return nil
	}
	b, err := json.Marshal(s.offsets)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func (s *Shipper) Lag() (int64, time.Time) {
	if s.remote == nil {
		return 0, time.Time{}
	}
	s.shipMu.Lock()
	defer s.shipMu.Unlock()
	s.loadCheckpoint()
	var bytes int64
	var since time.Time
	segments := s.log.Segments()
	sort.Slice(segments, func(i, j int) bool { return segments[i].Day.Before(segments[j].Day) })
	for _, seg := range segments {
		n := seg.Size - s.offsets[filepath.Base(seg.Path)]
		if n <= 0 {
			continue
		}
		bytes += n
		if since.IsZero() || seg.Day.Before(since) {
			since = seg.Day
		}
	}
	return bytes, since
}
