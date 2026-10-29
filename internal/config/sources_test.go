package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesValidation(t *testing.T) {
	valid := "version: \"1\"\nsources:\n  - name: notes\n    type: folder\n    path: ~/notes\n    include: ['**/*.md']\n"
	if _, err := ParseSources([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(valid, "path:", "paht:", 1),
		strings.Replace(valid, "folder", "unknown", 1),
		strings.Replace(valid, "notes\n", "../notes\n", 1),
		strings.Replace(valid, "'**/*.md'", "'['", 1),
		valid + "---\nversion: '1'\n",
		valid + "  - name: notes\n    type: folder\n    path: ~/other\n",
		strings.Replace(valid, "notes\n", "snapshots\n", 1),
	} {
		if _, err := ParseSources([]byte(input)); err == nil {
			t.Errorf("accepted invalid source: %s", input)
		}
	}
	// Remotes on different ports are different servers; a repository called
	// snapshots would collide with the docstore's snapshots route.
	for raw, want := range map[string]string{
		"git@github.com:acme/docs.git":           "git/github.com/acme/docs",
		"ssh://git@example.com:2222/acme/docs":   "git/example.com_2222/acme/docs",
		"https://example.com:8443/acme/docs.git": "git/example.com_8443/acme/docs",
		"https://github.com/acme/snapshots":      "",
	} {
		got, err := GitSourceStore(raw)
		if (err == nil) != (want != "") || got != want {
			t.Errorf("GitSourceStore(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	root := t.TempDir()
	file := filepath.Join(root, "openlore.yml")
	if err := os.WriteFile(file, []byte("version: '1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(WithConfigFile(file)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "home", "alice"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HomeSourcesFile(root, "alice"), []byte("version: '1'\nsources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(WithConfigFile(file)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HomeSourcesFile(root, "alice"), []byte("version: '2'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(WithConfigFile(file)); err == nil || !strings.Contains(err.Error(), filepath.Join("home", "alice", "sources.yaml")) {
		t.Fatalf("startup accepted invalid home/alice/sources.yaml: %v", err)
	}
}
