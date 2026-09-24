package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestReferenceIsCurrent fails when docs/openlore-yml.md does not match what
// the generator produces, so a config change cannot land without regenerating
// the reference. Fix with: go generate ./docs
func TestReferenceIsCurrent(t *testing.T) {
	want, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../docs/openlore-yml.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("docs/openlore-yml.md is stale; run `go generate ./docs`")
	}
}

func TestYAMLSnippetNesting(t *testing.T) {
	cases := map[string]string{
		"port":                     "port: 22\n",
		"analytics.log.rotate":     "analytics:\n  log:\n    rotate: 22\n",
		"shellexec.pre_read[].cmd": "shellexec:\n  pre_read:\n    - cmd: 22\n",
		"oidc_issuers[].jwks.url":  "oidc_issuers:\n  - jwks:\n      url: 22\n",
	}
	for path, want := range cases {
		if got := yamlSnippet(path, "22"); got != want {
			t.Errorf("%s:\n got %q\nwant %q", path, got, want)
		}
	}
}

func TestEntriesAreOneSentenceInBritishEnglish(t *testing.T) {
	american := []string{"customiz", "organiz", "initializ", "behavior", "color", "license "}
	for path, e := range entries {
		if e.Description == "" {
			t.Errorf("%s: empty description", path)
		}
		if !strings.HasSuffix(e.Description, ".") {
			t.Errorf("%s: description must end with a full stop", path)
		}
		for _, word := range american {
			if strings.Contains(strings.ToLower(e.Description), word) {
				t.Errorf("%s: American spelling %q", path, word)
			}
		}
	}
}
