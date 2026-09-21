package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	index       FactsIndex
	eventIndex  *sqliteEventIndex
	indexer     *factsIndexer
	processor   *workProcessor
	facts       ContentFacts
	registry    *Registry
	aggregator  *Aggregator
	remote      RemoteEventStore
	history     func(context.Context) (position string, lagBytes, blobObjects, blobBytes int64)
	emitted     sync.Map
	providersMu sync.RWMutex
	providers   []ContentScalarProvider
	scopesMu    sync.RWMutex
	scopes      []KnowledgeScope
	indexLog    sync.Once
	cancel      context.CancelFunc
	close       sync.Once
	started     atomic.Bool
	closeErr    error
}

type KnowledgeScope struct {
	Name string
	Root string
}

type SnapshotStatus struct {
	State      string    `json:"state"`
	ComputedAt time.Time `json:"computed_at,omitempty"`
	Updating   bool      `json:"updating"`
	Complete   bool      `json:"complete"`
	Coverage   string    `json:"coverage,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type UsageSnapshot struct {
	Summary
	Analytics SnapshotStatus `json:"analytics"`
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
		case "file":
			store, err = OpenFileAggregationStore(filepath.Join(dir, "aggregations"))
		case "", "sqlite":
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
	var index FactsIndex
	if sqliteStore, ok := store.(*SQLiteAggregationStore); ok {
		index = newSQLiteFactsIndex(sqliteStore)
	}
	s := &Service{cfg: cfg, fs: deps.FS, log: log, store: store, index: index, providers: providers, processor: newWorkProcessor()}
	if sqliteStore, ok := store.(*SQLiteAggregationStore); ok {
		s.eventIndex = &sqliteEventIndex{db: sqliteStore.db}
	}
	s.facts = newIndexedContentFacts(deps.FS, deps.FS, index, &s.indexLog, providers)
	if index != nil && deps.FS != nil {
		s.indexer = newFactsIndexer(s)
	}
	for _, processor := range deps.Processors {
		if processor != nil && processor.Name() == "doc-scalars" {
			s.emitted.Store("doc.scalars", true)
		}
	}
	reg := NewRegistry(store, func() []string {
		events := []string{"session.start", "session.end", "command.exec", "command.unknown", "syntax.unknown", "auth.login", "doc.write", "search.query", "doc.read", "doc.hit"}
		s.emitted.Range(func(event, _ any) bool { events = append(events, event.(string)); return true })
		if s.pipeline != nil {
			events = append(events, s.pipeline.EventTypes()...)
		}
		return events
	})
	reg.Bind(log, s.facts)
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
		consumers := []Consumer{agg}
		if s.eventIndex != nil {
			consumers = append(consumers, s.eventIndex)
		}
		s.pipeline = NewPipeline(log, filepath.Join(dir, "pipeline.checkpoint"), PipelineOptions{Processors: deps.Processors, Consumers: consumers, Refresher: ref, Sink: derivedSink, Buffer: cfg.Pipeline.Buffer})
		rec.SetHandoff(s.pipeline.Handoff())
	} else {
		reg.SetPaused(true)
	}
	agg.SetHealth(s.Health)
	return s, nil
}
func (s *Service) Start(ctx context.Context) {
	s.started.Store(true)
	ctx, s.cancel = context.WithCancel(ctx)
	go s.processor.run(ctx)
	if s.indexer != nil && s.cfg.PipelineEnabled() {
		s.indexer.enqueue("/")
	}
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
		s.pipeline.AddProcessor(processor)
		if processor.Name() == "doc-scalars" {
			s.emitted.Store("doc.scalars", true)
		}
	}
}
func (s *Service) AddConsumer(consumer Consumer) {
	if s.pipeline != nil && consumer != nil {
		s.pipeline.AddConsumer(consumer)
	}
}
func (s *Service) AddContentScalarProvider(provider ContentScalarProvider) {
	if provider == nil {
		return
	}
	s.providersMu.Lock()
	s.providers = append(s.providers, provider)
	s.facts = newIndexedContentFacts(s.fs, s.fs, s.index, &s.indexLog, s.providers)
	s.registry.Bind(s.log, s.facts)
	s.providersMu.Unlock()
	s.EnqueueFacts("/")
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
	s.facts = newIndexedContentFacts(s.fs, s.fs, s.index, &s.indexLog, s.providers)
	s.registry.Bind(s.log, s.facts)
	s.providersMu.Unlock()
	s.EnqueueFacts("/")
}

func (s *Service) EnqueueFacts(p string) {
	if s.indexer != nil && s.cfg.PipelineEnabled() {
		s.indexer.enqueue(p)
	}
}

func (s *Service) PromoteFacts(p string) {
	if s.indexer == nil || !s.cfg.PipelineEnabled() {
		return
	}
	if s.processor.active("facts") {
		return
	}
	s.indexer.enqueue(p)
	s.processor.enqueue("facts", true, s.indexer.run)
}

func (s *Service) ProcessingEnabled() bool { return s.cfg.PipelineEnabled() }
func (s *Service) Started() bool           { return s.started.Load() }
func (s *Service) HasDurableViews() bool   { return s.eventIndex != nil }
func (s *Service) IndexedEventSource() EventSource {
	if s.eventIndex != nil {
		return s.eventIndex
	}
	return s.log
}

func (s *Service) SetKnowledgeScopes(scopes []KnowledgeScope) {
	clean := make([]KnowledgeScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope.Name != "" {
			scope.Root = vfs.CleanPath(scope.Root)
			clean = append(clean, scope)
		}
	}
	s.scopesMu.Lock()
	s.scopes = clean
	s.scopesMu.Unlock()
	s.EnqueueFacts("/")
}

func (s *Service) ownerForPath(p string) string {
	p = vfs.CleanPath(p)
	s.scopesMu.RLock()
	defer s.scopesMu.RUnlock()
	owner, longest := "", -1
	for _, scope := range s.scopes {
		if pathWithinPrefix(p, scope.Root) && len(scope.Root) > longest {
			owner, longest = scope.Name, len(scope.Root)
		}
	}
	return owner
}

func (s *Service) IndexedFacts(ctx context.Context, prefix string, limit int) ([]IndexedFacts, SnapshotStatus, error) {
	if s.index == nil {
		return nil, SnapshotStatus{State: "unavailable", Error: "durable facts require sqlite storage"}, nil
	}
	rows, err := s.index.PrefixScan(ctx, prefix, limit)
	if err != nil {
		return nil, SnapshotStatus{State: "failed", Error: err.Error()}, err
	}
	status := SnapshotStatus{State: "ready", Complete: true, Coverage: "indexed content docsets only"}
	for _, row := range rows {
		if row.ComputedAt.After(status.ComputedAt) {
			status.ComputedAt = row.ComputedAt
		}
	}
	status.Updating = s.processor.active("facts")
	if len(rows) == 0 {
		status.State, status.Complete = "cold", false
	} else if status.Updating {
		status.State = "updating"
	}
	if !s.cfg.PipelineEnabled() {
		status.State, status.Updating = "disabled", false
	}
	return rows, status, nil
}

// DashboardUsage serves only a committed complete summary. Missing or stale
// work is deduplicated onto the same bounded processor used by fact warming.
func (s *Service) DashboardUsage(ctx context.Context, key string, compute func(context.Context) (Summary, error)) (UsageSnapshot, error) {
	store, ok := s.store.(*SQLiteAggregationStore)
	if !ok {
		return UsageSnapshot{Analytics: SnapshotStatus{State: "unavailable", Error: "durable usage requires sqlite storage"}}, nil
	}
	var raw []byte
	var computed int64
	var lastError string
	err := store.db.QueryRowContext(ctx, `SELECT value,computed_at,error FROM dashboard_views WHERE key=?`, key).Scan(&raw, &computed, &lastError)
	found := err == nil && len(raw) > 0
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return UsageSnapshot{}, err
	}
	result := UsageSnapshot{Analytics: SnapshotStatus{State: "cold", Complete: false}}
	if found {
		if err := json.Unmarshal(raw, &result.Summary); err != nil {
			return UsageSnapshot{}, err
		}
		result.Analytics = SnapshotStatus{State: "ready", Complete: true, ComputedAt: time.Unix(0, computed).UTC(), Coverage: "complete requested time window"}
	}
	if !s.cfg.PipelineEnabled() {
		result.Analytics.State, result.Analytics.Updating = "disabled", false
		return result, nil
	}
	if s.pipeline != nil && !s.pipeline.CaughtUp() {
		result.Analytics.Updating = true
		if found {
			result.Analytics.State = "stale"
		}
		result.Analytics.Coverage = "durable event index is catching up"
		return result, nil
	}
	stale := !found || time.Since(result.Analytics.ComputedAt) > time.Minute
	jobKey := "usage:" + key
	if stale {
		s.processor.enqueue(jobKey, true, func(jobCtx context.Context) {
			summary, computeErr := compute(jobCtx)
			if computeErr != nil {
				_, _ = store.db.ExecContext(context.WithoutCancel(jobCtx), `INSERT INTO dashboard_views(key,computed_at,error) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET error=excluded.error`, key, time.Now().UTC().UnixNano(), computeErr.Error())
				return
			}
			encoded, encodeErr := json.Marshal(summary)
			if encodeErr == nil {
				_, _ = store.db.ExecContext(context.WithoutCancel(jobCtx), `INSERT INTO dashboard_views(key,value,computed_at,error) VALUES(?,?,?,'')
ON CONFLICT(key) DO UPDATE SET value=excluded.value,computed_at=excluded.computed_at,error=''`, key, encoded, summary.ComputedAt.UnixNano())
			}
		})
		result.Analytics.Updating = true
		if found {
			result.Analytics.State = "stale"
		}
	}
	if lastError != "" {
		result.Analytics.Error = lastError
		if !found {
			result.Analytics.State = "failed"
		}
	}
	return result, nil
}

func (s *Service) DashboardMaterialized(ctx context.Context, key string, compute func(context.Context) (Materialized, error)) (Materialized, error) {
	store, ok := s.store.(*SQLiteAggregationStore)
	if !ok {
		return Materialized{Status: StatusPaused, Note: "durable analytics require sqlite storage"}, nil
	}
	key = "aggregation:" + key
	var raw []byte
	var computed int64
	var lastError string
	err := store.db.QueryRowContext(ctx, `SELECT value,computed_at,error FROM dashboard_views WHERE key=?`, key).Scan(&raw, &computed, &lastError)
	found := err == nil && len(raw) > 0
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Materialized{}, err
	}
	result := Materialized{Status: StatusPlanned, Note: "Cold build queued"}
	if found {
		if err := json.Unmarshal(raw, &result); err != nil {
			return Materialized{}, err
		}
	}
	state := &SnapshotStatus{State: "cold", Complete: false}
	if found {
		state = &SnapshotStatus{State: "ready", Complete: true, ComputedAt: time.Unix(0, computed).UTC(), Coverage: "complete requested time window"}
	}
	result.Analytics = state
	if !s.cfg.PipelineEnabled() {
		state.State = "disabled"
		return result, nil
	}
	if s.pipeline != nil && !s.pipeline.CaughtUp() {
		state.State, state.Updating, state.Coverage = "updating", true, "durable event index is catching up"
		return result, nil
	}
	if !found || time.Since(state.ComputedAt) > time.Minute {
		jobKey := "aggregation:" + key
		s.processor.enqueue(jobKey, true, func(jobCtx context.Context) {
			view, computeErr := compute(jobCtx)
			if computeErr != nil {
				_, _ = store.db.ExecContext(context.WithoutCancel(jobCtx), `INSERT INTO dashboard_views(key,computed_at,error) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET error=excluded.error`, key, time.Now().UTC().UnixNano(), computeErr.Error())
				return
			}
			view.Analytics = nil
			encoded, encodeErr := json.Marshal(view)
			if encodeErr == nil {
				_, _ = store.db.ExecContext(context.WithoutCancel(jobCtx), `INSERT INTO dashboard_views(key,value,computed_at,error) VALUES(?,?,?,'') ON CONFLICT(key) DO UPDATE SET value=excluded.value,computed_at=excluded.computed_at,error=''`, key, encoded, view.ComputedAt.UnixNano())
			}
		})
		state.Updating = true
		if found {
			state.State = "stale"
		}
	}
	if lastError != "" {
		state.Error = lastError
		if !found {
			state.State, result.Note = "failed", "Analytics build failed"
		}
	}
	return result, nil
}
func (s *Service) RegisterAggregations(aggregations []Aggregation) error {
	return s.registry.RegisterAll(aggregations)
}
func (s *Service) NewContentFacts(fs vfs.FileSystem) ContentFacts {
	s.providersMu.RLock()
	providers := append([]ContentScalarProvider(nil), s.providers...)
	s.providersMu.RUnlock()
	return newIndexedContentFacts(fs, s.fs, s.index, &s.indexLog, providers)
}

func (s *Service) HasFactsIndex() bool { return s.index != nil }

func (s *Service) CurrentFacts(ctx context.Context, p string, info *vfs.FileInfo, read func() ([]byte, error)) (DocScalars, error) {
	s.providersMu.RLock()
	providers := append([]ContentScalarProvider(nil), s.providers...)
	s.providersMu.RUnlock()
	if s.index != nil {
		fact, hit, err := s.index.Lookup(ctx, p, info.Size(), info.ModTime().UnixNano(), sourceNames(providers))
		if err == nil && hit {
			owner := s.ownerForPath(p)
			if fact.Owner != owner {
				fact.Owner = owner
				if err := s.index.Upsert(ctx, fact); err != nil {
					return DocScalars{}, err
				}
			}
			return docScalarsFromIndexed(fact, providers)
		}
		if err != nil {
			s.indexLog.Do(func() { log.Printf("analytics facts index unavailable; computing uncached: %v", err) })
		}
	}
	content, err := read()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && s.index != nil {
			_ = s.index.Delete(ctx, p)
		}
		return DocScalars{}, err
	}
	if s.index == nil {
		doc := computeScalars(p, content, providers)
		doc.Scalars["characters"] = float64(utf8.RuneCount(content))
		return doc, nil
	}
	// Do not publish content under metadata observed before a concurrent write.
	// The write hook (or periodic reconciliation) will retry this file.
	if s.ownerForPath(p) != "" {
		if current, statErr := s.fs.Stat(p); statErr == nil && (current.Size() != int64(len(content)) || current.ModTime().UnixNano() != info.ModTime().UnixNano()) {
			s.EnqueueFacts(p)
			return DocScalars{}, fmt.Errorf("%s changed during analytics indexing", p)
		}
	}
	fact := indexedFacts(p, info, content, providers)
	fact.Owner = s.ownerForPath(p)
	if err := s.index.Upsert(ctx, fact); err != nil {
		s.indexLog.Do(func() { log.Printf("analytics facts index unavailable; computing uncached: %v", err) })
	}
	return docScalarsFromIndexed(fact, providers)
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
			select {
			case <-s.processor.done:
			case <-ctx.Done():
				if s.closeErr == nil {
					s.closeErr = ctx.Err()
				}
			}
		}
		if err := s.log.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
		if s.index != nil {
			if err := s.index.Close(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
		if err := s.store.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}
