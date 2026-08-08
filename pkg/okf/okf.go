// Package okf implements validation for the Google Open Knowledge Format
// (OKF) — a directory of markdown files with YAML frontmatter. It supports
// spec revisions 0.1 and 0.2 (see Version); a bundle's effective version is
// read from the okf_version declaration in its root index.md, defaulting to
// Latest.
//
// Spec: https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md
//
// It is a dependency-light library (stdlib + yaml) so it can be reused three
// ways: as the go-openlore OKF write-admission plugin (pkg/openlore), directly
// from downstream shell commands (e.g. knowledge-backend's `kb save`/`kb
// publish`), and as a standalone conformance checker.
//
// Validate enforces only the hard conformance rules of the spec (§11 in
// v0.2), which are identical in both supported revisions:
//
//  1. Every non-reserved .md file contains a parseable YAML frontmatter block.
//  2. Every such frontmatter block contains a non-empty `type` field.
//  3. Reserved filenames (index.md, log.md) carry no required frontmatter; if
//     present it must still be parseable (the bundle-root index.md MAY declare
//     okf_version — the one place frontmatter is permitted in an index).
//
// The version-specific optional field families (v0.2 provenance, trust,
// lifecycle, and Attested Computation shapes; v0.1 legacy timestamp) are
// shape-checked as warnings by ValidateBundle via the granular, composable
// ConceptCheck functions in families.go. Everything else in the spec (titles,
// descriptions, links, body section conventions) is soft guidance that
// consumers MUST tolerate, so it is deliberately not enforced.
package okf

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Reserved filenames per OKF §3.1. They have defined meaning at any level of
// the hierarchy and are not concept documents.
const (
	IndexFile = "index.md"
	LogFile   = "log.md"
)

// IsReserved reports whether p's basename is an OKF reserved filename
// (index.md or log.md).
func IsReserved(p string) bool {
	b := path.Base(p)
	return b == IndexFile || b == LogFile
}

// Validate checks a single OKF file's bytes for conformance. p is the file's
// path (used only to determine reserved-filename status via its basename);
// content is the exact bytes. A nil error means the file is conformant.
//
// Reserved files (index.md, log.md) are validated leniently (no required
// frontmatter). Every other file is validated as a concept document: it must
// carry a parseable YAML frontmatter block with a non-empty `type`.
func Validate(p string, content []byte) error {
	if !utf8.Valid(content) {
		return fmt.Errorf("file is not valid UTF-8")
	}
	if IsReserved(p) {
		return validateReserved(content)
	}
	return validateConcept(content)
}

// validateConcept enforces §9.1 + §9.2 for a concept document.
func validateConcept(content []byte) error {
	fm, _, ok := SplitFrontmatter(content)
	if !ok {
		return fmt.Errorf("missing YAML frontmatter block (a concept must open with a '---' delimited block)")
	}
	meta, err := parseFrontmatter(fm)
	if err != nil {
		return err
	}
	t, _ := meta["type"].(string)
	if strings.TrimSpace(t) == "" {
		return fmt.Errorf("frontmatter is missing the required non-empty 'type' field")
	}
	return nil
}

// validateReserved permits a reserved file to have no frontmatter; any
// frontmatter present must still parse.
func validateReserved(content []byte) error {
	fm, _, ok := SplitFrontmatter(content)
	if !ok {
		if _, hasOpen := trimOpeningDelim(content); hasOpen {
			return fmt.Errorf("YAML frontmatter block has no closing '---' delimiter")
		}
		return nil
	}
	_, err := parseFrontmatter(fm)
	return err
}

// ParseFrontmatter extracts and decodes the YAML frontmatter of an OKF
// document, returning the decoded key/value map and the remaining markdown
// body. ok is false (with a nil error) when the content has no frontmatter
// block at all; a malformed block returns a non-nil error.
func ParseFrontmatter(content []byte) (meta map[string]any, body []byte, ok bool, err error) {
	fm, rest, found := SplitFrontmatter(content)
	if !found {
		return nil, content, false, nil
	}
	meta, err = parseFrontmatter(fm)
	if err != nil {
		return nil, rest, true, err
	}
	return meta, rest, true, nil
}

func parseFrontmatter(fm []byte) (map[string]any, error) {
	var meta map[string]any
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return nil, fmt.Errorf("unparseable YAML frontmatter: %w", err)
	}
	return meta, nil
}

// SplitFrontmatter separates a document's YAML frontmatter from its body. A
// frontmatter block is a `---` line at the very start of the file, its content,
// and a closing `---` line. It returns the raw frontmatter bytes (between the
// delimiters), the body bytes (after the closing delimiter), and ok=true when a
// well-formed opening+closing delimiter pair is found. Both LF and CRLF line
// endings are accepted.
func SplitFrontmatter(content []byte) (frontmatter, body []byte, ok bool) {
	rest, hasOpen := trimOpeningDelim(content)
	if !hasOpen {
		return nil, nil, false
	}
	// Scan line by line for a closing "---" delimiter on its own line.
	offset := 0
	for offset < len(rest) {
		line, next := nextLine(rest, offset)
		if isDelim(line) {
			return rest[:offset], rest[next:], true
		}
		offset = next
	}
	// Opening delimiter but no closing one: not a valid frontmatter block.
	return nil, nil, false
}

// trimOpeningDelim strips a leading "---" delimiter line (LF or CRLF) and
// returns the remainder. hasOpen is false when the content does not start with
// one.
func trimOpeningDelim(content []byte) (rest []byte, hasOpen bool) {
	switch {
	case bytes.HasPrefix(content, []byte("---\n")):
		return content[len("---\n"):], true
	case bytes.HasPrefix(content, []byte("---\r\n")):
		return content[len("---\r\n"):], true
	default:
		return content, false
	}
}

// nextLine returns the bytes of the line starting at offset (without its line
// terminator) and the offset of the next line.
func nextLine(b []byte, offset int) (line []byte, next int) {
	nl := bytes.IndexByte(b[offset:], '\n')
	if nl < 0 {
		return b[offset:], len(b)
	}
	end := offset + nl
	next = end + 1
	// Drop a trailing CR for CRLF endings.
	if end > offset && b[end-1] == '\r' {
		end--
	}
	return b[offset:end], next
}

// isDelim reports whether a line (already stripped of its terminator) is a
// frontmatter delimiter: exactly "---".
func isDelim(line []byte) bool {
	return string(line) == "---"
}
