package okf

import (
	"strings"
	"testing"
)

func TestValidate_Concept(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		wantErr bool
	}{
		{
			name: "valid concept",
			path: "tables/orders.md",
			content: `---
type: BigQuery Table
title: Orders
---

# Schema
`,
			wantErr: false,
		},
		{
			name: "valid concept with only type",
			path: "notes/x.md",
			content: `---
type: Note
---
body
`,
			wantErr: false,
		},
		{
			name:    "missing frontmatter",
			path:    "tables/orders.md",
			content: "# Just a heading\n\nno frontmatter here\n",
			wantErr: true,
		},
		{
			name: "missing type field",
			path: "tables/orders.md",
			content: `---
title: Orders
description: no type
---
body
`,
			wantErr: true,
		},
		{
			name: "empty type field",
			path: "tables/orders.md",
			content: `---
type: ""
---
body
`,
			wantErr: true,
		},
		{
			name: "whitespace-only type field",
			path: "tables/orders.md",
			content: `---
type: "   "
---
body
`,
			wantErr: true,
		},
		{
			name: "unparseable yaml frontmatter",
			path: "tables/orders.md",
			content: `---
type: [unterminated
---
body
`,
			wantErr: true,
		},
		{
			name: "opening delimiter but no closing",
			path: "tables/orders.md",
			content: `---
type: BigQuery Table
no closing delimiter
`,
			wantErr: true,
		},
		{
			name:    "crlf line endings",
			path:    "tables/orders.md",
			content: "---\r\ntype: BigQuery Table\r\n---\r\nbody\r\n",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.path, []byte(tt.content))
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestValidate_ReservedFiles(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		wantErr bool
	}{
		{
			name:    "index.md without frontmatter is fine",
			path:    "index.md",
			content: "# Section\n\n* [x](x.md) - a thing\n",
			wantErr: false,
		},
		{
			name:    "nested index.md without frontmatter is fine",
			path:    "tables/index.md",
			content: "# Tables\n\n* [orders](orders.md)\n",
			wantErr: false,
		},
		{
			name: "root index.md with okf_version frontmatter is fine",
			path: "index.md",
			content: `---
okf_version: "0.1"
---

# Root
`,
			wantErr: false,
		},
		{
			name:    "log.md without frontmatter is fine",
			path:    "log.md",
			content: "# Update Log\n\n## 2026-05-22\n* **Update**: something\n",
			wantErr: false,
		},
		{
			name: "reserved file with unparseable frontmatter fails",
			path: "index.md",
			content: `---
okf_version: [bad
---
body
`,
			wantErr: true,
		},
		{
			name:    "reserved file does not require a type",
			path:    "log.md",
			content: "no frontmatter, no type, still ok\n",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.path, []byte(tt.content))
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestValidate_NonUTF8(t *testing.T) {
	// An invalid UTF-8 byte sequence.
	content := []byte{'-', '-', '-', '\n', 't', 'y', 'p', 'e', ':', ' ', 0xff, 0xfe, '\n', '-', '-', '-', '\n'}
	if err := Validate("x.md", content); err == nil {
		t.Fatal("expected error for non-UTF-8 content")
	}
}

func TestValidate_ReservedFileWithUnclosedFrontmatter(t *testing.T) {
	if err := Validate("index.md", []byte("---\nokf_version: 0.1\n")); err == nil {
		t.Fatal("expected unclosed reserved-file frontmatter to fail")
	}
}

func TestIsReserved(t *testing.T) {
	cases := map[string]bool{
		"index.md":        true,
		"log.md":          true,
		"a/b/index.md":    true,
		"/deep/log.md":    true,
		"orders.md":       false,
		"my-index.md":     false,
		"indexing.md":     false,
		"tables/index.md": true,
	}
	for p, want := range cases {
		if got := IsReserved(p); got != want {
			t.Errorf("IsReserved(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	content := `---
type: BigQuery Table
title: Orders
tags: [a, b]
---

# Body
line two
`
	meta, body, ok, err := ParseFrontmatter([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected frontmatter to be found")
	}
	if meta["type"] != "BigQuery Table" {
		t.Errorf("type = %v, want %q", meta["type"], "BigQuery Table")
	}
	if !strings.HasPrefix(strings.TrimLeft(string(body), "\n"), "# Body") {
		t.Errorf("body = %q, want it to start with '# Body'", string(body))
	}
}

func TestParseFrontmatter_None(t *testing.T) {
	content := []byte("# No frontmatter\n")
	meta, body, ok, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when there is no frontmatter")
	}
	if meta != nil {
		t.Errorf("meta = %v, want nil", meta)
	}
	if string(body) != string(content) {
		t.Errorf("body = %q, want original content", string(body))
	}
}
