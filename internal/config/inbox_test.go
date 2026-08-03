package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInboxConfigDefaultsAndParse(t *testing.T) {
	cfg, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inbox.MaxUploadSize != 10*1024*1024 || cfg.Inbox.AllowedTypes[".md"] != "text/markdown" {
		t.Fatalf("defaults = %#v", cfg.Inbox)
	}
	path := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(path, []byte("inbox:\n  max_upload_size: 2MB\n  allowed_types:\n    - extensions: ['.txt']\n      mime: text/plain\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = New(WithConfigFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inbox.MaxUploadSize != 2*1024*1024 || cfg.Inbox.AllowedTypes[".txt"] != "text/plain" {
		t.Fatalf("parsed = %#v", cfg.Inbox)
	}
}

func TestInboxConfigValidation(t *testing.T) {
	for _, body := range []string{
		"inbox:\n  max_upload_size: 9999999999999999999GB\n",
		"inbox:\n  allowed_types:\n    - extensions: ['md']\n      mime: text/markdown\n",
		"inbox:\n  allowed_types:\n    - extensions: ['.md', '.md']\n      mime: text/markdown\n",
		"inbox:\n  allowed_types:\n    - extensions: ['.md']\n      mime: not a mime\n",
	} {
		path := filepath.Join(t.TempDir(), "openlore.yml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(WithConfigFile(path)); err == nil {
			t.Fatalf("accepted invalid config:\n%s", body)
		}
	}
}
