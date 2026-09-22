package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	servermetrics "github.com/aakarim/go-openlore/internal/metrics"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type phase4Consumer struct {
	mu      sync.Mutex
	types   []string
	waitFor string
	seen    chan struct{}
	seenOne sync.Once
}

func (c *phase4Consumer) Consume(_ context.Context, event AnalyticsEvent) {
	c.mu.Lock()
	c.types = append(c.types, event.Type)
	c.mu.Unlock()
	if event.Type == c.waitFor && c.seen != nil {
		c.seenOne.Do(func() { close(c.seen) })
	}
}

type phase4Processor struct{}

func (phase4Processor) Name() string { return "phase4" }
func (phase4Processor) Process(_ context.Context, event AnalyticsEvent) []AnalyticsEvent {
	if event.Type != "plugin.quality.measured" {
		return nil
	}
	return []AnalyticsEvent{{ID: event.ID + "-processed", Type: "processed"}}
}

type phase4Scalar struct{}

func (phase4Scalar) Name() string { return "readability" }
func (phase4Scalar) Scalars(_ string, _ []byte) map[string]float64 {
	return map[string]float64{"readability": 42, "bytes": 999, "tokens": 999}
}

type phase4Tokenizer struct{}

func (phase4Tokenizer) Name() string             { return "exact-test" }
func (phase4Tokenizer) Count(content []byte) int { return len(content) }

type phase4Plugin struct {
	sink     AnalyticsSink
	consumer *phase4Consumer
}

type phase4SubscriberPlugin struct{ consumer AnalyticsConsumer }

type phase4AggregationPlugin struct {
	name         string
	aggregations []AnalyticsAggregation
}

func (p *phase4AggregationPlugin) Info() PluginInfo {
	return PluginInfo{Name: p.name, Version: "1.0.0"}
}
func (p *phase4AggregationPlugin) Aggregations() []AnalyticsAggregation { return p.aggregations }

type phase4FilterPlugin struct {
	consumer *phase4Consumer
	filters  []meta.Filter
}

func (*phase4FilterPlugin) Info() PluginInfo {
	return PluginInfo{Name: "filter-analytics", Version: "1.0.0"}
}
func (p *phase4FilterPlugin) AnalyticsConsumers() []AnalyticsConsumer {
	return []AnalyticsConsumer{p.consumer}
}
func (p *phase4FilterPlugin) MetaFilters() []meta.Filter { return p.filters }

func (*phase4SubscriberPlugin) Info() PluginInfo {
	return PluginInfo{Name: "runtime-subscriber", Version: "1.0.0"}
}
func (p *phase4SubscriberPlugin) AnalyticsConsumers() []AnalyticsConsumer {
	return []AnalyticsConsumer{p.consumer}
}
func (*phase4SubscriberPlugin) AnalyticsProcessors() []AnalyticsProcessor {
	return []AnalyticsProcessor{phase4Processor{}}
}

func (*phase4Plugin) Info() PluginInfo                      { return PluginInfo{Name: "quality", Version: "1.0.0"} }
func (p *phase4Plugin) SetAnalyticsSink(sink AnalyticsSink) { p.sink = sink }
func (p *phase4Plugin) AnalyticsConsumers() []AnalyticsConsumer {
	return []AnalyticsConsumer{p.consumer}
}
func (*phase4Plugin) AnalyticsProcessors() []AnalyticsProcessor {
	return []AnalyticsProcessor{phase4Processor{}}
}
func (*phase4Plugin) ContentScalarProviders() []ContentScalarProvider {
	return []ContentScalarProvider{phase4Scalar{}}
}
func (*phase4Plugin) Tokenizer() AnalyticsTokenizer { return phase4Tokenizer{} }
func (*phase4Plugin) Aggregations() []AnalyticsAggregation {
	return []AnalyticsAggregation{
		{
			Name: "scores",
			Compute: func(ctx context.Context, _ AnalyticsEventSource, facts AnalyticsContentFacts, _ AnalyticsParams) (AnalyticsTable, error) {
				doc, err := facts.Stat(ctx, "/doc.md")
				if err != nil {
					return AnalyticsTable{}, err
				}
				return AnalyticsTable{Columns: []string{"score"}, Rows: [][]any{{doc.Scalars["readability"]}}}, nil
			},
		},
		{
			Name:     "events",
			Requires: []string{"plugin.quality.measured"},
			Compute: func(context.Context, AnalyticsEventSource, AnalyticsContentFacts, AnalyticsParams) (AnalyticsTable, error) {
				return AnalyticsTable{}, nil
			},
		},
	}
}

func TestAnalyticsInternalRootsFollowHostStorage(t *testing.T) {
	root := t.TempDir()
	merge := NewMergeFS()
	merge.SetRoot(NewDirFS(root, config.FilesConfig{}))
	merge.Mount("journal", NewDirFS(filepath.Join(root, "state", "history"), config.FilesConfig{}))
	merge.Mount("docs", NewDirFS(t.TempDir(), config.FilesConfig{}))
	excluded, err := analyticsInternalRoots(merge, filepath.Join(root, "state"), filepath.Join(root, "index"))
	if err != nil || !reflect.DeepEqual(excluded, []string{"/index", "/journal", "/state"}) {
		t.Fatalf("mapped storage roots=%v err=%v", excluded, err)
	}
	excluded, err = analyticsInternalRoots(merge, root+"-separate")
	if err != nil || len(excluded) != 0 {
		t.Fatalf("separate storage excluded real content: %v err=%v", excluded, err)
	}
	excluded, err = analyticsInternalRoots(NewDirFS(root, config.FilesConfig{}), root)
	if err != nil || !reflect.DeepEqual(excluded, []string{"/"}) {
		t.Fatalf("directly published storage was not excluded: %v err=%v", excluded, err)
	}
}

func TestAnalyticsInternalRootsPreservePublishedSubdirectory(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		t.Run(fmt.Sprintf("overlay=%v", overlay), func(t *testing.T) {
			dataDir := t.TempDir()
			root := filepath.Join(dataDir, "docs")
			var backend vfs.FileSystem = NewDirFS(root, config.FilesConfig{})
			if overlay {
				backend = NewOverlayFS(NewDirFS(t.TempDir(), config.FilesConfig{}), backend)
			}
			merge := NewMergeFS()
			merge.SetRoot(backend)
			merge.Mount("journal", NewDirFS(filepath.Join(dataDir, "history"), config.FilesConfig{}))
			excluded, err := analyticsInternalRoots(merge, dataDir, filepath.Join(root, "analytics"))
			if err != nil || !reflect.DeepEqual(excluded, []string{"/analytics", "/journal"}) {
				t.Fatalf("ancestor storage excluded published content: %v err=%v", excluded, err)
			}
		})
	}
}

func TestServerExcludesInternalContentButReplaysHistory(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		t.Run(fmt.Sprintf("overlay=%v", overlay), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			dataDir := filepath.Join(root, "system-state")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{"guide.md": "public", "system-state/small.json": "internal"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			opts := []config.Option{WithReadonly(false), config.WithDataDir(dataDir), func(cfg *config.Config) error {
				cfg.Analytics.Dir = filepath.Join(root, "index-cache")
				return nil
			}}
			var server *Server
			var err error
			wantFiles, wantBytes := int64(1), int64(6)
			if overlay {
				server, err = NewServerWithLowerFS(fstest.MapFS{"lower.md": {Data: []byte("lower")}}, append(opts, WithWritableDir(root))...)
				wantFiles, wantBytes = 2, 11
			} else {
				server, err = NewServer(root, opts...)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Shutdown(ctx) })
			// No doc.write event: only historical replay can emit these scalars.
			record := scalarRecord("past", "/old.md", false)
			record.Time = time.Now().UTC()
			if err := appendCommitRecord(server.historyPath, record); err != nil {
				t.Fatal(err)
			}
			server.analytics.Start(ctx)
			deadline := time.Now().Add(3 * time.Second)
			for {
				rows, totals, status, err := server.analytics.IndexedFactsForOwners(ctx, "/", []string{"public"}, 100)
				var replayed []analytics.Event
				scanErr := server.analytics.EventSource().Scan(ctx, analytics.EventFilter{Types: []string{"doc.scalars"}}, func(event analytics.Event) error {
					replayed = append(replayed, event)
					return nil
				})
				if err != nil || scanErr != nil {
					t.Fatalf("facts=%v events=%v", err, scanErr)
				}
				if status.Complete && len(replayed) == 1 {
					if totals.Files != wantFiles || totals.Bytes != wantBytes || status.Warning != "" {
						t.Fatalf("internal storage counted as knowledge: rows=%+v totals=%+v status=%+v", rows, totals, status)
					}
					if replayed[0].Fields["path"] != "/old.md" || replayed[0].Fields["after"].(map[string]any)["bytes"] != float64(4) {
						t.Fatalf("incorrect historical scalars: %+v", replayed)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("scan or replay did not finish: status=%+v events=%+v", status, replayed)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestAnalyticsPluginExtensionCapabilities(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte("four"), 0o600); err != nil {
		t.Fatal(err)
	}
	analyticsDir := filepath.Join(t.TempDir(), "analytics")
	analyticsConfig := config.AnalyticsConfig{
		Dir:      analyticsDir,
		Log:      config.AnalyticsLogConfig{Compress: "none"},
		Pipeline: config.AnalyticsPipelineConfig{Buffer: 8},
	}
	service, err := analytics.New(analyticsConfig, analytics.Deps{FS: NewDirFS(root, config.FilesConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	consumer := &phase4Consumer{}
	plugin := &phase4Plugin{consumer: consumer}
	merge := NewMergeFS()
	merge.SetRoot(NewDirFS(root, config.FilesConfig{}))
	server := &Server{analytics: service, merge: merge, config: config.Config{Readonly: true}, auth: &config.AuthConfig{}, grants: newGrantRegistry()}
	if err := server.registerPlugin(plugin); err != nil {
		t.Fatal(err)
	}
	if err := server.registerPlugin(plugin); err == nil {
		t.Fatal("duplicate plugin registration succeeded")
	}
	if plugin.sink == nil {
		t.Fatal("emitter did not receive an analytics sink")
	}
	registered := false
	for _, aggregation := range service.Registry().List() {
		registered = registered || aggregation.Name == "plugin.quality.scores"
	}
	if !registered {
		t.Fatal("plugin aggregation was not namespaced and registered")
	}
	if got := service.Registry().Status("plugin.quality.events"); got != analytics.StatusPlanned {
		t.Fatalf("plugin event aggregation status before event = %q, want planned", got)
	}
	result, err := service.Registry().Run(context.Background(), "plugin.quality.scores", analytics.Params{}, analytics.RunOptions{Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Table.Rows) != 1 || result.Table.Rows[0][0] != float64(42) {
		t.Fatalf("plugin aggregation rows = %#v", result.Table.Rows)
	}
	facts, err := service.NewContentFacts(NewDirFS(root, config.FilesConfig{})).Stat(context.Background(), "/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Scalars["readability"] != 42 || facts.Scalars["bytes"] != 4 || facts.Scalars["tokens"] != 4 || facts.Tokenizer != "exact-test" {
		t.Fatalf("plugin content facts = %#v, tokenizer %q", facts.Scalars, facts.Tokenizer)
	}
	directoryFacts, err := service.NewContentFacts(NewDirFS(root, config.FilesConfig{})).Stat(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if directoryFacts.Scalars["tokens"] != 4 || directoryFacts.Tokenizer != "exact-test" {
		t.Fatalf("plugin directory facts = %#v, tokenizer %q", directoryFacts.Scalars, directoryFacts.Tokenizer)
	}
	request := httptest.NewRequest("GET", "/analytics/facts?path=/doc.md", nil)
	request = request.WithContext(contextWithIdentity(request.Context(), Identity{}))
	scopedFacts, err := (&analyticsPlugin{service: service, server: server}).scopedFacts(request).Stat(request.Context(), "/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if scopedFacts.Scalars["readability"] != 42 || scopedFacts.Tokenizer != "exact-test" {
		t.Fatalf("plugin scoped facts = %#v, tokenizer %q", scopedFacts.Scalars, scopedFacts.Tokenizer)
	}

	service.Start(context.Background())
	plugin.sink.Record(context.Background(), AnalyticsEvent{Type: "measured"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var persisted []string
	if err := service.EventSource().Scan(context.Background(), analytics.EventFilter{}, func(event analytics.Event) error {
		persisted = append(persisted, event.Type)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !containsString(persisted, "plugin.quality.measured") || !containsString(persisted, "plugin.quality.processed") {
		t.Fatalf("persisted plugin events = %v", persisted)
	}
	if got := service.Registry().Status("plugin.quality.events"); got != analytics.StatusOK {
		t.Fatalf("plugin event aggregation status after event = %q, want ok", got)
	}
	consumer.mu.Lock()
	if !containsString(consumer.types, "plugin.quality.measured") || !containsString(consumer.types, "plugin.quality.processed") {
		consumer.mu.Unlock()
		t.Fatalf("subscriber events = %v", consumer.types)
	}
	consumer.mu.Unlock()

	restarted, err := analytics.New(analyticsConfig, analytics.Deps{FS: NewDirFS(root, config.FilesConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	restartedPlugin := &phase4Plugin{consumer: &phase4Consumer{}}
	if err := (&Server{analytics: restarted}).registerPlugin(restartedPlugin); err != nil {
		t.Fatal(err)
	}
	restarted.Start(context.Background())
	if got := restarted.Registry().Status("plugin.quality.events"); got != analytics.StatusOK {
		t.Fatalf("plugin event aggregation status after restart = %q, want ok", got)
	}
	restartCloseCtx, restartCloseCancel := context.WithTimeout(context.Background(), time.Second)
	defer restartCloseCancel()
	if err := restarted.Close(restartCloseCtx); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsSubscriberCanRegisterAfterStart(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:      filepath.Join(t.TempDir(), "analytics"),
		Log:      config.AnalyticsLogConfig{Compress: "none"},
		Pipeline: config.AnalyticsPipelineConfig{Buffer: 256},
	}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close analytics service: %v", err)
		}
	})

	recordingDone := make(chan struct{})
	go func() {
		defer close(recordingDone)
		for i := 0; i < 200; i++ {
			service.Record(context.Background(), analytics.Event{Type: "plugin.runtime.background"})
		}
	}()
	const sentinel = "plugin.runtime.sentinel"
	consumer := &phase4Consumer{waitFor: sentinel, seen: make(chan struct{})}
	server := &Server{analytics: service}
	if err := server.registerPlugin(&phase4SubscriberPlugin{consumer: consumer}); err != nil {
		t.Fatal(err)
	}
	<-recordingDone
	service.Record(context.Background(), analytics.Event{Type: sentinel})

	select {
	case <-consumer.seen:
	case <-time.After(10 * time.Second):
		t.Fatal("runtime subscriber did not receive sentinel")
	}
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	if !containsString(consumer.types, "plugin.runtime.sentinel") {
		t.Fatalf("runtime subscriber events = %v", consumer.types)
	}
}

func TestAnalyticsPluginRejectsEmptyAggregationNameBeforeRegistration(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	server := &Server{analytics: service}
	compute := func(context.Context, AnalyticsEventSource, AnalyticsContentFacts, AnalyticsParams) (AnalyticsTable, error) {
		return AnalyticsTable{}, nil
	}
	if err := server.registerPlugin(&phase4AggregationPlugin{name: "empty-aggregation", aggregations: []AnalyticsAggregation{{Name: "", Compute: compute}}}); err == nil {
		t.Fatal("empty aggregation name was accepted")
	}
	if err := server.registerPlugin(&phase4AggregationPlugin{name: "empty-aggregation", aggregations: []AnalyticsAggregation{{Name: "valid", Compute: compute}}}); err != nil {
		t.Fatalf("failed registration reserved plugin name: %v", err)
	}
}

func TestRejectedMetadataPluginDoesNotInstallAnalytics(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:      filepath.Join(t.TempDir(), "analytics"),
		Log:      config.AnalyticsLogConfig{Compress: "none"},
		Pipeline: config.AnalyticsPipelineConfig{Buffer: 8},
	}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	rejectedConsumer := &phase4Consumer{}
	acceptedConsumer := &phase4Consumer{}
	server := &Server{analytics: service, metaFilters: []meta.Filter{{Name: "taken"}}}
	if err := server.registerPlugin(&phase4FilterPlugin{consumer: rejectedConsumer, filters: []meta.Filter{{Name: "taken"}}}); err == nil {
		t.Fatal("metadata filter collision was accepted")
	}
	if err := server.registerPlugin(&phase4FilterPlugin{consumer: acceptedConsumer}); err != nil {
		t.Fatalf("rejected plugin left analytics registration behind: %v", err)
	}
	service.Start(context.Background())
	service.Record(context.Background(), analytics.Event{Type: "plugin.filter-analytics.sentinel"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	rejectedConsumer.mu.Lock()
	rejectedCount := len(rejectedConsumer.types)
	rejectedConsumer.mu.Unlock()
	acceptedConsumer.mu.Lock()
	accepted := containsString(acceptedConsumer.types, "plugin.filter-analytics.sentinel")
	acceptedConsumer.mu.Unlock()
	if rejectedCount != 0 || !accepted {
		t.Fatalf("rejected consumer count = %d, accepted consumer saw sentinel = %v", rejectedCount, accepted)
	}
}

func TestAnalyticsDashboardShowsObservedValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("one two\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:          filepath.Join(t.TempDir(), "analytics"),
		Log:          config.AnalyticsLogConfig{Compress: "none"},
		Pipeline:     config.AnalyticsPipelineConfig{Buffer: 8},
		Aggregations: config.AnalyticsAggregationConfig{Store: "file"},
	}, analytics.Deps{FS: NewDirFS(root, config.FilesConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	service.Record(context.Background(), analytics.Event{
		Type:      "command.exec",
		Principal: "dashboard-test",
		Transport: "ssh",
		SessionID: "session-test",
		Fields:    map[string]any{"command": "stat", "exit_code": 0, "duration_ms": 2},
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	(&analyticsPlugin{service: service}).dashboard(recorder, httptest.NewRequest("GET", "/analytics/", nil))
	body := recorder.Body.String()
	for _, want := range []string{"Analytics", "Top commands", "stat", "Tree size", "README.md", "Top search queries", "planned", "Download CSV"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	for _, private := range []string{"health-grid", "History cursor", "Blob bytes", "Ship lag"} {
		if strings.Contains(body, private) {
			t.Errorf("docset reader received global operator metric %q", private)
		}
	}
	requiredPanel := strings.SplitN(body, `<a href="/analytics/least-used-lines">`, 2)
	if len(requiredPanel) != 2 {
		t.Fatal("dashboard missing least-used-lines panel")
	}
	requiredPanelBody := strings.SplitN(requiredPanel[1], `</article>`, 2)[0]
	if strings.Contains(requiredPanelBody, "Download CSV") {
		t.Fatal("required-parameter panel links to an invalid CSV export")
	}
}

func TestAnalyticsDashboardShowsLiveSearchQualityResults(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:          filepath.Join(t.TempDir(), "analytics"),
		Log:          config.AnalyticsLogConfig{Compress: "none"},
		Pipeline:     config.AnalyticsPipelineConfig{Buffer: 8},
		Aggregations: config.AnalyticsAggregationConfig{Store: "file"},
	}, analytics.Deps{FS: NewDirFS(t.TempDir(), config.FilesConfig{})})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	service.Record(context.Background(), analytics.Event{
		Type:      "search.query",
		Principal: "dashboard-test",
		Fields:    map[string]any{"pattern": "missing runbook", "filled": false},
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest("GET", "/analytics/top-unfilled-queries", nil)
	request.SetPathValue("name", "top-unfilled-queries")
	recorder := httptest.NewRecorder()
	(&analyticsPlugin{service: service}).dashboard(recorder, request)
	for _, want := range []string{"top-unfilled-queries", "Status: ok", "missing runbook", "principals"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("quality dashboard missing %q: %s", want, recorder.Body.String())
		}
	}
}

func TestAnalyticsRoutesAreAbsentWithoutEnforcedAuth(t *testing.T) {
	register, err := (&analyticsPlugin{}).PrepareHTTPRoutes(&Server{authEnforced: false})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/analytics/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("dashboard status = %d, want 404", response.Code)
	}
}

func TestAnalyticsAuthenticationFailureDiffersFromPermissionDenial(t *testing.T) {
	s, mux, token := newDashboardTestServer(t)
	register, err := (&analyticsPlugin{}).PrepareHTTPRoutes(s)
	if err != nil {
		t.Fatal(err)
	}
	register(mux)
	// Keep the identity valid but remove its resource grant.
	ds := s.auth.Docsets["public"]
	delete(ds.Access.Allow, "reader")
	s.auth.Docsets["public"] = ds
	for _, endpoint := range []string{"/analytics/", "/analytics/facts", "/analytics/aggregations", "/analytics/aggregations/top-commands"} {
		for _, supplied := range []string{"", "invalid", token} {
			want := http.StatusUnauthorized
			if supplied == token {
				want = http.StatusNotFound
			}
			w := dashboardRequest(mux, "GET", endpoint, supplied)
			if w.Code != want {
				t.Fatalf("%s: got %d, want %d", endpoint, w.Code, want)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Vary") != "Cookie, Authorization" {
				t.Fatalf("failure response must remain private: %v", w.Header())
			}
		}
	}
}

func TestAnalyticsDocsetUsesConfiguredPathMapping(t *testing.T) {
	s := &Server{auth: &config.AuthConfig{Docsets: map[string]config.DocsetSpec{
		"handbook": {Paths: []config.PathMapping{{Source: "/source", Display: "/company/docs"}}, Aliases: []string{"/legacy"}},
	}}}
	for _, target := range []string{"/company/docs/intro.md", "/legacy/intro.md"} {
		if got := (&analyticsPlugin{server: s}).docsetForPath(target); got != "handbook" {
			t.Errorf("docset for %q = %q, want handbook", target, got)
		}
	}
}

func TestWriteEventPreservesInvocationAndSessionCorrelation(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:      filepath.Join(t.TempDir(), "analytics"),
		Log:      config.AnalyticsLogConfig{Compress: "none"},
		Pipeline: config.AnalyticsPipelineConfig{Buffer: 8},
	}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	p := &analyticsPlugin{service: service}
	info := CommitInfo{
		ID: "commit-1",
		Attribution: Attribution{Principal: "alice", Actor: "claude@claude.ai", Extra: map[string]string{
			"transport": "ssh", "session_id": "session-1", "client_session_id": "client-1",
			"invocation_id": "invocation-1", "parent_id": "command-1", "remote_addr": "127.0.0.1:22",
		}},
		ChangeSet: vfs.ChangeSet{Target: "/doc.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("body")}},
		Leaves:    []LeafRecord{{Target: "/doc.md", Action: vfs.ChangeActionWrite, AfterHash: hashContent([]byte("body"))}},
	}
	if err := p.observeWrites(func(context.Context, CommitInfo) error { return nil })(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var got analytics.Event
	if err := service.EventSource().Scan(context.Background(), analytics.EventFilter{Types: []string{"doc.write"}}, func(event analytics.Event) error { got = event; return nil }); err != nil {
		t.Fatal(err)
	}
	if got.InvocationID != "invocation-1" || got.ParentID != "command-1" || got.SessionID != "session-1" || got.ClientSessionID != "client-1" || got.Transport != "ssh" || got.RemoteAddr != "127.0.0.1:22" {
		t.Fatalf("correlation envelope = %#v", got)
	}
	if got.Actor != "claude@claude.ai" || got.Fields["writer"] != "agent" || got.Fields["actor_kind"] != "agent" {
		t.Fatalf("write attribution = %#v", got)
	}
}

func TestPrometheusExportPreservesJSONServerMetrics(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{
		Dir: filepath.Join(t.TempDir(), "analytics"),
		Log: config.AnalyticsLogConfig{Compress: "none"},
	}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{analytics: service, metrics: &servermetrics.Metrics{}}
	s.metrics.TotalCommands.Store(7)

	jsonResponse := httptest.NewRecorder()
	s.metricsExportHandler().ServeHTTP(jsonResponse, httptest.NewRequest("GET", "/metrics.json", nil))
	if jsonResponse.Code != 200 || !strings.Contains(jsonResponse.Body.String(), `"total_commands":7`) {
		t.Fatalf("JSON metrics response = %d %q", jsonResponse.Code, jsonResponse.Body.String())
	}

	promResponse := httptest.NewRecorder()
	s.metricsExportHandler().ServeHTTP(promResponse, httptest.NewRequest("GET", "/metrics", nil))
	if promResponse.Code != 200 || !strings.Contains(promResponse.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Prometheus response = %d content-type %q", promResponse.Code, promResponse.Header().Get("Content-Type"))
	}
}

func TestAnalyticsQueryParamsParsesWindow(t *testing.T) {
	r := httptest.NewRequest("GET", "/analytics/top-commands?since=7d&until=now&limit=12&page=3&transport=mcp&fresh=true", nil)
	p := queryParams(r)
	if p.Limit != 12 || p.Extra["transport"] != "mcp" || p.Extra["fresh"] != "" || p.Extra["page"] != "" {
		t.Fatalf("unexpected params: %#v", p)
	}
	if got := p.Until.Sub(p.Since); got < 7*24*time.Hour-time.Second || got > 7*24*time.Hour+time.Second {
		t.Fatalf("window = %s", got)
	}
}

func TestAnalyticsAggregationPaginatesAfterMaterialization(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Buffer: 16}, Aggregations: config.AnalyticsAggregationConfig{Store: "file"}}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	for _, command := range []string{"alpha", "alpha", "alpha", "beta", "beta", "gamma"} {
		service.Record(context.Background(), analytics.Event{Type: "command.exec", Fields: map[string]any{"command": command}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/analytics/aggregations/top-commands?limit=1&page=2&fresh=true", nil)
	request.SetPathValue("name", "top-commands")
	recorder := httptest.NewRecorder()
	(&analyticsPlugin{service: service}).aggregation(recorder, request)
	var materialized analytics.Materialized
	if err := json.Unmarshal(recorder.Body.Bytes(), &materialized); err != nil {
		t.Fatalf("response %d is not materialized JSON: %v\n%s", recorder.Code, err, recorder.Body.String())
	}
	if len(materialized.Table.Rows) != 1 || materialized.Table.Rows[0][0] != "beta" {
		t.Fatalf("page 2 rows = %#v, want beta", materialized.Table.Rows)
	}
	if materialized.Window.Limit != 1 || materialized.Window.Extra["_offset"] != "" {
		t.Fatalf("presentation window leaked offset into aggregation params: %#v", materialized.Window)
	}
}

func TestAnalyticsAggregationLargePageReturnsEmpty(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	request := httptest.NewRequest("GET", fmt.Sprintf("/analytics/aggregations/top-commands?limit=100&page=%d&fresh=true", int(^uint(0)>>1)), nil)
	request.SetPathValue("name", "top-commands")
	recorder := httptest.NewRecorder()
	(&analyticsPlugin{service: service}).aggregation(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("large page response = %d: %s", recorder.Code, recorder.Body.String())
	}
	var materialized analytics.Materialized
	if err := json.Unmarshal(recorder.Body.Bytes(), &materialized); err != nil || len(materialized.Table.Rows) != 0 {
		t.Fatalf("large page rows = %#v, err=%v", materialized.Table.Rows, err)
	}
}

func TestAnalyticsAggregationCSVDownloadsAllRows(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Buffer: 8}, Aggregations: config.AnalyticsAggregationConfig{Store: "file"}}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	for _, command := range []string{"cat", "stat"} {
		service.Record(context.Background(), analytics.Event{Type: "command.exec", Fields: map[string]any{"command": command}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/analytics/aggregations/top-commands?format=csv&limit=1", nil)
	request.SetPathValue("name", "top-commands")
	recorder := httptest.NewRecorder()
	(&analyticsPlugin{service: service}).aggregation(recorder, request)
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Fatalf("content type = %q", contentType)
	}
	for _, want := range []string{"command,count,principals,sessions,error_rate,p50_ms", "cat", "stat"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("CSV missing %q: %s", want, recorder.Body.String())
		}
	}
}

func TestAnalyticsAggregationCSVNeverUsesGlobalMaterialization(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Buffer: 8}, Aggregations: config.AnalyticsAggregationConfig{Store: "file"}}, analytics.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	now := time.Now().UTC()
	for _, event := range []analytics.Event{
		{ID: "allowed-command", Time: now, Type: "command.exec", InvocationID: "allowed", Fields: map[string]any{"command": "cat"}},
		{ID: "allowed-read", Time: now, Type: "doc.read", InvocationID: "allowed", ParentID: "allowed-command", Fields: map[string]any{"path": "/docs/note.md"}},
		{ID: "secret-command", Time: now, Type: "command.exec", InvocationID: "secret", Fields: map[string]any{"command": "secret-command"}},
		{ID: "secret-read", Time: now, Type: "doc.read", InvocationID: "secret", ParentID: "secret-command", Fields: map[string]any{"path": "/docs/private/secret.md"}},
	} {
		service.Record(context.Background(), event)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Seed the shared cache with both commands. The scoped CSV must neither read
	// this result nor write its caller-specific result back to the cache.
	if _, err := service.Registry().Run(context.Background(), "top-commands", analytics.Params{Extra: map[string]string{}}, analytics.RunOptions{}); err != nil {
		t.Fatal(err)
	}

	server, alice, _ := analyticsScopeServer()
	server.analytics = service
	request := httptest.NewRequest("GET", "/analytics/aggregations/top-commands?format=csv&path=/docs", nil)
	request.SetPathValue("name", "top-commands")
	request = request.WithContext(contextWithIdentity(request.Context(), alice))
	recorder := httptest.NewRecorder()
	(&analyticsPlugin{service: service, server: server}).aggregation(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "cat") || strings.Contains(recorder.Body.String(), "secret-command") {
		t.Fatalf("scoped CSV response = %d: %s", recorder.Code, recorder.Body.String())
	}
}
