package openlore

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
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
	for _, want := range []string{"OpenLore analytics (experimental)", "Top commands", "stat", "Tree size", "README.md", "Top search queries", "planned"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestAnalyticsQueryParamsParsesWindow(t *testing.T) {
	r := httptest.NewRequest("GET", "/analytics/top-commands?since=7d&until=now&limit=12&transport=mcp&fresh=true", nil)
	p := queryParams(r)
	if p.Limit != 12 || p.Extra["transport"] != "mcp" || p.Extra["fresh"] != "" {
		t.Fatalf("unexpected params: %#v", p)
	}
	if got := p.Until.Sub(p.Since); got < 7*24*time.Hour-time.Second || got > 7*24*time.Hour+time.Second {
		t.Fatalf("window = %s", got)
	}
}
