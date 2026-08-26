package cmds_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// mapFS is an in-memory filesystem for testing. It implements vfs.WritableFS so
// mutating commands (mkdir, rm, redirects) can be exercised directly.
type mapFS struct {
	Files    map[string]*vfs.FileInfo
	Dirs     map[string][]string
	readonly bool
}

var _ vfs.WritableFS = (*mapFS)(nil)

func newMapFS() *mapFS {
	return &mapFS{
		Files: make(map[string]*vfs.FileInfo),
		Dirs:  make(map[string][]string),
	}
}

func (m *mapFS) AddFile(path string, content string) {
	fi := &vfs.FileInfo{
		FileName:    baseName(path),
		FilePath:    path,
		Content:     []byte(content),
		Dir:         false,
		FileModTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		FileSize:    int64(len(content)),
	}
	m.Files[path] = fi
	dir := dirName(path)
	for dir != "" && dir != "/" {
		if _, ok := m.Files[dir]; !ok {
			m.Files[dir] = &vfs.FileInfo{
				FileName: baseName(dir), FilePath: dir, Dir: true,
				FileModTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			}
		}
		parent := dirName(dir)
		m.addChild(parent, baseName(dir))
		dir = parent
	}
	if _, ok := m.Files["/"]; !ok {
		m.Files["/"] = &vfs.FileInfo{FileName: "/", FilePath: "/", Dir: true, FileModTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	parent := dirName(path)
	m.addChild(parent, baseName(path))
}

func (m *mapFS) addChild(dir, child string) {
	for _, c := range m.Dirs[dir] {
		if c == child {
			return
		}
	}
	m.Dirs[dir] = append(m.Dirs[dir], child)
}

func (m *mapFS) AddDir(path string) {
	m.Files[path] = &vfs.FileInfo{
		FileName: baseName(path), FilePath: path, Dir: true,
		FileModTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
	}
	if path != "/" {
		parent := dirName(path)
		m.addChild(parent, baseName(path))
	}
}

func (m *mapFS) Stat(path string) (*vfs.FileInfo, error) {
	path = vfs.CleanPath(path)
	fi, ok := m.Files[path]
	if !ok {
		return nil, fmt.Errorf("not found: %s", path)
	}
	return fi, nil
}

func (m *mapFS) ReadDir(path string) ([]vfs.FileInfo, error) {
	path = vfs.CleanPath(path)
	fi, ok := m.Files[path]
	if !ok {
		return nil, fmt.Errorf("not found: %s", path)
	}
	if !fi.Dir {
		return nil, fmt.Errorf("not a directory: %s", path)
	}
	children := m.Dirs[path]
	var result []vfs.FileInfo
	for _, name := range children {
		childPath := path + "/" + name
		if path == "/" {
			childPath = "/" + name
		}
		if cfi, ok := m.Files[childPath]; ok {
			result = append(result, *cfi)
		}
	}
	return result, nil
}

func (m *mapFS) ReadFile(path string) ([]byte, error) {
	path = vfs.CleanPath(path)
	fi, ok := m.Files[path]
	if !ok {
		return nil, fmt.Errorf("not found: %s", path)
	}
	if fi.Dir {
		return nil, fmt.Errorf("is a directory: %s", path)
	}
	return fi.Content, nil
}

func (m *mapFS) SetWriteable() error { m.readonly = false; return nil }
func (m *mapFS) SetReadonly() error  { m.readonly = true; return nil }

func (m *mapFS) WriteFileAtomic(p string, data []byte, opts vfs.WriteOpts) (string, error) {
	if m.readonly {
		return "", vfs.ErrReadOnly
	}
	clean := vfs.CleanPath(p)
	current, exists := m.Files[clean]
	currentHash := ""
	if exists && !current.Dir {
		sum := sha256.Sum256(current.Content)
		currentHash = hex.EncodeToString(sum[:])
	}
	if opts.IfNoneMatch && exists {
		return "", &vfs.PreconditionError{Path: clean, Current: currentHash}
	}
	if opts.IfMatch != nil && (!exists || current.Dir || currentHash != *opts.IfMatch) {
		return "", &vfs.PreconditionError{Path: clean, Current: currentHash}
	}
	m.AddFile(clean, string(data))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (m *mapFS) Mkdir(p string) error {
	if m.readonly {
		return vfs.ErrReadOnly
	}
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return fmt.Errorf("cannot create docset root")
	}
	parent := dirName(clean)
	if parent == "" {
		parent = "/"
	}
	if fi, ok := m.Files[parent]; !ok || !fi.Dir {
		return fmt.Errorf("mkdir: %s: no such file or directory", parent)
	}
	if _, ok := m.Files[clean]; ok {
		return fmt.Errorf("mkdir: %s: file exists", p)
	}
	m.AddDir(clean)
	return nil
}

func (m *mapFS) MkdirAll(p string) error {
	if m.readonly {
		return vfs.ErrReadOnly
	}
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		cur += "/" + part
		if fi, ok := m.Files[cur]; ok {
			if !fi.Dir {
				return fmt.Errorf("mkdir: %s: not a directory", cur)
			}
			continue
		}
		m.AddDir(cur)
	}
	return nil
}

func (m *mapFS) Remove(p string) error {
	if m.readonly {
		return vfs.ErrReadOnly
	}
	clean := vfs.CleanPath(p)
	fi, ok := m.Files[clean]
	if !ok {
		return fmt.Errorf("rm: %s: no such file or directory", p)
	}
	if fi.Dir && len(m.Dirs[clean]) > 0 {
		return fmt.Errorf("rm: %s: directory not empty", p)
	}
	m.removeEntry(clean)
	return nil
}

func (m *mapFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	if m.readonly {
		return vfs.ErrReadOnly
	}
	clean := vfs.CleanPath(p)
	if _, ok := m.Files[clean]; !ok {
		return fmt.Errorf("rm: %s: no such file or directory", p)
	}
	if opts.Expected != nil && !mapFSSnapshotEqual(opts.Expected, m.snapshot(clean)) {
		return &vfs.TreeStaleError{Path: clean, Detail: "subtree does not match expected snapshot"}
	}
	prefix := clean + "/"
	for tracked := range m.Files {
		if tracked == clean || strings.HasPrefix(tracked, prefix) {
			m.removeEntry(tracked)
		}
	}
	return nil
}

func (m *mapFS) snapshot(root string) *vfs.TreeSnapshot {
	root = vfs.CleanPath(root)
	snapshot := &vfs.TreeSnapshot{Root: root}
	prefix := root + "/"
	for tracked, info := range m.Files {
		if tracked != root && !strings.HasPrefix(tracked, prefix) {
			continue
		}
		rel := "."
		if tracked != root {
			rel = strings.TrimPrefix(tracked, prefix)
		}
		op := vfs.TreeOp{RelPath: rel, Kind: "dir"}
		if !info.Dir {
			sum := sha256.Sum256(info.Content)
			op.Kind = "file"
			op.Hash = hex.EncodeToString(sum[:])
			op.Size = int64(len(info.Content))
		}
		snapshot.Ops = append(snapshot.Ops, op)
	}
	return snapshot
}

func mapFSSnapshotEqual(want, got *vfs.TreeSnapshot) bool {
	if want.Root != got.Root || len(want.Ops) != len(got.Ops) {
		return false
	}
	wantByPath := make(map[string]vfs.TreeOp, len(want.Ops))
	for _, op := range want.Ops {
		wantByPath[op.RelPath] = op
	}
	for _, op := range got.Ops {
		if expected, ok := wantByPath[op.RelPath]; !ok || expected != op {
			return false
		}
	}
	return true
}

func (m *mapFS) removeEntry(clean string) {
	delete(m.Files, clean)
	delete(m.Dirs, clean)
	parent := dirName(clean)
	if parent == "" {
		parent = "/"
	}
	children := m.Dirs[parent]
	for i, c := range children {
		if c == baseName(clean) {
			m.Dirs[parent] = append(children[:i], children[i+1:]...)
			break
		}
	}
}

func baseName(path string) string {
	if path == "/" {
		return "/"
	}
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return parts[len(parts)-1]
}

func dirName(path string) string {
	if path == "/" {
		return ""
	}
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx]
}

func testFS() *mapFS {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddDir("/docs")
	fs.AddFile("/docs/readme.md", "# Hello World\nThis is a test file.\nLine 3\nLine 4\nLine 5\n")
	fs.AddFile("/docs/notes.txt", "banana\napple\ncherry\napple\ndate\nbanana\n")
	fs.AddFile("/docs/data.csv", "name,age,city\nalice,30,new york\nbob,25,boston\ncharlie,35,chicago\n")
	fs.AddFile("/docs/data.json", `{"name":"alice","age":30,"items":[1,2,3],"active":true}`)
	fs.AddFile("/docs/numbers.txt", "10\n2\n30\n1\n20\n3\n")
	fs.AddFile("/docs/script.sh", "echo hello\necho world\n")
	fs.AddFile("/docs/tabs.txt", "col1\tcol2\tcol3\nval1\tval2\tval3\n")
	fs.AddFile("/docs/sorted1.txt", "apple\nbanana\ncherry\n")
	fs.AddFile("/docs/sorted2.txt", "banana\ncherry\ndate\n")
	fs.AddDir("/docs/sub")
	fs.AddFile("/docs/sub/file.txt", "sub file content\n")
	fs.AddFile("/docs/users.json", `[{"name":"alice","age":30,"active":true},{"name":"bob","age":25,"active":false},{"name":"charlie","age":35,"active":true}]`)
	fs.AddFile("/docs/checksum.txt", "test content for hashing\n")
	return fs
}

func execCmd(t *testing.T, fs *mapFS, cmd string) (string, string, int) {
	t.Helper()
	sh := shell.NewShell(fs)
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline(cmd, &out, &errOut, nil)
	return out.String(), errOut.String(), code
}

func assertOutput(t *testing.T, fs *mapFS, cmd, expected string) {
	t.Helper()
	out, errOut, code := execCmd(t, fs, cmd)
	if code != 0 {
		t.Errorf("%s: exit code %d, stderr: %s", cmd, code, errOut)
	}
	if strings.TrimRight(out, "\n") != strings.TrimRight(expected, "\n") {
		t.Errorf("%s:\n  got:  %q\n  want: %q", cmd, strings.TrimRight(out, "\n"), strings.TrimRight(expected, "\n"))
	}
}

func assertExitCode(t *testing.T, fs *mapFS, cmd string, expectedCode int) {
	t.Helper()
	_, _, code := execCmd(t, fs, cmd)
	if code != expectedCode {
		t.Errorf("%s: exit code %d, want %d", cmd, code, expectedCode)
	}
}

func TestMapFSWriteFileAtomicHonorsPreconditions(t *testing.T) {
	fs := testFS()
	path := "/docs/notes.txt"
	before := append([]byte(nil), fs.Files[path].Content...)
	stale := "stale"

	_, err := fs.WriteFileAtomic(path, []byte("replacement"), vfs.WriteOpts{IfMatch: &stale})
	var precondition *vfs.PreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("stale IfMatch: want PreconditionError, got %v", err)
	}
	if got := fs.Files[path].Content; !bytes.Equal(got, before) {
		t.Fatalf("stale IfMatch changed content to %q", got)
	}

	_, err = fs.WriteFileAtomic(path, []byte("replacement"), vfs.WriteOpts{IfNoneMatch: true})
	if !errors.As(err, &precondition) {
		t.Fatalf("IfNoneMatch on existing file: want PreconditionError, got %v", err)
	}

	sum := sha256.Sum256(before)
	match := hex.EncodeToString(sum[:])
	if _, err := fs.WriteFileAtomic(path, []byte("replacement"), vfs.WriteOpts{IfMatch: &match}); err != nil {
		t.Fatalf("matching IfMatch: %v", err)
	}
	if _, err := fs.WriteFileAtomic("/docs/new.txt", []byte("new"), vfs.WriteOpts{IfNoneMatch: true}); err != nil {
		t.Fatalf("IfNoneMatch on missing file: %v", err)
	}
}

func TestMapFSRemoveAllHonorsExpectedSnapshot(t *testing.T) {
	fs := testFS()
	expected := fs.snapshot("/docs/sub")
	fs.AddFile("/docs/sub/concurrent.txt", "concurrent")

	err := fs.RemoveAll("/docs/sub", vfs.RemoveOpts{Expected: expected})
	var stale *vfs.TreeStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("stale snapshot: want TreeStaleError, got %v", err)
	}
	if _, ok := fs.Files["/docs/sub/concurrent.txt"]; !ok {
		t.Fatal("stale snapshot removed the tree")
	}

	if err := fs.RemoveAll("/docs/sub", vfs.RemoveOpts{Expected: fs.snapshot("/docs/sub")}); err != nil {
		t.Fatalf("matching snapshot: %v", err)
	}
}

type writeRaceFS struct {
	*mapFS
	target string
	raced  bool
}

func (f *writeRaceFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	if clean := vfs.CleanPath(name); clean == f.target && !f.raced {
		f.raced = true
		f.AddFile(clean, "concurrent")
	}
	return f.mapFS.WriteFileAtomic(name, data, opts)
}

func TestRedirectCarriesIfMatchToWritableFS(t *testing.T) {
	fs := &writeRaceFS{mapFS: testFS(), target: "/docs/notes.txt"}
	sh := shell.NewShell(fs)
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("echo replacement > /docs/notes.txt", &out, &errOut, nil)

	if code != 1 || !strings.Contains(errOut.String(), "file changed concurrently") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if got := string(fs.Files[fs.target].Content); got != "concurrent" {
		t.Fatalf("concurrent content was overwritten: %q", got)
	}
}

type removeRaceFS struct {
	*mapFS
	target string
	raced  bool
}

func (f *removeRaceFS) RemoveAll(name string, opts vfs.RemoveOpts) error {
	if clean := vfs.CleanPath(name); clean == f.target && !f.raced {
		f.raced = true
		f.AddFile(clean, "concurrent")
	}
	return f.mapFS.RemoveAll(name, opts)
}

func TestMvCarriesSourceSnapshotToWritableFS(t *testing.T) {
	fs := &removeRaceFS{mapFS: testFS(), target: "/docs/notes.txt"}
	sh := shell.NewShell(fs)
	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("mv /docs/notes.txt /docs/moved.txt", &out, &errOut, nil)

	if code != 1 || !strings.Contains(errOut.String(), "source changed concurrently") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if got := string(fs.Files[fs.target].Content); got != "concurrent" {
		t.Fatalf("concurrently changed source was removed or overwritten: %q", got)
	}
}
