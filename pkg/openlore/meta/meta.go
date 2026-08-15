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

// Scan walks *.md documents under root in fsys and returns one Record per
// document that opens with a parseable YAML frontmatter block, sorted by path.
// It is a generic reader, not a validator: documents without (or with an
// unparseable) frontmatter block are skipped, and the full frontmatter map is
// passed through. Each record's Fields are the frontmatter merged with the
// output of every extender (applied in order). Paths are relative to root so
// they feed straight back into cat/grep from that directory.
func Scan(fsys vfs.FileSystem, root string, extenders ...Extender) ([]Record, error) {
	root = vfs.CleanPath(root)
	var records []Record
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
			return nil // no (or unparseable) frontmatter block: not a meta doc
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
		records = append(records, Record{Path: relPath(root, p), Fields: fields})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, nil
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
