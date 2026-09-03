package rules

import (
	"strings"
	"testing"
)

func TestDecodeFileStrictAndReservedSections(t *testing.T) {
	valid := "version: 1\nrules:\n  short:\n    match: ['**/*.md']\n    use: size/lines\n    with: {max: 3}\nhooks: {}\n"
	config, err := DecodeFile([]byte(valid))
	if err != nil || config.Rules["short"].Use != "size/lines" {
		t.Fatalf("DecodeFile = %#v, %v", config, err)
	}
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"version: 1\nhooks: {x: {}}\n", "hooks: not supported yet"},
		{"version: 1\nunknown: true\n", "field unknown not found"},
		{"version: 2\n", "version: expected 1"},
		{"version: 1\nrules:\n  x:\n    use: size/lines\n    matc: ['*.md']\n", `rules.x: unknown key "matc" (did you mean "match"?)`},
		{"rules: {}\n", "version: required (expected 1)"},
	} {
		if _, err := DecodeFile([]byte(tc.content)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("DecodeFile(%q) error = %v, want %q", tc.content, err, tc.want)
		}
	}
}

func TestIsDirConfigPathRejectsNestedLore(t *testing.T) {
	if !IsDirConfigPath("/docs/backend/.lore/config.yaml") {
		t.Fatal("folder config was not recognized")
	}
	if IsDirConfigPath("/docs/.lore/xattrs/.lore/config.yaml") {
		t.Fatal("nested .lore path was recognized as a folder config")
	}
}
