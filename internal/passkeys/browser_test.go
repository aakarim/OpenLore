package passkeys

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type browserTestFS struct{}

func (browserTestFS) Stat(string) (*vfs.FileInfo, error) { return nil, nil }
func (browserTestFS) ReadFile(string) ([]byte, error)    { return nil, nil }
func (browserTestFS) ReadDir(string) ([]vfs.FileInfo, error) {
	return []vfs.FileInfo{
		{FileName: "guides", Dir: true},
		{FileName: "README.md"},
	}, nil
}

func TestRenderFileIncludesBreadcrumbsIframeAndParentCloseLink(t *testing.T) {
	pk := &Passkeys{}
	req := httptest.NewRequest("GET", "/lore/guides/setup.md", nil)
	rec := httptest.NewRecorder()

	pk.renderFile(rec, req, "/lore", "/guides/setup.md", "adil", []FileHistoryEntry{{
		Time: time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC), Attribution: "adil/claude", Action: "write", Hash: "abc123",
	}}, true)

	body := rec.Body.String()
	for _, want := range []string{
		`rel="manifest" href="/lore/manifest.webmanifest"`,
		`apple-mobile-web-app-capable`,
		`navigator.serviceWorker.register("/lore/service-worker.js")`,
		`href="/lore/"`,
		`href="/lore/guides/"`,
		`aria-current="page">setup.md`,
		`href="/lore/guides" aria-label="Close file and return to folder"`,
		`src="/lore/guides/setup.md?raw=1"`,
		`id="history-toggle"`,
		`aria-controls="file-history" aria-expanded="true"`,
		`<h2>Edit history</h2>`,
		`class="history-action">write`,
		`class="history-actor">adil/claude`,
		`datetime="2026-08-19T12:30:00Z"`,
		`title="abc123">abc123`,
		`history.hidden=open`,
		`id="identity-button"`,
		`<span class="identity-name">adil</span>`,
		`href="/settings/permissions">Permission settings`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("file view missing %q", want)
		}
	}
}

func TestLoreBrowserServesPWAAssetsWithoutAuthentication(t *testing.T) {
	pk := &Passkeys{cfg: Config{LorePath: "/knowledge"}}
	handler := pk.LoreBrowserHandler(nil, nil)

	t.Run("manifest", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/knowledge/manifest.webmanifest", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/manifest+json" {
			t.Errorf("Content-Type = %q", got)
		}
		var manifest map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("manifest JSON: %v", err)
		}
		if got := manifest["display"]; got != "standalone" {
			t.Errorf("display = %q", got)
		}
		if got := manifest["start_url"]; got != "/knowledge/" {
			t.Errorf("start_url = %q", got)
		}
	})

	t.Run("service worker", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/knowledge/service-worker.js", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "clients.claim") {
			t.Fatalf("service worker response: status=%d body=%q", rec.Code, rec.Body.String())
		}
	})
}

func TestRenderDirUsesSingleTapActionsAndDoubleTapNavigation(t *testing.T) {
	pk := &Passkeys{}
	rec := httptest.NewRecorder()

	pk.renderDir(rec, browserTestFS{}, "/lore", "/docs", "adil")

	body := rec.Body.String()
	for _, want := range []string{
		`class="entry dir" href="/lore/docs/guides/" data-path="/docs/guides"`,
		`class="entry" href="/lore/docs/README.md" data-path="/docs/README.md"`,
		`class="action-hint">Double tap to open`,
		`data-open>Open`,
		`data-copy="url">Copy URL`,
		`data-copy="path">Copy path<span class="action-subtext">Copy for agent</span>`,
		`window.location.assign(selected.href)`,
		`setTimeout(()=>{tapTimer=null;show(link)},275)`,
		`window.location.assign(link.href)`,
		`class="brand" href="/lore/"`,
		`id="identity-button"`,
		`<span class="identity-name">adil</span>`,
		`href="/settings/permissions">Permission settings`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("directory view missing %q", want)
		}
	}
}

func TestRenderMarkdownFormatsGFMAndDoesNotRenderRawHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	source := []byte("---\ntype: guide\ntags:\n  - setup\n  - web\n---\n# Guide\n\n| A | B |\n| - | - |\n| one | two |\n\n<script>alert('no')</script>\n")

	if err := renderMarkdown(rec, source); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<h1>Guide</h1>",
		"<table>",
		`<base target="_top">`,
		`<section class="frontmatter"`,
		"type: guide\ntags:\n  - setup\n  - web",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered markdown missing %q", want)
		}
	}
	if strings.Contains(body, "<hr>") {
		t.Fatal("frontmatter delimiters must not be rendered as Markdown")
	}
	if strings.Contains(body, "<script>") {
		t.Fatal("raw HTML must not be rendered into the markdown document")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}
