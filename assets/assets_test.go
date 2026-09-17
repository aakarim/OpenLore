package assets

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDefaultSiteUsesPublicServerMetadata(t *testing.T) {
	index, err := fs.ReadFile(Site(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)

	for _, content := range []string{
		"fetch('/.well-known/openlore')",
		"location.hostname",
		"metadata.ssh.port",
		"metadata.routes[name]",
	} {
		if !strings.Contains(html, content) {
			t.Fatalf("index.html does not contain %q", content)
		}
	}
	if _, err := fs.Stat(Site(), "404.html"); err != nil {
		t.Fatal("default site does not include 404.html")
	}
}

func TestDashboardRequiresGeneratedIndex(t *testing.T) {
	if got := dashboard(fstest.MapFS{".gitkeep": {}}); got != nil {
		t.Fatal("dashboard without dist/index.html must be nil")
	}

	got := dashboard(fstest.MapFS{"dist/index.html": {Data: []byte("dashboard")}})
	if got == nil {
		t.Fatal("dashboard with dist/index.html must be available")
	}
	index, err := fs.ReadFile(got, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(index) != "dashboard" {
		t.Fatalf("index.html = %q, want dashboard", index)
	}
}
