// Package analytics contains OpenLore's export-first analytics substrate. It
// deliberately keeps event recording independent from analysis.
package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Event struct {
	ID              string         `json:"id"`
	Time            time.Time      `json:"time"`
	Type            string         `json:"type"`
	Principal       string         `json:"principal"`
	Actor           string         `json:"actor,omitempty"`
	Transport       string         `json:"transport"`
	SessionID       string         `json:"session_id"`
	ClientSessionID string         `json:"client_session_id,omitempty"`
	InvocationID    string         `json:"invocation_id"`
	ParentID        string         `json:"parent_id,omitempty"`
	RemoteAddr      string         `json:"remote_addr,omitempty"`
	Fields          map[string]any `json:"fields,omitempty"`
}

// NewID returns a lexically time-ordered, process-independent event ID. The
// representation is intentionally opaque to consumers.
func NewID() string {
	var entropy [10]byte
	_, _ = rand.Read(entropy[:])
	return fmt.Sprintf("%012x%s", uint64(time.Now().UTC().UnixMilli()), hex.EncodeToString(entropy[:]))
}

type invocationKey struct{}
type invocation struct{ invocationID, parentID string }

func ContextWithInvocation(ctx context.Context, invocationID, parentEventID string) context.Context {
	return context.WithValue(ctx, invocationKey{}, invocation{invocationID, parentEventID})
}

func InvocationFromContext(ctx context.Context) (string, string, bool) {
	v, ok := ctx.Value(invocationKey{}).(invocation)
	return v.invocationID, v.parentID, ok
}

type ContentUnit struct {
	Lines   *LineRange `json:"lines,omitempty"`
	Section string     `json:"section,omitempty"`
}
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Sink interface{ Record(context.Context, Event) }
type sinkFunc func(context.Context, Event)

func (f sinkFunc) Record(ctx context.Context, e Event) { f(ctx, e) }

func TeeSink(sinks ...Sink) Sink {
	return sinkFunc(func(ctx context.Context, e Event) {
		for _, sink := range sinks {
			if sink != nil {
				sink.Record(ctx, e)
			}
		}
	})
}

// NamespacedSink confines plugin events to the plugin's event namespace. An
// already-correct type is preserved; every other type is treated as a suffix.
func NamespacedSink(base Sink, pluginName string) Sink {
	prefix := "plugin." + pluginName + "."
	return sinkFunc(func(ctx context.Context, e Event) {
		if base == nil {
			return
		}
		var ok bool
		e.Type, ok = namespacedType(e.Type, prefix)
		if !ok {
			return
		}
		base.Record(ctx, e)
	})
}

func namespacedType(eventType, prefix string) (string, bool) {
	if strings.TrimSpace(eventType) == "" {
		return "", false
	}
	if !strings.HasPrefix(eventType, prefix) {
		eventType = prefix + eventType
	}
	return eventType, true
}

type EventFilter struct {
	From, To   time.Time
	Types      []string
	Principals []string
}
type EventSource interface {
	Scan(context.Context, EventFilter, func(Event) error) error
}

// SnapshotEventSource is implemented by sources that can scan a fixed set of
// events repeatedly. Every scan of the snapshot observes the same events, even
// if new events are appended between scans.
type SnapshotEventSource interface {
	Snapshot(context.Context) (EventSnapshot, error)
}

type EventSnapshot interface {
	EventSource
	Close() error
}
type Consumer interface{ Consume(context.Context, Event) }
type Processor interface {
	Name() string
	Process(context.Context, Event) []Event
}

type Writer string

const (
	WriterHuman   Writer = "human"
	WriterAgent   Writer = "agent"
	WriterUnknown Writer = "unknown"
)

type Status string

const (
	StatusOK      Status = "ok"
	StatusPlanned Status = "planned"
	StatusStale   Status = "stale"
	StatusPaused  Status = "paused"
)

type Params struct {
	Since, Until time.Time
	Limit        int
	Extra        map[string]string
}
type ParamSpec struct {
	Name, Description, Default string
	Required                   bool
}
type Table struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Total   int      `json:"total,omitempty"`
}
type Materialized struct {
	Status     Status          `json:"status"`
	Table      Table           `json:"table"`
	ComputedAt time.Time       `json:"computed_at"`
	Window     Params          `json:"window"`
	Note       string          `json:"note,omitempty"`
	Analytics  *SnapshotStatus `json:"analytics,omitempty"`
}
type Aggregation struct {
	Name, Title, Description string
	Params                   []ParamSpec
	Requires                 []string
	Compute                  func(context.Context, EventSource, ContentFacts, Params) (Table, error)
	// Incremental, when set, lets durable dashboard views cache the
	// aggregation's state per settled day and merge it instead of rescanning
	// the whole window. Compute must equal a single partial over the window.
	Incremental *Incremental
}

// Partial is the mergeable state of an incremental aggregation over one time
// range. Merge adds the state of a later, adjacent range, so merging the
// partials of consecutive ranges equals one partial over their union.
// Partials round-trip through JSON; configuration from Params must live in
// unexported fields set by Incremental.New.
type Partial interface {
	Add(Event)
	Merge(later Partial)
}

type Incremental struct {
	// Types are the event types Add consumes.
	Types []string
	New   func(Params) Partial
	Table func(context.Context, Partial, ContentFacts, Params) (Table, error)
	// Check, when set, rejects parameters before any events are scanned.
	Check func(context.Context, ContentFacts, Params) error
}

// incremental installs a's Compute as a single partial over the window.
func incremental(a Aggregation, inc Incremental) Aggregation {
	a.Incremental = &inc
	a.Compute = func(ctx context.Context, src EventSource, facts ContentFacts, p Params) (Table, error) {
		if inc.Check != nil {
			if err := inc.Check(ctx, facts, p); err != nil {
				return Table{}, err
			}
		}
		partial := inc.New(p)
		if err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: inc.Types}, func(e Event) error {
			partial.Add(e)
			return nil
		}); err != nil {
			return Table{}, err
		}
		return inc.Table(ctx, partial, facts, p)
	}
	return a
}

type RunOptions struct{ Fresh bool }

type AggregationStore interface {
	Put(context.Context, string, Params, Materialized) error
	Get(context.Context, string, Params) (Materialized, bool, error)
	Invalidate(context.Context, string) error
	Close() error
}

type Registry struct {
	mu      sync.RWMutex
	items   map[string]Aggregation
	store   AggregationStore
	emitted func() []string
	facts   ContentFacts
	source  EventSource
	paused  bool
}

func NewRegistry(store AggregationStore, emitted func() []string) *Registry {
	return &Registry{items: map[string]Aggregation{}, store: store, emitted: emitted}
}
func (r *Registry) Bind(src EventSource, facts ContentFacts) { r.source, r.facts = src, facts }
func (r *Registry) SetPaused(paused bool)                    { r.paused = paused }

var aggregationName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

func (r *Registry) Register(a Aggregation) error {
	return r.RegisterAll([]Aggregation{a})
}

// RegisterAll validates and installs a set of aggregations atomically.
func (r *Registry) RegisterAll(aggregations []Aggregation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make(map[string]struct{}, len(aggregations))
	for _, aggregation := range aggregations {
		if !aggregationName.MatchString(aggregation.Name) || aggregation.Compute == nil {
			return fmt.Errorf("invalid aggregation %q", aggregation.Name)
		}
		if _, exists := r.items[aggregation.Name]; exists {
			return fmt.Errorf("aggregation %q already registered", aggregation.Name)
		}
		if _, exists := names[aggregation.Name]; exists {
			return fmt.Errorf("aggregation %q already registered", aggregation.Name)
		}
		names[aggregation.Name] = struct{}{}
	}
	for _, aggregation := range aggregations {
		r.items[aggregation.Name] = aggregation
	}
	return nil
}
func (r *Registry) List() []Aggregation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Aggregation, 0, len(r.items))
	for _, a := range r.items {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (r *Registry) Status(name string) Status {
	if r.paused {
		return StatusPaused
	}
	r.mu.RLock()
	a, ok := r.items[name]
	r.mu.RUnlock()
	if !ok {
		return StatusPlanned
	}
	have := map[string]bool{"facts": r.facts != nil}
	if r.emitted != nil {
		for _, t := range r.emitted() {
			have[t] = true
		}
	}
	for _, req := range a.Requires {
		if !have[req] {
			return StatusPlanned
		}
	}
	return StatusOK
}

// incrementalFor returns the incremental form of a runnable aggregation.
func (r *Registry) incrementalFor(name string) *Incremental {
	r.mu.RLock()
	a, ok := r.items[name]
	r.mu.RUnlock()
	if !ok || r.Status(name) != StatusOK {
		return nil
	}
	return a.Incremental
}

func (r *Registry) Run(ctx context.Context, name string, p Params, opts RunOptions) (Materialized, error) {
	return r.run(ctx, name, p, opts, r.facts, true)
}

// RunWithFacts computes an aggregation against caller-scoped current content.
// Scoped facts are never read from or written to the shared materialized store.
func (r *Registry) RunWithFacts(ctx context.Context, name string, p Params, facts ContentFacts) (Materialized, error) {
	return r.run(ctx, name, p, RunOptions{Fresh: true}, facts, false)
}

// RunWithSource computes against caller-scoped events and current content.
// Scoped results never use or populate the process-wide materialization store.
func (r *Registry) RunWithSource(ctx context.Context, name string, p Params, source EventSource, facts ContentFacts) (Materialized, error) {
	return r.runWith(ctx, name, p, RunOptions{Fresh: true}, source, facts, false)
}

func (r *Registry) run(ctx context.Context, name string, p Params, opts RunOptions, facts ContentFacts, materialize bool) (Materialized, error) {
	return r.runWith(ctx, name, p, opts, r.source, facts, materialize)
}

func (r *Registry) runWith(ctx context.Context, name string, p Params, opts RunOptions, source EventSource, facts ContentFacts, materialize bool) (Materialized, error) {
	r.mu.RLock()
	a, ok := r.items[name]
	r.mu.RUnlock()
	if !ok {
		return Materialized{}, fmt.Errorf("unknown aggregation %q", name)
	}
	status := r.Status(name)
	if status == StatusPlanned {
		return Materialized{Status: status, Window: p, Note: "required analytics events activate in a later phase"}, nil
	}
	if status == StatusPaused && !opts.Fresh {
		return Materialized{Status: status, Window: p, Note: "analytics pipeline is paused"}, nil
	}
	if materialize && !opts.Fresh && r.store != nil {
		if m, found, err := r.store.Get(ctx, name, p); err != nil {
			return Materialized{}, err
		} else if found {
			return m, nil
		}
	}
	t, err := a.Compute(ctx, source, facts, p)
	if err != nil {
		return Materialized{}, err
	}
	m := Materialized{Status: StatusOK, Table: t, ComputedAt: time.Now().UTC(), Window: p}
	if materialize && r.store != nil {
		if err := r.store.Put(ctx, name, p, m); err != nil {
			return Materialized{}, err
		}
	}
	return m, nil
}
