package okf

import (
	"strings"
	"testing"
)

func TestValidateBundle_ReportsEveryMandatoryError(t *testing.T) {
	files := []File{
		{Path: "bad.md", Content: []byte("# missing frontmatter\n")},
		{Path: "nested/index.md", Content: []byte("---\ntype: Index\n---\n# Nested\n")},
		{Path: "log.md", Content: []byte("# Updates\n\n## July 14\n- changed\n")},
		{Path: "good.md", Content: []byte("---\ntype: Note\n---\n[missing](nope.md)\n")},
	}

	got := ValidateBundle(files)
	if len(got) != 3 {
		t.Fatalf("got %d diagnostics, want 3: %+v", len(got), got)
	}
	wantRules := []string{"okf/concept", "okf/log-date", "okf/index-frontmatter"}
	for _, rule := range wantRules {
		found := false
		for _, diagnostic := range got {
			if diagnostic.Rule == rule {
				found = true
			}
		}
		if !found {
			t.Errorf("missing rule %q in %+v", rule, got)
		}
	}
	for _, diagnostic := range got {
		if strings.Contains(diagnostic.Message, "nope.md") {
			t.Fatalf("broken links are not OKF conformance errors: %+v", diagnostic)
		}
	}
}

func TestValidateBundle_RootIndexVersionIsValid(t *testing.T) {
	files := []File{{Path: "index.md", Content: []byte("---\nokf_version: \"0.1\"\n---\n# Contents\n")}}
	if got := ValidateBundle(files); len(got) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", got)
	}
}

func TestValidateBundle_ReservedStructure(t *testing.T) {
	t.Run("index accepts any markdown heading level", func(t *testing.T) {
		files := []File{{Path: "index.md", Content: []byte("## Tables\n\n- [Orders](orders.md)\n")}}
		if got := ValidateBundle(files); len(got) != 0 {
			t.Fatalf("unexpected diagnostics: %+v", got)
		}
	})
	t.Run("empty index fails", func(t *testing.T) {
		if got := ValidateBundle([]File{{Path: "index.md"}}); len(got) != 1 || got[0].Rule != "okf/index-structure" {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("log dates must be newest first", func(t *testing.T) {
		content := []byte("# Updates\n\n## 2026-01-01\n- old\n\n## 2026-02-01\n- new\n")
		got := ValidateBundle([]File{{Path: "log.md", Content: content}})
		if len(got) != 1 || got[0].Rule != "okf/log-order" {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
}

func TestLinks_FindsLocalLinksOutsideCodeFences(t *testing.T) {
	content := []byte("[relative](../tables/orders.md#schema) and [absolute](/index.md)\n```md\n[example](missing.md)\n```\n[web](https://example.com)\n")
	links := Links(content)
	if len(links) != 3 {
		t.Fatalf("got %d links, want 3: %+v", len(links), links)
	}
	if got, ok := LocalLinkPath(links[0].Destination); !ok || got != "../tables/orders.md" {
		t.Fatalf("first local link = %q, %v", got, ok)
	}
	if _, ok := LocalLinkPath(links[2].Destination); ok {
		t.Fatal("external URL must not be treated as a bundle-local path")
	}
}

func TestLinks_UsesCommonMarkAndOnlyDocumentBodyLinks(t *testing.T) {
	content := []byte(`---
type: Note
description: "[not a body link](frontmatter.md)"
---
` + "`[inline code](code.md)` ![image](image.png)\n" + `
[balanced](a_(old).md)
[reference][target]

[target]: referenced.md
`)
	links := Links(content)
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2: %+v", len(links), links)
	}
	if links[0].Destination != "a_(old).md" || links[1].Destination != "referenced.md" {
		t.Fatalf("destinations = %+v", links)
	}
	if links[0].Line != 7 {
		t.Fatalf("balanced link line = %d, want 7", links[0].Line)
	}
}
