package okf

import (
	"strings"
	"testing"
)

func concept(t *testing.T, frontmatter string) Concept {
	t.Helper()
	content := []byte("---\n" + frontmatter + "\n---\nbody\n")
	meta, body, ok, err := ParseFrontmatter(content)
	if !ok || err != nil {
		t.Fatalf("fixture frontmatter did not parse: ok=%v err=%v", ok, err)
	}
	return Concept{Path: "doc.md", Meta: meta, Body: body}
}

func rules(diagnostics []Diagnostic) []string {
	var out []string
	for _, d := range diagnostics {
		out = append(out, d.Rule)
	}
	return out
}

func TestFamilyChecks_AbsentFamiliesAreNeverFindings(t *testing.T) {
	c := concept(t, "type: Note")
	for _, check := range FamilyChecksV02 {
		if got := check(c); len(got) != 0 {
			t.Errorf("check reported %+v for a concept with only 'type'", got)
		}
	}
	for _, check := range FamilyChecksV01 {
		if got := check(c); len(got) != 0 {
			t.Errorf("v0.1 check reported %+v for a concept with only 'type'", got)
		}
	}
}

func TestFamilyChecks_WellFormedV02FamiliesPass(t *testing.T) {
	c := concept(t, strings.TrimSpace(`
type: Metric
sources:
  - resource: https://example.com/source
    id: src
    usage_count: 5000
    last_modified: 2026-05-30
    usage_window:
      from: 2026-06-01
      to: 2026-06-30
usage_window:
  from: 2026-06-01
  to: 2026-06-30
generated:
  by: reference_agent/gemini-2.5-pro
  at: 2026-06-20T22:53:05Z
verified:
  - by: human:ahormati
    at: 2026-06-25T09:00:00Z
status: stable
stale_after: 2026-09-23
`))
	for _, check := range FamilyChecksV02 {
		if got := check(c); len(got) != 0 {
			t.Errorf("unexpected diagnostics: %+v", got)
		}
	}
}

func TestCheckSources(t *testing.T) {
	t.Run("not a list", func(t *testing.T) {
		got := CheckSources(concept(t, "type: Note\nsources: nope"))
		if len(got) != 1 || got[0].Rule != "okf/sources" || got[0].Severity != SeverityWarning {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("entry missing resource", func(t *testing.T) {
		got := CheckSources(concept(t, "type: Note\nsources:\n  - id: src"))
		if len(got) != 1 || !strings.Contains(got[0].Message, "resource") {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("scope descriptor resource is valid", func(t *testing.T) {
		got := CheckSources(concept(t, "type: Note\nsources:\n  - resource: all queries in BigQuery project X"))
		if len(got) != 0 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("bad dates and counts", func(t *testing.T) {
		got := CheckSources(concept(t, strings.TrimSpace(`
type: Note
sources:
  - resource: a
    last_modified: May 2026
    usage_count: lots
usage_window:
  from: 2026-06-01
`)))
		if len(got) != 3 {
			t.Fatalf("got %d diagnostics, want 3 (last_modified, usage_count, usage_window.to): %+v", len(got), got)
		}
	})
}

func TestCheckGenerated(t *testing.T) {
	if got := CheckGenerated(concept(t, "type: Note\ngenerated: yesterday")); len(got) != 1 {
		t.Fatalf("non-mapping generated: %+v", got)
	}
	if got := CheckGenerated(concept(t, "type: Note\ngenerated:\n  at: 2026-06-20T22:53:05Z")); len(got) != 1 || !strings.Contains(got[0].Message, "generated.by") {
		t.Fatalf("missing by: %+v", got)
	}
	if got := CheckGenerated(concept(t, "type: Note\ngenerated:\n  by: human:a\n  at: not a time")); len(got) != 1 || !strings.Contains(got[0].Message, "generated.at") {
		t.Fatalf("bad at: %+v", got)
	}
}

func TestCheckVerified(t *testing.T) {
	t.Run("bare mapping normalizes to one event", func(t *testing.T) {
		got := CheckVerified(concept(t, "type: Note\nverified:\n  by: human:a\n  at: 2026-06-25T09:00:00Z"))
		if len(got) != 0 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("event missing by and at", func(t *testing.T) {
		got := CheckVerified(concept(t, "type: Note\nverified:\n  - note: hi"))
		if len(got) != 2 {
			t.Fatalf("got %d diagnostics, want 2: %+v", len(got), got)
		}
	})
	t.Run("scalar rejected", func(t *testing.T) {
		if got := CheckVerified(concept(t, "type: Note\nverified: true")); len(got) != 1 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
}

func TestCheckStatusAndStaleAfter(t *testing.T) {
	if got := CheckStatus(concept(t, "type: Note\nstatus: archived")); len(got) != 1 || got[0].Rule != "okf/status" {
		t.Fatalf("diagnostics = %+v", got)
	}
	if got := CheckStatus(concept(t, "type: Note\nstatus: deprecated")); len(got) != 0 {
		t.Fatalf("diagnostics = %+v", got)
	}
	if got := CheckStaleAfter(concept(t, "type: Note\nstale_after: in 30 days")); len(got) != 1 || got[0].Rule != "okf/stale-after" {
		t.Fatalf("diagnostics = %+v", got)
	}
}

func TestCheckAttestedComputation(t *testing.T) {
	t.Run("other types are ignored", func(t *testing.T) {
		if got := CheckAttestedComputation(concept(t, "type: Metric")); len(got) != 0 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("runtime required", func(t *testing.T) {
		got := CheckAttestedComputation(concept(t, "type: Attested Computation"))
		if len(got) != 1 || !strings.Contains(got[0].Message, "runtime") {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("full contract passes", func(t *testing.T) {
		got := CheckAttestedComputation(concept(t, strings.TrimSpace(`
type: Attested Computation
runtime: bigquery
parameters:
  - name: year
    type: integer
    required: true
computation: references/computations/revenue.sql
executor:
  resource: references/skills/run-on-bq.md
  receipt:
    - job_id
    - result
attester:
  resource: references/attesters/revenue.py
`)))
		if len(got) != 0 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("malformed contract fields", func(t *testing.T) {
		got := CheckAttestedComputation(concept(t, strings.TrimSpace(`
type: Attested Computation
runtime: bigquery
parameters:
  - name: year
executor:
  receipt: job_id
attester: revenue.py
`)))
		want := []string{"parameters[0].type", "parameters[0].required", "executor.resource", "executor.receipt", "'attester'"}
		if len(got) != len(want) {
			t.Fatalf("got %d diagnostics, want %d: %+v", len(got), len(want), got)
		}
		for i, substr := range want {
			if !strings.Contains(got[i].Message, substr) {
				t.Errorf("diagnostic %d = %q, want it to mention %q", i, got[i].Message, substr)
			}
		}
	})
}

func TestCheckTimestamp(t *testing.T) {
	if got := CheckTimestamp(concept(t, "type: Note\ntimestamp: 2026-06-20T22:53:05Z")); len(got) != 0 {
		t.Fatalf("diagnostics = %+v", got)
	}
	if got := CheckTimestamp(concept(t, "type: Note\ntimestamp: last tuesday")); len(got) != 1 || got[0].Rule != "okf/timestamp" {
		t.Fatalf("diagnostics = %+v", got)
	}
}

func TestValidateBundle_VersionDispatch(t *testing.T) {
	badSources := File{Path: "doc.md", Content: []byte("---\ntype: Note\nsources: nope\n---\nbody\n")}

	t.Run("versionless bundle lints against latest", func(t *testing.T) {
		got := ValidateBundle([]File{badSources})
		if len(got) != 1 || got[0].Rule != "okf/sources" || got[0].Severity != SeverityWarning {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("v0.1 bundle skips v0.2 family checks", func(t *testing.T) {
		index := File{Path: "index.md", Content: []byte("---\nokf_version: \"0.1\"\n---\n# Contents\n")}
		if got := ValidateBundle([]File{index, badSources}); len(got) != 0 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("unknown version warns and lints against latest", func(t *testing.T) {
		index := File{Path: "index.md", Content: []byte("---\nokf_version: \"0.9\"\n---\n# Contents\n")}
		got := ValidateBundle([]File{index, badSources})
		if len(got) != 2 {
			t.Fatalf("got %d diagnostics, want 2: %+v", len(got), got)
		}
		for _, want := range []string{"okf/version", "okf/sources"} {
			found := false
			for _, d := range got {
				if d.Rule == want && d.Severity == SeverityWarning {
					found = true
				}
			}
			if !found {
				t.Errorf("missing %s warning in %+v", want, got)
			}
		}
	})
	t.Run("hard errors suppress family checks for that file", func(t *testing.T) {
		noType := File{Path: "doc.md", Content: []byte("---\nsources: nope\n---\nbody\n")}
		got := ValidateBundle([]File{noType})
		if len(got) != 1 || got[0].Rule != "okf/concept" {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
	t.Run("pinned version bypasses detection", func(t *testing.T) {
		if got := ValidateBundleAs(V01, []File{badSources}); len(got) != 0 {
			t.Fatalf("diagnostics = %+v", got)
		}
	})
}

func TestVersionDetection(t *testing.T) {
	rootIndex := func(fm string) File {
		return File{Path: "index.md", Content: []byte("---\n" + fm + "\n---\n# Contents\n")}
	}
	t.Run("quoted declaration", func(t *testing.T) {
		if v := DetectVersion([]File{rootIndex(`okf_version: "0.1"`)}); v != V01 {
			t.Fatalf("DetectVersion = %q, want %q", v, V01)
		}
	})
	t.Run("unquoted declaration decodes as a float", func(t *testing.T) {
		declared, ok := DeclaredVersion([]File{rootIndex(`okf_version: 0.2`)})
		if !ok || declared != "0.2" {
			t.Fatalf("DeclaredVersion = %q, %v", declared, ok)
		}
	})
	t.Run("no root index means latest", func(t *testing.T) {
		files := []File{{Path: "nested/index.md", Content: []byte("# Nested\n")}}
		if v := DetectVersion(files); v != Latest {
			t.Fatalf("DetectVersion = %q, want %q", v, Latest)
		}
	})
	t.Run("unknown resolves to latest", func(t *testing.T) {
		v, known := ResolveVersion("3.0")
		if known || v != Latest {
			t.Fatalf("ResolveVersion = %q, known=%v", v, known)
		}
	})
	if got := rules(nil); got != nil {
		t.Fatalf("rules(nil) = %v", got)
	}
}
