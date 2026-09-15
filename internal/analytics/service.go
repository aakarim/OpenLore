package analytics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type Deps struct {
	FS         vfs.FileSystem
	Processors []Processor
	Tokenizer  Tokenizer
	Remote     RemoteEventStore
	Store      AggregationStore
}
type Health struct {
	Dropped                 int64     `json:"dropped"`
	DroppedAtShutdown       int64     `json:"dropped_at_shutdown"`
	PipelineEnabled         bool      `json:"pipeline_enabled"`
	PipelineLagEvents       int64     `json:"pipeline_lag_events"`
	ShipLagBytes            int64     `json:"ship_lag_bytes"`
	Segments                int       `json:"segments"`
	LastRefresh             time.Time `json:"last_refresh,omitempty"`
	HistoryCursorPosition   string    `json:"history_cursor_position,omitempty"`
	HistoryCursorLagBytes   int64     `json:"history_cursor_lag_bytes,omitempty"`
	HistoryBlobStoreObjects int64     `json:"history_blob_store_objects,omitempty"`
	HistoryBlobStoreBytes   int64     `json:"history_blob_store_bytes,omitempty"`
}
type Service struct {
	cfg         config.AnalyticsConfig
	fs          vfs.FileSystem
	log         EventLog
	recorder    *Recorder
	pipeline    *Pipeline
	shipper     *Shipper
	refresher   *Refresher
	store       AggregationStore
	facts       ContentFacts
	registry    *Registry
	aggregator  *Aggregator
	remote      RemoteEventStore
	history     func(context.Context) (position string, lagBytes, blobObjects, blobBytes int64)
	emitted     sync.Map
	providersMu sync.RWMutex
	providers   []ContentScalarProvider
	cancel      context.CancelFunc
	close       sync.Once
	closeErr    error
}

func New(cfg config.AnalyticsConfig, deps Deps) (*Service, error) {
	dir := cfg.Dir
	if dir == "" {
		dir = "analytics"
	}
	log, err := OpenEventLog(filepath.Join(dir, "events"), LogOptions{Rotate: cfg.Log.Rotate, Compress: cfg.Log.Compress, Retention: cfg.Log.Retention})
	if err != nil {
		return nil, err
	}
	store := deps.Store
	if store == nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Aggregations.Store)) {
		case "", "file":
			store, err = OpenFileAggregationStore(filepath.Join(dir, "aggregations"))
		case "sqlite":
			store, err = OpenSQLiteAggregationStore(filepath.Join(dir, "aggregations.sqlite"))
		default:
			err = fmt.Errorf("unsupported analytics aggregation store %q", cfg.Aggregations.Store)
		}
		if err != nil {
			return nil, err
		}
	}
	remote := deps.Remote
	if remote == nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Ship.Remote)) {
		case "", "none":
		default:
			err = fmt.Errorf("unsupported analytics remote store %q", cfg.Ship.Remote)
		}
		if err != nil {
			_ = store.Close()
			_ = log.Close()
			return nil, err
		}
	}
	providers := append([]ContentScalarProvider(nil), defaultProviders...)
	if deps.Tokenizer != nil {
		providers = []ContentScalarProvider{sizeProvider{}, tokenProvider{deps.Tokenizer}}
	}
	facts := NewContentFacts(deps.FS, providers...)
	s := &Service{cfg: cfg, fs: deps.FS, log: log, store: store, facts: facts, providers: providers}
	for _, processor := range deps.Processors {
		if processor != nil && processor.Name() == "doc-scalars" {
			s.emitted.Store("doc.scalars", true)
		}
	}
	reg := NewRegistry(store, func() []string {
		events := []string{"session.start", "session.end", "command.exec", "command.unknown", "syntax.unknown", "auth.login", "doc.write", "search.query", "doc.read", "doc.hit"}
		s.emitted.Range(func(event, _ any) bool { events = append(events, event.(string)); return true })
		return events
	})
	reg.Bind(log, facts)
	for _, a := range BuiltinAggregations() {
		if err := reg.Register(a); err != nil {
			return nil, err
		}
	}
	agg := NewAggregator(AggregatorOptions{})
	ref := NewRefresher(reg, log, store, cfg.Aggregations.RefreshInterval)
	rec := NewRecorder(log, cfg.Pipeline.Buffer)
	s.recorder = rec
	s.shipper = NewShipper(log, remote, cfg.Ship.Interval)
	s.remote = remote
	s.registry = reg
	s.aggregator = agg
	s.refresher = ref
	if cfg.PipelineEnabled() {
		derivedSink := sinkFunc(func(ctx context.Context, event Event) { _ = log.Append(ctx, event) })
		s.pipeline = NewPipeline(log, filepath.Join(dir, "pipeline.checkpoint"), PipelineOptions{Processors: deps.Processors, Consumers: []Consumer{agg}, Refresher: ref, Sink: derivedSink, Buffer: cfg.Pipeline.Buffer})
		rec.SetHandoff(s.pipeline.Handoff())
	} else {
		reg.SetPaused(true)
	}
	agg.SetHealth(s.Health)
	return s, nil
}
func (s *Service) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.recorder.Start(ctx)
	if s.pipeline != nil {
		s.pipeline.Run(ctx)
		s.refresher.Run(ctx)
	}
	go s.shipper.Run(ctx)
}
func (s *Service) Sink() Sink { return s.recorder }
func (s *Service) AddProcessor(processor Processor) {
	if s.pipeline != nil && processor != nil {
		s.pipeline.opts.Processors = append(s.pipeline.opts.Processors, processor)
		if processor.Name() == "doc-scalars" {
			s.emitted.Store("doc.scalars", true)
		}
	}
}
func (s *Service) AddConsumer(consumer Consumer) {
	if s.pipeline != nil && consumer != nil {
		s.pipeline.opts.Consumers = append(s.pipeline.opts.Consumers, consumer)
	}
}
func (s *Service) AddContentScalarProvider(provider ContentScalarProvider) {
	if provider == nil {
		return
	}
	s.providersMu.Lock()
	s.providers = append(s.providers, provider)
	s.facts = NewContentFacts(s.fs, s.providers...)
	s.registry.Bind(s.log, s.facts)
	s.providersMu.Unlock()
}
func (s *Service) SetTokenizer(tokenizer Tokenizer) {
	if tokenizer == nil {
		return
	}
	s.providersMu.Lock()
	providers := make([]ContentScalarProvider, 0, len(s.providers))
	for _, provider := range s.providers {
		if _, builtin := provider.(tokenProvider); !builtin {
			providers = append(providers, provider)
		}
	}
	s.providers = append(providers, tokenProvider{tokenizer})
	s.facts = NewContentFacts(s.fs, s.providers...)
	s.registry.Bind(s.log, s.facts)
	s.providersMu.Unlock()
}
func (s *Service) RegisterAggregation(aggregation Aggregation) error {
	return s.registry.Register(aggregation)
}
func (s *Service) NewContentFacts(fs vfs.FileSystem) ContentFacts {
	s.providersMu.RLock()
	providers := append([]ContentScalarProvider(nil), s.providers...)
	s.providersMu.RUnlock()
	return NewContentFacts(fs, providers...)
}
func (s *Service) ComputeScalars(path string, content []byte) DocScalars {
	s.providersMu.RLock()
	providers := append([]ContentScalarProvider(nil), s.providers...)
	s.providersMu.RUnlock()
	return computeScalars(path, content, providers)
}
func (s *Service) Record(ctx context.Context, e Event) { s.recorder.Record(ctx, e) }
func (s *Service) Registry() *Registry                 { return s.registry }
func (s *Service) Facts() ContentFacts {
	s.providersMu.RLock()
	defer s.providersMu.RUnlock()
	return s.facts
}
func (s *Service) EventSource() EventSource { return s.log }
func (s *Service) Aggregator() http.Handler { return http.HandlerFunc(s.aggregator.ServeHTTP) }
func (s *Service) Refresh(ctx context.Context, names ...string) error {
	return s.refresher.Refresh(ctx, names...)
}
func (s *Service) Replay(ctx context.Context, from time.Time) error {
	if s.pipeline == nil {
		return nil
	}
	return s.pipeline.Replay(ctx, from)
}

// RebuildFromRemote restores remote events into the local canonical log, then
// uses the ordinary replay path to recreate metrics and materializations.
func (s *Service) RebuildFromRemote(ctx context.Context) error {
	if s.remote == nil {
		return errors.New("analytics remote store is not configured")
	}
	source := RemoteSource(s.remote)
	existing := map[string]struct{}{}
	if err := s.log.Scan(ctx, EventFilter{}, func(event Event) error {
		existing[event.ID] = struct{}{}
		return nil
	}); err != nil {
		return err
	}
	if err := source.Scan(ctx, EventFilter{}, func(event Event) error {
		if _, found := existing[event.ID]; found {
			return nil
		}
		existing[event.ID] = struct{}{}
		return s.log.Append(ctx, event)
	}); err != nil {
		return err
	}
	if s.pipeline != nil {
		if err := s.pipeline.Replay(ctx, time.Time{}); err != nil {
			return err
		}
	}
	for _, aggregation := range s.registry.List() {
		if err := s.store.Invalidate(ctx, aggregation.Name); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) ShipNow(ctx context.Context) error { return s.shipper.ShipNow(ctx) }
func (s *Service) SetHistoryHealth(provider func(context.Context) (string, int64, int64, int64)) {
	s.history = provider
}
func (s *Service) Health() Health {
	h := Health{Dropped: s.recorder.Dropped(), DroppedAtShutdown: s.recorder.DroppedAtShutdown(), PipelineEnabled: s.pipeline != nil, Segments: len(s.log.Segments()), LastRefresh: s.refresher.LastRefresh()}
	if s.pipeline != nil {
		h.PipelineLagEvents, _ = s.pipeline.Lag()
	}
	h.ShipLagBytes, _ = s.shipper.Lag()
	if s.history != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		h.HistoryCursorPosition, h.HistoryCursorLagBytes, h.HistoryBlobStoreObjects, h.HistoryBlobStoreBytes = s.history(ctx)
	}
	return h
}
func (s *Service) Close(ctx context.Context) error {
	s.close.Do(func() {
		if err := s.recorder.Close(ctx); err != nil {
			s.closeErr = err
		}
		if s.pipeline != nil {
			if err := s.pipeline.Close(ctx); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
			if err := s.refresher.Close(ctx); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
		if err := s.shipper.Close(ctx); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
		if s.cancel != nil {
			s.cancel()
		}
		if err := s.log.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
		if err := s.store.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}
