package okf

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Severity is the impact of a validation diagnostic.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Diagnostic is one linter-style validation finding.
type Diagnostic struct {
	Path     string
	Line     int
	Column   int
	Severity Severity
	Rule     string
	Message  string
}

// File is one file in a knowledge bundle. Path is relative to the bundle root.
type File struct {
	Path    string
	Content []byte
}

// Link is a standard Markdown link found in an OKF document.
type Link struct {
	Destination string
	Line        int
	Column      int
}

// ValidateBundle checks OKF conformance for every Markdown file in a bundle,
// linting against the spec version the bundle declares in its root index.md
// (Latest when absent; an unknown declaration is a warning, never a
// rejection, per §12). Hard conformance violations (§11) are errors; shape
// problems in the version's optional field families are warnings. It
// intentionally does not reject broken links: OKF §6.1 requires consumers to
// tolerate them. OpenLore checks link resolvability separately as an
// operational requirement.
func ValidateBundle(files []File) []Diagnostic {
	version := Latest
	var diagnostics []Diagnostic
	if declared, ok := DeclaredVersion(files); ok {
		var known bool
		version, known = ResolveVersion(declared)
		if !known {
			diagnostics = append(diagnostics, warnDiagnostic(IndexFile, 1, "okf/version",
				fmt.Sprintf("unknown okf_version %q; validating against OKF v%s", declared, version)))
		}
	}
	diagnostics = append(diagnostics, ValidateBundleAs(version, files)...)
	sortDiagnostics(diagnostics)
	return diagnostics
}

// ValidateBundleAs is ValidateBundle pinned to a specific spec version,
// bypassing detection. The hard conformance core is identical across
// versions; the version selects which optional-family checks (FamilyChecks)
// run as warnings.
func ValidateBundleAs(version Version, files []File) []Diagnostic {
	checks := FamilyChecks(version)
	var diagnostics []Diagnostic
	for _, file := range files {
		name := strings.TrimPrefix(path.Clean("/"+file.Path), "/")
		if path.Ext(name) != ".md" {
			continue
		}

		if !IsReserved(name) {
			if err := Validate(name, file.Content); err != nil {
				diagnostics = append(diagnostics, diagnostic(name, 1, "okf/concept", err.Error()))
				continue
			}
			meta, body, _, _ := ParseFrontmatter(file.Content)
			concept := Concept{Path: name, Meta: meta, Body: body}
			for _, check := range checks {
				diagnostics = append(diagnostics, check(concept)...)
			}
			continue
		}

		switch path.Base(name) {
		case IndexFile:
			diagnostics = append(diagnostics, validateIndex(name, file.Content)...)
		case LogFile:
			diagnostics = append(diagnostics, validateLog(name, file.Content)...)
		}
	}
	sortDiagnostics(diagnostics)
	return diagnostics
}

func validateIndex(name string, content []byte) []Diagnostic {
	if !utf8Valid(content) {
		return []Diagnostic{diagnostic(name, 1, "okf/utf8", "file is not valid UTF-8")}
	}
	meta, body, hasFM, err := ParseFrontmatter(content)
	if err != nil {
		return []Diagnostic{diagnostic(name, 1, "okf/frontmatter", err.Error())}
	}
	if !hasFM && hasOpeningDelimiter(content) {
		return []Diagnostic{diagnostic(name, 1, "okf/frontmatter", "YAML frontmatter block has no closing '---' delimiter")}
	}
	var diagnostics []Diagnostic
	if hasFM {
		if name != IndexFile {
			diagnostics = append(diagnostics, diagnostic(name, 1, "okf/index-frontmatter", "frontmatter is permitted only in the bundle-root index.md"))
		} else {
			version, ok := meta["okf_version"].(string)
			if !ok || strings.TrimSpace(version) == "" {
				diagnostics = append(diagnostics, diagnostic(name, 1, "okf/index-frontmatter", "bundle-root index.md frontmatter must declare a non-empty 'okf_version'"))
			}
		}
	} else {
		body = content
	}
	if len(markdownHeadings(body)) == 0 {
		diagnostics = append(diagnostics, diagnostic(name, firstContentLine(content), "okf/index-structure", "index.md must contain at least one Markdown section heading"))
	}
	return diagnostics
}

func validateLog(name string, content []byte) []Diagnostic {
	if !utf8Valid(content) {
		return []Diagnostic{diagnostic(name, 1, "okf/utf8", "file is not valid UTF-8")}
	}
	_, body, hasFM, err := ParseFrontmatter(content)
	if err != nil {
		return []Diagnostic{diagnostic(name, 1, "okf/frontmatter", err.Error())}
	}
	if !hasFM && hasOpeningDelimiter(content) {
		return []Diagnostic{diagnostic(name, 1, "okf/frontmatter", "YAML frontmatter block has no closing '---' delimiter")}
	}

	var diagnostics []Diagnostic
	if hasFM {
		diagnostics = append(diagnostics, diagnostic(name, 1, "okf/log-frontmatter", "log.md must not contain YAML frontmatter"))
	} else {
		body = content
	}
	var previous time.Time
	groupHeadings := 0
	for _, heading := range markdownHeadings(body) {
		if heading.Level != 2 {
			continue
		}
		groupHeadings++
		date := strings.TrimSpace(heading.Text)
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Format("2006-01-02") != date {
			diagnostics = append(diagnostics, diagnostic(name, heading.Line, "okf/log-date", "log.md date headings must use ISO 8601 YYYY-MM-DD form"))
			continue
		}
		if !previous.IsZero() && parsed.After(previous) {
			diagnostics = append(diagnostics, diagnostic(name, heading.Line, "okf/log-order", "log.md date groups must be newest first"))
		}
		previous = parsed
	}
	if groupHeadings == 0 {
		diagnostics = append(diagnostics, diagnostic(name, firstContentLine(content), "okf/log-structure", "log.md must contain at least one ISO 8601 date group"))
	}
	return diagnostics
}

// Links returns standard Markdown links from a document body. Frontmatter,
// images, code spans, and code blocks are excluded by the CommonMark parser.
// Destinations with URL schemes are included so callers can decide which
// schemes they know how to check.
func Links(content []byte) []Link {
	_, body, hasFM, err := ParseFrontmatter(content)
	lineOffset := 0
	if err != nil || !hasFM {
		body = content
	} else {
		lineOffset = bytes.Count(content[:len(content)-len(body)], []byte("\n"))
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader(body))
	var links []Link
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link, ok := node.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		offset := nodeOffset(link)
		line, column := lineColumn(body, offset)
		links = append(links, Link{Destination: string(link.Destination), Line: line + lineOffset, Column: column})
		return ast.WalkContinue, nil
	})
	return links
}

// LocalLinkPath returns the path component of a link that should resolve
// inside a bundle. External URLs, anchors, and empty destinations return false.
func LocalLinkPath(destination string) (string, bool) {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.HasPrefix(destination, "#") {
		return "", false
	}
	u, err := url.Parse(destination)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(destination, "//") {
		return "", false
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil || p == "" {
		return "", false
	}
	return p, true
}

func diagnostic(name string, line int, rule, message string) Diagnostic {
	return Diagnostic{Path: name, Line: line, Column: 1, Severity: SeverityError, Rule: rule, Message: message}
}

func warnDiagnostic(name string, line int, rule, message string) Diagnostic {
	return Diagnostic{Path: name, Line: line, Column: 1, Severity: SeverityWarning, Rule: rule, Message: message}
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		if diagnostics[i].Line != diagnostics[j].Line {
			return diagnostics[i].Line < diagnostics[j].Line
		}
		return diagnostics[i].Column < diagnostics[j].Column
	})
}

func hasOpeningDelimiter(content []byte) bool {
	_, ok := trimOpeningDelim(content)
	return ok
}

func utf8Valid(content []byte) bool {
	return utf8.Valid(content)
}

type markdownHeading struct {
	Level int
	Text  string
	Line  int
}

func markdownHeadings(content []byte) []markdownHeading {
	doc := goldmark.DefaultParser().Parse(text.NewReader(content))
	var headings []markdownHeading
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := node.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		offset := nodeOffset(heading)
		line, _ := lineColumn(content, offset)
		headings = append(headings, markdownHeading{Level: heading.Level, Text: string(heading.Text(content)), Line: line})
		return ast.WalkContinue, nil
	})
	return headings
}

func nodeOffset(node ast.Node) int {
	if node.Type() == ast.TypeBlock {
		if lines := node.Lines(); lines != nil && lines.Len() > 0 {
			return lines.At(0).Start
		}
	}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if textNode, ok := child.(*ast.Text); ok {
			return textNode.Segment.Start
		}
	}
	return 0
}

func lineColumn(source []byte, offset int) (line, column int) {
	if offset < 0 || offset > len(source) {
		offset = 0
	}
	line = bytes.Count(source[:offset], []byte("\n")) + 1
	lastNewline := bytes.LastIndexByte(source[:offset], '\n')
	return line, offset - lastNewline
}

func firstContentLine(content []byte) int {
	_, body, ok := SplitFrontmatter(content)
	if !ok {
		return 1
	}
	return bytes.Count(content[:len(content)-len(body)], []byte("\n")) + 1
}

// FormatDiagnostic renders a diagnostic in a grep-friendly compiler format.
func FormatDiagnostic(d Diagnostic) string {
	return fmt.Sprintf("%s:%d:%d: %s [%s] %s", d.Path, d.Line, d.Column, d.Severity, d.Rule, d.Message)
}
