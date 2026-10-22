package docs

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// navEntry is either a page ({path, label}) or a subgroup ({subgroup, pages}).
type navEntry struct {
	Path     string     `yaml:"path"`
	Label    string     `yaml:"label"`
	Subgroup string     `yaml:"subgroup"`
	Pages    []navEntry `yaml:"pages"`
}

type navFile struct {
	Groups []struct {
		Name  string     `yaml:"name"`
		Pages []navEntry `yaml:"pages"`
	} `yaml:"groups"`
}

// TestNavCoversEveryPage keeps docs/nav.yml and the Markdown files in sync, so
// a new page cannot be published without a place in the openlore.sh sidebar and
// the sidebar cannot point at a page that no longer exists.
func TestNavCoversEveryPage(t *testing.T) {
	raw, err := os.ReadFile("nav.yml")
	if err != nil {
		t.Fatal(err)
	}
	var nav navFile
	if err := yaml.Unmarshal(raw, &nav); err != nil {
		t.Fatalf("nav.yml: %v", err)
	}

	listed := map[string]bool{}
	slugs := map[string]string{}
	var walk func(group string, entries []navEntry, nested bool)
	walk = func(group string, entries []navEntry, nested bool) {
		for _, e := range entries {
			switch {
			case e.Subgroup != "":
				if nested {
					t.Errorf("%s: subgroup %q is nested inside a subgroup", group, e.Subgroup)
				}
				if e.Path != "" || e.Label != "" || len(e.Pages) == 0 {
					t.Errorf("%s: subgroup %q must have pages and nothing else", group, e.Subgroup)
				}
				walk(group+" > "+e.Subgroup, e.Pages, true)
			case e.Path != "":
				if e.Label == "" || len(e.Pages) != 0 {
					t.Errorf("%s: page %q must have a label and nothing else", group, e.Path)
				}
				if listed[e.Path] {
					t.Errorf("%s: %q is listed more than once", group, e.Path)
				}
				listed[e.Path] = true
				slug := strings.TrimSuffix(path.Base(e.Path), ".md")
				if other, ok := slugs[slug]; ok {
					t.Errorf("%q and %q would share the URL /docs/%s/; file names must be unique", other, e.Path, slug)
				}
				slugs[slug] = e.Path
			default:
				t.Errorf("%s: entry needs either path or subgroup: %+v", group, e)
			}
		}
	}
	for _, g := range nav.Groups {
		if g.Name == "" {
			t.Error("group without a name")
		}
		walk(g.Name, g.Pages, false)
	}

	found := map[string]bool{}
	err = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			found[filepath.ToSlash(p)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for p := range found {
		if !listed[p] {
			t.Errorf("%s is not listed in nav.yml", p)
		}
	}
	for p := range listed {
		if !found[p] {
			t.Errorf("nav.yml lists %s, which does not exist", p)
		}
	}
}
