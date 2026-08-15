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
