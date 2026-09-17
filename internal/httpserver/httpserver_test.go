package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSiteHandlerUsesSite404(t *testing.T) {
	h := siteHandler(fstest.MapFS{
		"index.html":      {Data: []byte("home")},
		"docs/index.html": {Data: []byte("docs")},
		"404.html":        {Data: []byte("site missing")},
	})

	for target, want := range map[string]string{"/": "home", "/docs/": "docs"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", target, response.Code, response.Body.String(), want)
		}
	}

	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if response.Code != http.StatusNotFound || response.Body.String() != "site missing" {
		t.Fatalf("missing page = %d %q, want site 404", response.Code, response.Body.String())
	}

	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/missing", nil))
	if head.Code != http.StatusNotFound {
		t.Fatalf("HEAD missing = %d, want 404", head.Code)
	}
	if body, _ := io.ReadAll(head.Result().Body); len(body) != 0 {
		t.Fatalf("HEAD missing returned body %q", body)
	}
}
