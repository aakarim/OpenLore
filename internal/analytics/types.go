// Package analytics contains OpenLore's experimental, export-first analytics
// substrate. It deliberately keeps event recording independent from analysis.
package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
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
type LineRange struct{ Start, End int }

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

type EventFilter struct {
	From, To   time.Time
	Types      []string
	Principals []string
}
type EventSource interface {
	Scan(context.Context, EventFilter, func(Event) error) error
}
type Consumer interface{ Consume(context.Context, Event) }
type Processor interface {
	Name() string
	Process(context.Context, Event) []Event
}

type Writer string

const (
	WriterHuman Writer = "human"
	WriterAgent Writer = "agent"
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
	Status     Status    `json:"status"`
	Table      Table     `json:"table"`
	ComputedAt time.Time `json:"computed_at"`
	Window     Params    `json:"window"`
	Note       string    `json:"note,omitempty"`
}
type Aggregation struct {
	Name, Title, Description string
	Params                   []ParamSpec
	Requires                 []string
	Compute                  func(context.Context, EventSource, ContentFacts, Params) (Table, error)
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
	if !aggregationName.MatchString(a.Name) || a.Compute == nil {
		return fmt.Errorf("invalid aggregation %q", a.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[a.Name]; exists {
		return fmt.Errorf("aggregation %q already registered", a.Name)
	}
	r.items[a.Name] = a
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
func (r *Registry) Run(ctx context.Context, name string, p Params, opts RunOptions) (Materialized, error) {
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
	if !opts.Fresh && r.store != nil {
		if m, found, err := r.store.Get(ctx, name, p); err != nil {
			return Materialized{}, err
		} else if found {
			return m, nil
		}
	}
	t, err := a.Compute(ctx, r.source, r.facts, p)
	if err != nil {
		return Materialized{}, err
	}
	m := Materialized{Status: StatusOK, Table: t, ComputedAt: time.Now().UTC(), Window: p}
	if r.store != nil {
		_ = r.store.Put(ctx, name, p, m)
	}
	return m, nil
}
