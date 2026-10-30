// Package meta holds the business logic behind `lore meta`: walking a document
// tree, extracting each document's YAML frontmatter, and letting plugins enrich
// the result. It lives under the openlore namespace but is a separate package
// from pkg/openlore (which imports cmds): keeping meta cmds-free lets the shell
// command (pkg/shell/cmds) call it without an import cycle, while the parent
// openlore host reuses it too. It depends only on pkg/vfs and pkg/okf, so the
// same scanning logic backs `lore meta` on every path (as pkg/okf backs
// write-side validation).
package meta

import (
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Extender augments a scanned document's fields. It receives the document's
// absolute display path (so it can resolve which docset owns it), its raw bytes,
// and its already-parsed frontmatter, and returns extra fields to merge into the
// record. Returning nil adds nothing. The okf plugin registers one to annotate
// documents with OKF conformance where OKF applies, so read-side discovery
// agrees with write-side enforcement.
type Extender func(absPath string, content []byte, frontmatter map[string]any) map[string]any

// Filter is a plugin-provided, session-bound metadata query.
type Filter struct {
	Name          string
	Aliases       []string
	Roots         []string
	AbsolutePaths bool
	Selector      func(absPath string, record Record) bool
}

// Record is one scanned document: its path relative to the scan root and the
// merged field map (frontmatter plus any extender-contributed fields). Path is
// kept separate so callers decide how to surface it.
type Record struct {
	Path   string
	Fields map[string]any
}

// Missing describes a document omitted from metadata discovery, or lacking
// one or more requested top-level frontmatter keys. Keys contributed by
// extenders do not count as frontmatter. A present key with a null value counts
// as present; this is discovery, not schema validation.
type Missing struct {
	Path        string   `json:"path"`
	Reason      string   `json:"reason"`
	MissingKeys []string `json:"missing_keys,omitempty"`
}

// Report includes both ordinary metadata records and discovery diagnostics.
// WithoutFrontmatter includes absent and unparseable frontmatter. Unreadable
// files are skipped, as in Scan, and are not included in either count.
type Report struct {
	Records            []Record
	Missing            []Missing
	WithFrontmatter    int
	WithoutFrontmatter int
}

// Scan walks *.md documents under root in fsys and returns one Record per
// document that opens with a parseable YAML frontmatter block, sorted by path.
// It is a generic reader, not a validator: documents without (or with an
// unparseable) frontmatter block are skipped, and the full frontmatter map is
// passed through. Each record's Fields are the frontmatter merged with the
// output of every extender (applied in order). Paths are relative to root so
// they feed straight back into cat/grep from that directory.
func Scan(fsys vfs.FileSystem, root string, extenders ...Extender) ([]Record, error) {
	report, err := ScanWithReport(fsys, root, nil, extenders...)
	return report.Records, err
}

// ScanWithReport scans once and additionally reports files with absent or
// unparseable frontmatter, or missing any of keys. An empty keys list selects
// only files without parseable frontmatter. All paths are relative to root and
// both result lists are sorted by path, just like Scan.
func ScanWithReport(fsys vfs.FileSystem, root string, keys []string, extenders ...Extender) (Report, error) {
	root = vfs.CleanPath(root)
	var report Report
	err := vfs.WalkDir(fsys, root, func(p string, info *vfs.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.Dir {
			return nil
		}
		if ok, _ := path.Match("*.md", info.FileName); !ok {
			return nil
		}
		content, err := fsys.ReadFile(p)
		if err != nil {
			return nil
		}
		fm, _, hasFM, err := okf.ParseFrontmatter(content)
		if !hasFM || err != nil {
			report.WithoutFrontmatter++
			reason := "no_frontmatter"
			if err != nil {
				reason = "invalid_frontmatter"
			}
			report.Missing = append(report.Missing, Missing{Path: relPath(root, p), Reason: reason})
			return nil
		}
		report.WithFrontmatter++
		var missingKeys []string
		seen := map[string]bool{}
		for _, key := range keys {
			if _, exists := fm[key]; !exists && !seen[key] {
				missingKeys = append(missingKeys, key)
				seen[key] = true
			}
		}
		if len(missingKeys) != 0 {
			report.Missing = append(report.Missing, Missing{Path: relPath(root, p), Reason: "missing_keys", MissingKeys: missingKeys})
		}
		fields := make(map[string]any, len(fm))
		for k, v := range fm {
			fields[k] = v
		}
		for _, ext := range extenders {
			for k, v := range ext(p, content, fm) {
				fields[k] = v
			}
		}
		report.Records = append(report.Records, Record{Path: relPath(root, p), Fields: fields})
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Records, func(i, j int) bool { return report.Records[i].Path < report.Records[j].Path })
	sort.Slice(report.Missing, func(i, j int) bool { return report.Missing[i].Path < report.Missing[j].Path })
	return report, nil
}

// relPath returns p expressed relative to root (POSIX, no leading slash), so a
// `lore meta` path can be fed straight back into cat/grep from the cwd. p is
// assumed to be at or under root (it comes from a walk rooted there).
func relPath(root, p string) string {
	root = vfs.CleanPath(root)
	p = vfs.CleanPath(p)
	if root == "/" {
		return strings.TrimPrefix(p, "/")
	}
	if p == root {
		return path.Base(p)
	}
	return strings.TrimPrefix(p, root+"/")
}
