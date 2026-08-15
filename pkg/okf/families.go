package okf

import (
	"fmt"
	"strings"
	"time"
)

// TypeAttestedComputation is the exact concept type (v0.2 §10) that carries
// the computation contract fields.
const TypeAttestedComputation = "Attested Computation"

// Concept is one parsed concept document: the input for granular
// frontmatter-family checks. Meta is the decoded frontmatter and Body the
// remaining markdown.
type Concept struct {
	Path string
	Meta map[string]any
	Body []byte
}

// ConceptCheck is one granular, composable family check over a parsed concept
// document. Checks only shape-check fields that are present — a missing
// optional family is never a finding (§11) — and report warnings, never
// errors, so they cannot make a conformant bundle invalid.
type ConceptCheck func(Concept) []Diagnostic

// Per-version family check sets. These are compositions of the exported
// granular checks; consumers with different policies can assemble their own.
var (
	// FamilyChecksV01 covers the v0.1 conventions superseded in v0.2.
	FamilyChecksV01 = []ConceptCheck{CheckTimestamp}
	// FamilyChecksV02 covers the v0.2 provenance, trust, lifecycle, and
	// computation families.
	FamilyChecksV02 = []ConceptCheck{
		CheckSources,
		CheckGenerated,
		CheckVerified,
		CheckStatus,
		CheckStaleAfter,
		CheckAttestedComputation,
	}
)

// FamilyChecks returns the standard check set for a spec version.
func FamilyChecks(v Version) []ConceptCheck {
	if v == V01 {
		return FamilyChecksV01
	}
	return FamilyChecksV02
}

// CheckTimestamp shape-checks the legacy v0.1 `timestamp` field. v0.2
// superseded it with `generated`, but a v0.2 consumer MAY still read it
// (§13), so it is never reported as unknown or deprecated.
func CheckTimestamp(c Concept) []Diagnostic {
	v, ok := c.Meta["timestamp"]
	if !ok || v == nil {
		return nil
	}
	if !isDateTime(v) {
		return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/timestamp", "'timestamp' must be an ISO 8601 datetime")}
	}
	return nil
}

// CheckSources shape-checks the v0.2 provenance family (§5.1): the `sources`
// list and the top-level `usage_window` that frames its usage counts.
func CheckSources(c Concept) []Diagnostic {
	var diagnostics []Diagnostic
	if w, ok := c.Meta["usage_window"]; ok && w != nil {
		diagnostics = append(diagnostics, checkUsageWindow(c.Path, "usage_window", w)...)
	}
	raw, ok := c.Meta["sources"]
	if !ok || raw == nil {
		return diagnostics
	}
	entries, ok := raw.([]any)
	if !ok {
		return append(diagnostics, warnDiagnostic(c.Path, 1, "okf/sources", "'sources' must be a YAML list of source entries"))
	}
	for i, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/sources", fmt.Sprintf("sources[%d] must be a mapping", i)))
			continue
		}
		if s, _ := entry["resource"].(string); strings.TrimSpace(s) == "" {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/sources", fmt.Sprintf("sources[%d] is missing the required 'resource' field", i)))
		}
		if v, ok := entry["last_modified"]; ok && !isDate(v) {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/sources", fmt.Sprintf("sources[%d].last_modified must be a YYYY-MM-DD date", i)))
		}
		if v, ok := entry["usage_count"]; ok && !isNumber(v) {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/sources", fmt.Sprintf("sources[%d].usage_count must be a number", i)))
		}
		if w, ok := entry["usage_window"]; ok && w != nil {
			diagnostics = append(diagnostics, checkUsageWindow(c.Path, fmt.Sprintf("sources[%d].usage_window", i), w)...)
		}
	}
	return diagnostics
}

// CheckGenerated shape-checks the v0.2 trust family's `generated` block
// (§5.2): a mapping whose `by` actor is required within the block.
func CheckGenerated(c Concept) []Diagnostic {
	raw, ok := c.Meta["generated"]
	if !ok || raw == nil {
		return nil
	}
	g, ok := raw.(map[string]any)
	if !ok {
		return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/generated", "'generated' must be a mapping with a 'by' actor")}
	}
	var diagnostics []Diagnostic
	if by, _ := g["by"].(string); strings.TrimSpace(by) == "" {
		diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/generated", "'generated.by' is required when 'generated' is present"))
	}
	if at, ok := g["at"]; ok && !isDateTime(at) {
		diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/generated", "'generated.at' must be an ISO 8601 datetime"))
	}
	return diagnostics
}

// CheckVerified shape-checks the v0.2 trust family's `verified` events
// (§5.2): a list of {by, at} mappings, or a bare {by, at} mapping which
// consumers MUST normalize to a one-element list (§11).
func CheckVerified(c Concept) []Diagnostic {
	raw, ok := c.Meta["verified"]
	if !ok || raw == nil {
		return nil
	}
	var events []any
	switch v := raw.(type) {
	case []any:
		events = v
	case map[string]any:
		events = []any{v}
	default:
		return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/verified", "'verified' must be a list of {by, at} events or a single {by, at} mapping")}
	}
	var diagnostics []Diagnostic
	for i, e := range events {
		event, ok := e.(map[string]any)
		if !ok {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/verified", fmt.Sprintf("verified[%d] must be a {by, at} mapping", i)))
			continue
		}
		if by, _ := event["by"].(string); strings.TrimSpace(by) == "" {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/verified", fmt.Sprintf("verified[%d].by is required", i)))
		}
		if at, ok := event["at"]; !ok || !isDateTime(at) {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/verified", fmt.Sprintf("verified[%d].at must be an ISO 8601 datetime", i)))
		}
	}
	return diagnostics
}

// CheckStatus shape-checks the v0.2 lifecycle `status` field (§5.4). Absent
// means stable and is never a finding.
func CheckStatus(c Concept) []Diagnostic {
	raw, ok := c.Meta["status"]
	if !ok || raw == nil {
		return nil
	}
	s, _ := raw.(string)
	switch s {
	case "draft", "stable", "deprecated":
		return nil
	}
	return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/status", "'status' must be one of draft, stable, deprecated")}
}

// CheckStaleAfter shape-checks the v0.2 lifecycle `stale_after` field (§5.5):
// an absolute YYYY-MM-DD date, not a relative TTL.
func CheckStaleAfter(c Concept) []Diagnostic {
	raw, ok := c.Meta["stale_after"]
	if !ok || raw == nil {
		return nil
	}
	if !isDate(raw) {
		return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/stale-after", "'stale_after' must be a YYYY-MM-DD date")}
	}
	return nil
}

// CheckAttestedComputation shape-checks the v0.2 computation contract (§10.2)
// on concepts whose type is exactly "Attested Computation".
func CheckAttestedComputation(c Concept) []Diagnostic {
	if t, _ := c.Meta["type"].(string); t != TypeAttestedComputation {
		return nil
	}
	var diagnostics []Diagnostic
	if runtime, _ := c.Meta["runtime"].(string); strings.TrimSpace(runtime) == "" {
		diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", "'runtime' is required for an Attested Computation"))
	}
	if raw, ok := c.Meta["parameters"]; ok && raw != nil {
		params, ok := raw.([]any)
		if !ok {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", "'parameters' must be a YAML list of {name, type, required} entries"))
		} else {
			for i, p := range params {
				param, ok := p.(map[string]any)
				if !ok {
					diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", fmt.Sprintf("parameters[%d] must be a {name, type, required} mapping", i)))
					continue
				}
				if name, _ := param["name"].(string); strings.TrimSpace(name) == "" {
					diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", fmt.Sprintf("parameters[%d].name is required", i)))
				}
				if typ, _ := param["type"].(string); strings.TrimSpace(typ) == "" {
					diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", fmt.Sprintf("parameters[%d].type is required", i)))
				}
				if _, ok := param["required"].(bool); !ok {
					diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", fmt.Sprintf("parameters[%d].required must be a boolean", i)))
				}
			}
		}
	}
	if raw, ok := c.Meta["computation"]; ok && raw != nil {
		if s, _ := raw.(string); strings.TrimSpace(s) == "" {
			diagnostics = append(diagnostics, warnDiagnostic(c.Path, 1, "okf/computation", "'computation' must be a path or URI"))
		}
	}
	if raw, ok := c.Meta["executor"]; ok && raw != nil {
		diagnostics = append(diagnostics, checkResourceBlock(c.Path, "executor", raw, func(block map[string]any) []Diagnostic {
			receipt, ok := block["receipt"]
			if !ok || receipt == nil {
				return nil
			}
			fields, ok := receipt.([]any)
			if !ok {
				return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/computation", "'executor.receipt' must be a list of field names")}
			}
			for i, f := range fields {
				if s, _ := f.(string); strings.TrimSpace(s) == "" {
					return []Diagnostic{warnDiagnostic(c.Path, 1, "okf/computation", fmt.Sprintf("executor.receipt[%d] must be a field name", i))}
				}
			}
			return nil
		})...)
	}
	if raw, ok := c.Meta["attester"]; ok && raw != nil {
		diagnostics = append(diagnostics, checkResourceBlock(c.Path, "attester", raw, nil)...)
	}
	return diagnostics
}

// checkResourceBlock validates a {resource: <path-or-URI>} mapping such as
// executor or attester, running extra on the mapping when provided.
func checkResourceBlock(path, field string, raw any, extra func(map[string]any) []Diagnostic) []Diagnostic {
	block, ok := raw.(map[string]any)
	if !ok {
		return []Diagnostic{warnDiagnostic(path, 1, "okf/computation", fmt.Sprintf("'%s' must be a mapping with a 'resource' field", field))}
	}
	var diagnostics []Diagnostic
	if s, _ := block["resource"].(string); strings.TrimSpace(s) == "" {
		diagnostics = append(diagnostics, warnDiagnostic(path, 1, "okf/computation", fmt.Sprintf("'%s.resource' is required", field)))
	}
	if extra != nil {
		diagnostics = append(diagnostics, extra(block)...)
	}
	return diagnostics
}

func checkUsageWindow(path, field string, raw any) []Diagnostic {
	window, ok := raw.(map[string]any)
	if !ok {
		return []Diagnostic{warnDiagnostic(path, 1, "okf/sources", fmt.Sprintf("'%s' must be a mapping with 'from' and 'to' dates", field))}
	}
	var diagnostics []Diagnostic
	for _, key := range []string{"from", "to"} {
		if v, ok := window[key]; !ok || !isDate(v) {
			diagnostics = append(diagnostics, warnDiagnostic(path, 1, "okf/sources", fmt.Sprintf("'%s.%s' must be a YYYY-MM-DD date", field, key)))
		}
	}
	return diagnostics
}

// isDate accepts a YAML-decoded time.Time or a YYYY-MM-DD string.
func isDate(v any) bool {
	switch t := v.(type) {
	case time.Time:
		return true
	case string:
		s := strings.TrimSpace(t)
		parsed, err := time.Parse("2006-01-02", s)
		return err == nil && parsed.Format("2006-01-02") == s
	}
	return false
}

// isDateTime accepts a YAML-decoded time.Time or an ISO 8601 / RFC 3339
// datetime string (a bare date also qualifies).
func isDateTime(v any) bool {
	switch t := v.(type) {
	case time.Time:
		return true
	case string:
		s := strings.TrimSpace(t)
		if _, err := time.Parse(time.RFC3339, s); err == nil {
			return true
		}
		return isDate(s)
	}
	return false
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, uint64, float64:
		return true
	}
	return false
}
