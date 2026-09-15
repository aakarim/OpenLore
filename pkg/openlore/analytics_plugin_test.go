package openlore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	servermetrics "github.com/aakarim/go-openlore/internal/metrics"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestAnalyticsDashboardShowsObservedValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("one two\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:      filepath.Join(t.TempDir(), "analytics"),
		Log:      config.AnalyticsLogConfig{Compress: "none"},
		Pipeline: config.AnalyticsPipelineConfig{Buffer: 8},
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
	for _, want := range []string{"Analytics", "Top commands", "stat", "Tree size", "README.md", "Top search queries", "planned", "Health", "Download CSV"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestAnalyticsDashboardShowsLiveSearchQualityResults(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:      filepath.Join(t.TempDir(), "analytics"),
		Log:      config.AnalyticsLogConfig{Compress: "none"},
		Pipeline: config.AnalyticsPipelineConfig{Buffer: 8},
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
		Attribution: Attribution{Principal: "alice", Extra: map[string]string{
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
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Buffer: 16}}, analytics.Deps{})
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

func TestAnalyticsAggregationCSVDownloadsAllRows(t *testing.T) {
	service, err := analytics.New(config.AnalyticsConfig{Dir: filepath.Join(t.TempDir(), "analytics"), Log: config.AnalyticsLogConfig{Compress: "none"}, Pipeline: config.AnalyticsPipelineConfig{Buffer: 8}}, analytics.Deps{})
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
