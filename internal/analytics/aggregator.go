package analytics

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type AggregatorOptions struct{}
type durationSummary struct {
	sum   float64
	count int64
}
type Aggregator struct {
	mu         sync.RWMutex
	commands   map[string]int64
	durations  map[string]durationSummary
	writes     map[string]int64
	scalars    map[string]float64
	scalarSeen map[string]struct{}
	health     func() Health
}

func NewAggregator(AggregatorOptions) *Aggregator {
	return &Aggregator{commands: map[string]int64{}, durations: map[string]durationSummary{}, writes: map[string]int64{}, scalars: map[string]float64{}, scalarSeen: map[string]struct{}{}}
}
func (a *Aggregator) SetHealth(fn func() Health) { a.health = fn }
func (a *Aggregator) Reset() {
	a.mu.Lock()
	a.commands = map[string]int64{}
	a.durations = map[string]durationSummary{}
	a.writes = map[string]int64{}
	a.scalars = map[string]float64{}
	a.scalarSeen = map[string]struct{}{}
	a.mu.Unlock()
}
func (a *Aggregator) Consume(_ context.Context, e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch e.Type {
	case "command.exec":
		cmd := fieldString(e, "command")
		exitClass := "success"
		if fieldFloat(e, "exit_code") != 0 {
			exitClass = "error"
		}
		key := cmd + "\x00" + e.Transport + "\x00" + exitClass
		a.commands[key]++
		duration := a.durations[cmd]
		duration.sum += fieldFloat(e, "duration_ms") / 1000
		duration.count++
		a.durations[cmd] = duration
	case "doc.write":
		key := fieldString(e, "docset") + "\x00" + fieldString(e, "writer")
		a.writes[key]++
	case "doc.scalars":
		key := scalarEventKey(e)
		if _, exists := a.scalarSeen[key]; exists {
			return
		}
		a.scalarSeen[key] = struct{}{}
		docset, writer := fieldString(e, "docset"), fieldString(e, "writer")
		if delta, ok := e.Fields["delta"].(map[string]any); ok {
			for scalar, v := range delta {
				if n, ok := v.(float64); ok {
					a.scalars[docset+"\x00"+writer+"\x00"+scalar] += n
				}
			}
		}
	}
}
func promQuote(s string) string { return strconv.Quote(s) }
func (a *Aggregator) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	a.mu.RLock()
	defer a.mu.RUnlock()
	var keys []string
	for k := range a.commands {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "# TYPE openlore_commands_total counter")
	for _, k := range keys {
		p := strings.Split(k, "\x00")
		fmt.Fprintf(w, "openlore_commands_total{command=%s,transport=%s,exit_class=%s} %d\n", promQuote(p[0]), promQuote(p[1]), promQuote(p[2]), a.commands[k])
	}
	fmt.Fprintln(w, "# TYPE openlore_doc_writes_total counter")
	keys = keys[:0]
	for k := range a.writes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := strings.Split(k, "\x00")
		fmt.Fprintf(w, "openlore_doc_writes_total{docset=%s,writer=%s} %d\n", promQuote(p[0]), promQuote(p[1]), a.writes[k])
	}
	fmt.Fprintln(w, "# TYPE openlore_command_duration_seconds summary")
	keys = keys[:0]
	for k := range a.durations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, command := range keys {
		duration := a.durations[command]
		fmt.Fprintf(w, "openlore_command_duration_seconds_sum{command=%s} %g\n", promQuote(command), duration.sum)
		fmt.Fprintf(w, "openlore_command_duration_seconds_count{command=%s} %d\n", promQuote(command), duration.count)
	}
	// Content can shrink, so scalar deltas are gauges rather than monotonic
	// counters.
	fmt.Fprintln(w, "# TYPE openlore_doc_write_scalar_total gauge")
	keys = keys[:0]
	for k := range a.scalars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		fmt.Fprintf(w, "openlore_doc_write_scalar_total{docset=%s,writer=%s,scalar=%s} %g\n", promQuote(parts[0]), promQuote(parts[1]), promQuote(parts[2]), a.scalars[key])
	}
	if a.health != nil {
		h := a.health()
		fmt.Fprintln(w, "# TYPE openlore_analytics_dropped_events_total counter")
		fmt.Fprintf(w, "openlore_analytics_dropped_events_total %d\n", h.Dropped)
		fmt.Fprintf(w, "openlore_analytics_dropped_at_shutdown_total %d\n", h.DroppedAtShutdown)
		fmt.Fprintf(w, "openlore_analytics_pipeline_lag_events %d\n", h.PipelineLagEvents)
		fmt.Fprintf(w, "openlore_analytics_ship_lag_bytes %d\n", h.ShipLagBytes)
	}
}
