package analytics

import (
	"context"
	"net/http"
	"path/filepath"
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
	Dropped           int64 `json:"dropped"`
	DroppedAtShutdown int64 `json:"dropped_at_shutdown"`
	PipelineEnabled   bool  `json:"pipeline_enabled"`
	PipelineLagEvents int64 `json:"pipeline_lag_events"`
	ShipLagBytes      int64 `json:"ship_lag_bytes"`
	Segments          int   `json:"segments"`
}
type Service struct {
	cfg        config.AnalyticsConfig
	log        EventLog
	recorder   *Recorder
	pipeline   *Pipeline
	shipper    *Shipper
	refresher  *Refresher
	store      AggregationStore
	facts      ContentFacts
	registry   *Registry
	aggregator *Aggregator
	cancel     context.CancelFunc
	close      sync.Once
	closeErr   error
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
		store, err = OpenAggregationStore(filepath.Join(dir, "aggregations"))
		if err != nil {
			return nil, err
		}
	}
	facts := NewContentFacts(deps.FS)
	reg := NewRegistry(store, func() []string {
		return []string{"session.start", "session.end", "command.exec", "command.unknown", "syntax.unknown", "auth.login", "doc.write", "doc.scalars"}
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
	s := &Service{cfg: cfg, log: log, recorder: rec, shipper: NewShipper(log, deps.Remote, cfg.Ship.Interval), store: store, facts: facts, registry: reg, aggregator: agg, refresher: ref}
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
	}
}
func (s *Service) Record(ctx context.Context, e Event) { s.recorder.Record(ctx, e) }
func (s *Service) Registry() *Registry                 { return s.registry }
func (s *Service) Facts() ContentFacts                 { return s.facts }
func (s *Service) EventSource() EventSource            { return s.log }
func (s *Service) Aggregator() http.Handler            { return http.HandlerFunc(s.aggregator.ServeHTTP) }
func (s *Service) Refresh(ctx context.Context, names ...string) error {
	return s.refresher.Refresh(ctx, names...)
}
func (s *Service) Replay(ctx context.Context, from time.Time) error {
	if s.pipeline == nil {
		return nil
	}
	return s.pipeline.Replay(ctx, from)
}
func (s *Service) ShipNow(ctx context.Context) error { return s.shipper.ShipNow(ctx) }
func (s *Service) Health() Health {
	h := Health{Dropped: s.recorder.Dropped(), DroppedAtShutdown: s.recorder.DroppedAtShutdown(), PipelineEnabled: s.pipeline != nil, Segments: len(s.log.Segments())}
	if s.pipeline != nil {
		h.PipelineLagEvents, _ = s.pipeline.Lag()
	}
	h.ShipLagBytes, _ = s.shipper.Lag()
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
