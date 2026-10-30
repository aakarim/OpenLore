package meta_test

import (
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// mapFS is a tiny in-memory vfs.FileSystem for tests: keys are absolute POSIX
// paths, values are file contents. Directories are synthesized from the key set.
type mapFS map[string]string

func (m mapFS) Stat(p string) (*vfs.FileInfo, error) {
	p = vfs.CleanPath(p)
	if content, ok := m[p]; ok {
		return &vfs.FileInfo{FileName: path.Base(p), FilePath: p, FileSize: int64(len(content)), FileModTime: time.Time{}}, nil
	}
	// treat as directory if any key is under it
	prefix := p
	if prefix != "/" {
		prefix += "/"
	}
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return &vfs.FileInfo{FileName: path.Base(p), FilePath: p, Dir: true}, nil
		}
	}
	return nil, vfs.ErrNotFound(p)
}

func (m mapFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	p = vfs.CleanPath(p)
	prefix := p
	if prefix != "/" {
		prefix += "/"
	}
	seen := map[string]bool{}
	var out []vfs.FileInfo
	for k, content := range m {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		name, _, isDir := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		fi := vfs.FileInfo{FileName: name, FilePath: path.Join(p, name)}
		if isDir {
			fi.Dir = true
		} else {
			fi.FileSize = int64(len(content))
		}
		out = append(out, fi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	return out, nil
}

func (m mapFS) ReadFile(p string) ([]byte, error) {
	p = vfs.CleanPath(p)
	if content, ok := m[p]; ok {
		return []byte(content), nil
	}
	return nil, vfs.ErrNotFound(p)
}

func metaTreeFS() mapFS {
	return mapFS{
		"/docs/orders.md":     "---\ntype: Table\ntitle: Orders\ntags:\n  - sales\n  - orders\n---\n# Orders\nbody\n",
		"/docs/metric.md":     "---\ntype: Metric\ntitle: Revenue\n---\nbody\n",
		"/docs/plain.md":      "# No frontmatter here\njust text\n",
		"/docs/notes.txt":     "---\ntype: NotMarkdown\n---\n",
		"/docs/sub/nested.md": "---\ntype: Note\n---\nnested body\n",
	}
}

func TestScan_EmitsFrontmatterSkipsRest(t *testing.T) {
	recs, err := meta.Scan(metaTreeFS(), "/docs")
	if err != nil {
		t.Fatal(err)
	}
	// orders.md, metric.md, sub/nested.md qualify; plain.md and .txt do not.
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d: %+v", len(recs), recs)
	}
	// Sorted by path.
	if recs[0].Path != "metric.md" || recs[1].Path != "orders.md" || recs[2].Path != "sub/nested.md" {
		t.Fatalf("paths not sorted/relative: %q %q %q", recs[0].Path, recs[1].Path, recs[2].Path)
	}
	// Full frontmatter passes through.
	if recs[1].Fields["type"] != "Table" || recs[1].Fields["title"] != "Orders" {
		t.Fatalf("orders.md fields wrong: %v", recs[1].Fields)
	}
	tags, ok := recs[1].Fields["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "sales" {
		t.Fatalf("orders.md tags wrong: %v", recs[1].Fields["tags"])
	}
}

func TestScan_Scoped(t *testing.T) {
	recs, err := meta.Scan(metaTreeFS(), "/docs/sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Path != "nested.md" {
		t.Fatalf("scoped scan wrong: %+v", recs)
	}
}

func TestScan_AppliesExtenders(t *testing.T) {
	ext := func(absPath string, content []byte, fm map[string]any) map[string]any {
		if fm["type"] == "Metric" {
			return map[string]any{"annotated": true}
		}
		return nil
	}
	recs, err := meta.Scan(metaTreeFS(), "/docs", ext)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Path == "metric.md" {
			if r.Fields["annotated"] != true {
				t.Fatalf("extender not applied to metric.md: %v", r.Fields)
			}
		} else if _, ok := r.Fields["annotated"]; ok {
			t.Fatalf("extender leaked onto %s", r.Path)
		}
	}
}

func TestScanWithReport_DiscoveryAndRawKeys(t *testing.T) {
	fsys := metaTreeFS()
	fsys["/docs/broken.md"] = "---\ntags: [broken\n---\nbody"
	fsys["/docs/empty.md"] = "---\n---\nbody"
	fsys["/docs/null.md"] = "---\ntype: null\ntags: []\n---\nbody"
	fsys["/docs/unclosed.md"] = "---\ntype: Note\n"
	calls := 0
	report, err := meta.ScanWithReport(fsys, "/docs", []string{"type", "tags", "tags"},
		func(_ string, _ []byte, fm map[string]any) map[string]any {
			calls++
			// Synthesized fields must not hide missing frontmatter keys.
			return map[string]any{"tags": []string{"generated"}}
		})
	if err != nil {
		t.Fatal(err)
	}
	if report.WithFrontmatter != 5 || report.WithoutFrontmatter != 3 || calls != 5 {
		t.Fatalf("counts/extender calls: %+v, calls=%d", report, calls)
	}
	wantPaths := []string{"broken.md", "empty.md", "metric.md", "plain.md", "sub/nested.md", "unclosed.md"}
	if len(report.Missing) != len(wantPaths) {
		t.Fatalf("missing = %+v", report.Missing)
	}
	for i, want := range wantPaths {
		if report.Missing[i].Path != want {
			t.Errorf("missing[%d].Path = %q, want %q", i, report.Missing[i].Path, want)
		}
	}
	if report.Missing[0].Reason != "invalid_frontmatter" || report.Missing[3].Reason != "no_frontmatter" {
		t.Fatalf("frontmatter diagnostics = %+v", report.Missing)
	}
	if got := report.Missing[1]; got.Reason != "missing_keys" || strings.Join(got.MissingKeys, ",") != "type,tags" {
		t.Fatalf("empty frontmatter diagnostic = %+v", got)
	}
	if got := report.Missing[2]; strings.Join(got.MissingKeys, ",") != "tags" {
		t.Fatalf("raw frontmatter keys must not include extender fields: %+v", got)
	}
}

func TestScanWithReport_NoKeysAndExactFile(t *testing.T) {
	for _, root := range []string{"/docs", "/docs/plain.md"} {
		report, err := meta.ScanWithReport(metaTreeFS(), root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Missing) != 1 || report.Missing[0].Path != "plain.md" || report.Missing[0].Reason != "no_frontmatter" {
			t.Fatalf("root %s: missing = %+v", root, report.Missing)
		}
	}
	report, err := meta.ScanWithReport(metaTreeFS(), "/docs/sub", []string{"tags"})
	if err != nil || len(report.Missing) != 1 || report.Missing[0].Path != "nested.md" {
		t.Fatalf("scoped missing report = %+v, err=%v", report, err)
	}
}
