package passkeys

import (
	"encoding/json"
	"fmt"
	"image/png"
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
	}}, true, &ContentFacts{Bytes: 42, Lines: 3, Tokens: 11, Tokenizer: "approx"})

	body := rec.Body.String()
	for _, want := range []string{
		`rel="manifest" href="/lore/manifest.webmanifest"`,
		`rel="apple-touch-icon" href="/lore/apple-touch-icon.png" sizes="180x180"`,
		`apple-mobile-web-app-capable`,
		`navigator.serviceWorker.register("/lore/service-worker.js")`,
		`href="/lore/"`,
		`href="/lore/guides/"`,
		`aria-current="page">setup.md`,
		`href="/lore/guides" aria-label="Close file and return to folder"`,
		`src="/lore/guides/setup.md?raw=1"`,
		`id="history-toggle"`,
		`aria-controls="file-history" aria-expanded="false"`,
		`id="file-history" aria-label="Edit history" hidden`,
		`<h2>Edit history</h2>`,
		`class="history-action">write`,
		`class="history-actor">adil/claude`,
		`datetime="2026-08-19T12:30:00Z"`,
		`title="abc123">abc123`,
		`setOpen(!window.matchMedia('(max-width:700px)').matches)`,
		`history.hidden=!open`,
		`id="identity-button"`,
		`<span class="identity-name">adil</span>`,
		`href="/settings/permissions">Permission settings`,
		`42 bytes · 3 lines · ~11 tokens · approx`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("file view missing %q", want)
		}
	}
}

func TestLoreBrowserServesPWAAssetsWithoutAuthentication(t *testing.T) {
	pk := &Passkeys{cfg: Config{LorePath: "/knowledge"}}
	handler := pk.LoreBrowserHandler(nil, nil, nil)

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
		if got := manifest["scope"]; got != "/" {
			t.Errorf("scope = %q; must include both knowledge and dashboard routes", got)
		}
		var install struct {
			Icons []struct{ Src, Sizes, Type string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &install); err != nil {
			t.Fatal(err)
		}
		pngSizes := map[string]bool{"192x192": false, "512x512": false}
		for _, icon := range install.Icons {
			if icon.Type != "image/png" {
				continue
			}
			iconRec := httptest.NewRecorder()
			handler.ServeHTTP(iconRec, httptest.NewRequest(http.MethodGet, icon.Src, nil))
			if iconRec.Code != http.StatusOK || iconRec.Header().Get("Content-Type") != icon.Type {
				t.Fatalf("%s: status=%d Content-Type=%q", icon.Src, iconRec.Code, iconRec.Header().Get("Content-Type"))
			}
			image, err := png.Decode(iconRec.Body)
			if err != nil {
				t.Fatalf("%s: %v", icon.Src, err)
			}
			if size := fmt.Sprintf("%dx%d", image.Bounds().Dx(), image.Bounds().Dy()); size != icon.Sizes {
				t.Errorf("%s: image size=%s, manifest size=%s", icon.Src, size, icon.Sizes)
			}
			pngSizes[icon.Sizes] = true
		}
		for size, found := range pngSizes {
			if !found {
				t.Errorf("manifest missing %s PNG fallback", size)
			}
		}
	})

	t.Run("app icons", func(t *testing.T) {
		for _, path := range []string{"/knowledge/app-icon.svg", "/knowledge/apple-touch-icon.svg"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
				t.Errorf("%s: status=%d Content-Type=%q", path, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/knowledge/apple-touch-icon.png", nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("Apple icon: status=%d Content-Type=%q", rec.Code, rec.Header().Get("Content-Type"))
		}
		image, err := png.Decode(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		if image.Bounds().Dx() != 180 || image.Bounds().Dy() != 180 {
			t.Errorf("Apple icon size = %v; want 180x180", image.Bounds())
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

	pk.renderDir(rec, browserTestFS{}, "/lore", "/docs", "adil", func(vfs.FileSystem, string) (ContentFacts, error) {
		return ContentFacts{Bytes: 20, Lines: 2, Tokens: 5}, nil
	})

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
		`20 bytes · 2 lines · ~5 tokens`,
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

func TestMarkdownFragmentCannotExecuteDocumentHTML(t *testing.T) {
	fragment, err := RenderMarkdownFragment([]byte("---\ntitle: '<img src=x onerror=alert(1)>'\n---\n# Safe\n\n[link](javascript:alert%281%29)\n\n<script>alert(1)</script>\n\n![image](data:text/html;base64,PHNjcmlwdD4=)\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fragment, "<h1>Safe</h1>") || !strings.Contains(fragment, "&lt;img") {
		t.Fatalf("missing rendered body or escaped frontmatter: %s", fragment)
	}
	for _, unsafe := range []string{"<script", "<img src=x", `href="javascript:`, `src="data:text/html`} {
		if strings.Contains(fragment, unsafe) {
			t.Fatalf("unsafe fragment contains %q: %s", unsafe, fragment)
		}
	}
}
