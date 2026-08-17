package openlore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestAliasFS_ReadsListsAndWritesCanonicalStorage(t *testing.T) {
	root := t.TempDir()
	canonicalDir := filepath.Join(root, "agent", "jared")
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalDir, "note.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := NewDirFS(root, config.FilesConfig{})
	if err := dir.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	fsys := newAliasFS(dir, []pathAlias{{Alias: "/jared", Target: "/agent/jared"}})

	data, err := fsys.ReadFile("/jared/note.md")
	if err != nil || string(data) != "hello" {
		t.Fatalf("alias read = %q, %v", data, err)
	}
	entries, err := fsys.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.FileName] = true
	}
	if !seen["agent"] || !seen["jared"] {
		t.Fatalf("root listing should expose canonical parent and alias: %+v", entries)
	}
	info, err := fsys.Stat("/jared")
	if err != nil || info.FileName != "jared" || info.FilePath != "/jared" {
		t.Fatalf("alias stat = %+v, %v", info, err)
	}

	writable := fsys.(vfs.WritableFS)
	if _, err := writable.WriteFileAtomic("/jared/new.md", []byte("new"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(canonicalDir, "new.md"))
	if err != nil || string(got) != "new" {
		t.Fatalf("canonical file after alias write = %q, %v", got, err)
	}
}

func TestBuildCanonicalSessionFSOmitsAliases(t *testing.T) {
	s := grantTestServer()
	merge := NewMergeFS()
	merge.SetRoot(NewFSAdapter(fstest.MapFS{
		"agent/jared/note.md": {Data: []byte("hello")},
	}))
	s.merge = merge
	ds := s.auth.Docsets["alfie"]
	ds.Paths = []config.PathMapping{{Source: "/agent/jared", Display: "/agent/jared"}}
	ds.Aliases = []string{"/jared"}
	s.auth.Docsets["alfie"] = ds

	id := identityWithPolicy("jared", "alfie-rw")
	canonical := s.buildCanonicalSessionFS(id)
	if _, err := canonical.ReadFile("/agent/jared/note.md"); err != nil {
		t.Fatalf("canonical path should remain visible: %v", err)
	}
	if _, err := canonical.Stat("/jared"); err == nil {
		t.Fatal("canonical session filesystem exposed alias")
	}

	aliased := s.buildSessionFS(id)
	if got, err := aliased.ReadFile("/jared/note.md"); err != nil || string(got) != "hello" {
		t.Fatalf("filesystem transports lost alias: %q, %v", got, err)
	}
}

func TestAliasFS_SynthesizesNestedAliasAncestors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "channel", "general"), 0o755); err != nil {
		t.Fatal(err)
	}
	fsys := newAliasFS(NewDirFS(root, config.FilesConfig{}), []pathAlias{
		{Alias: "/legacy/shared/knowledge", Target: "/channel/general"},
	})

	for _, p := range []string{"/legacy", "/legacy/shared", "/legacy/shared/knowledge"} {
		info, err := fsys.Stat(p)
		if err != nil || !info.Dir {
			t.Fatalf("Stat(%q) = %+v, %v", p, info, err)
		}
	}
	entries, err := fsys.ReadDir("/legacy")
	if err != nil || len(entries) != 1 || entries[0].FileName != "shared" {
		t.Fatalf("ReadDir(/legacy) = %+v, %v", entries, err)
	}
}

func TestServerAliasCanonicalizesAuthorizationAndChangesets(t *testing.T) {
	s := grantTestServer()
	ds := s.auth.Docsets["alfie"]
	ds.Paths = []config.PathMapping{{Source: "/user/alfie", Display: "/user/alfie"}}
	ds.Aliases = []string{"/alfie"}
	s.auth.Docsets["alfie"] = ds

	id := identityWithPolicy("alfie", "alfie-rw")
	setCurrentPolicy(s, id)
	if !s.identityCanWrite(id, vfs.ChangeActionWrite, "/alfie/note.md") {
		t.Fatal("alias path should authorize through its canonical docset")
	}
	if got := s.canonicalPath("/alfie/note.md"); got != "/user/alfie/note.md" {
		t.Fatalf("canonicalPath = %q", got)
	}
}

func TestAliasFS_CanonicalizesMiddlewarePaths(t *testing.T) {
	root := t.TempDir()
	canonicalDir := filepath.Join(root, "channel", "general")
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalDir, "note.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := NewDirFS(root, config.FilesConfig{})

	var readPath string
	readView := newReadChainFS(base, Attribution{}, func(_ context.Context, op ReadOp) error {
		readPath = op.Path
		return nil
	})
	readAlias := newAliasFS(readView, []pathAlias{{Alias: "/knowledge", Target: "/channel/general"}})
	if _, err := readAlias.ReadFile("/knowledge/note.md"); err != nil {
		t.Fatal(err)
	}
	if readPath != "/channel/general/note.md" {
		t.Fatalf("read middleware path = %q", readPath)
	}

	var changePath string
	writeView := newMiddlewareFS(base, Attribution{}, func(_ context.Context, op WriteOp) (WriteResult, error) {
		changePath = op.Leaves()[0].Target
		return WriteResult{}, nil
	})
	writeAlias := newAliasFS(writeView, []pathAlias{{Alias: "/knowledge", Target: "/channel/general"}})
	if _, err := writeAlias.(vfs.WritableFS).WriteFileAtomic("/knowledge/note.md", []byte("updated"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if changePath != "/channel/general/note.md" {
		t.Fatalf("changeset target = %q", changePath)
	}
}

func TestAliasFS_MoveAliasOntoCanonicalPathIsNoOp(t *testing.T) {
	root := t.TempDir()
	canonicalDir := filepath.Join(root, "agent", "jared")
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(canonicalDir, "note.md")
	if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := NewDirFS(root, config.FilesConfig{}).WithDocsetRoots([]string{"/agent/jared"})
	if err := dir.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	sh := shell.NewShell(newAliasFS(dir, []pathAlias{{Alias: "/jared", Target: "/agent/jared"}}))
	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline("mv /jared/note.md /agent/jared/note.md", &out, &errOut, nil); code != 0 {
		t.Fatalf("mv exit = %d, stderr = %q", code, errOut.String())
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep" {
		t.Fatalf("same-file move removed content: %q, %v", data, err)
	}
}

type failingReadFS struct{ err error }

func (f failingReadFS) Stat(string) (*vfs.FileInfo, error)     { return nil, f.err }
func (f failingReadFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, f.err }
func (f failingReadFS) ReadFile(string) ([]byte, error)        { return nil, f.err }

func TestAliasFS_DoesNotHideReadErrorsWithSyntheticAncestors(t *testing.T) {
	want := errors.New("read middleware denied")
	fsys := newAliasFS(failingReadFS{err: want}, []pathAlias{{Alias: "/legacy/shared", Target: "/channel/general"}})
	if _, err := fsys.Stat("/legacy"); !errors.Is(err, want) {
		t.Fatalf("Stat error = %v, want %v", err, want)
	}
	if _, err := fsys.ReadDir("/legacy"); !errors.Is(err, want) {
		t.Fatalf("ReadDir error = %v, want %v", err, want)
	}
}

func TestAliasFS_RewritesNamedMountFileInfoAndPreservesFileType(t *testing.T) {
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "readme.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(docs, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "folder", "note.md"), []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}
	merge := NewMergeFS()
	merge.Mount("docs", NewDirFS(docs, config.FilesConfig{}))
	fsys := newAliasFS(merge, []pathAlias{
		{Alias: "/readme", Target: "/docs/readme.md"},
		{Alias: "/legacy", Target: "/docs/folder"},
	})

	entries, err := fsys.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]vfs.FileInfo{}
	for _, entry := range entries {
		byName[entry.FileName] = entry
	}
	if entry := byName["readme"]; entry.Dir || entry.FilePath != "/readme" {
		t.Fatalf("file alias entry = %+v", entry)
	}
	children, err := fsys.ReadDir("/legacy")
	if err != nil || len(children) != 1 || children[0].FilePath != "/legacy/note.md" {
		t.Fatalf("named-mount alias listing = %+v, %v", children, err)
	}
}

func TestAliasFS_CanonicalizesRemoveSnapshotWithoutMutatingCaller(t *testing.T) {
	snapshot := &vfs.TreeSnapshot{Root: "/knowledge/old", Ops: []vfs.TreeOp{{RelPath: ".", Kind: "dir"}}}
	var got vfs.ChangeSet
	writeView := newMiddlewareFS(failingReadFS{err: os.ErrNotExist}, Attribution{}, func(_ context.Context, op WriteOp) (WriteResult, error) {
		got = op.persistenceChangeSet()
		return WriteResult{}, nil
	})
	fsys := newAliasFS(writeView, []pathAlias{{Alias: "/knowledge", Target: "/channel/general"}})
	err := fsys.(vfs.WritableFS).RemoveAll("/knowledge/old", vfs.RemoveOpts{Expected: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "/channel/general/old" || got.RemoveAll.Opts.Expected.Root != "/channel/general/old" {
		t.Fatalf("canonical remove changeset = %+v", got)
	}
	if snapshot.Root != "/knowledge/old" {
		t.Fatalf("caller snapshot mutated to %q", snapshot.Root)
	}
	snapshot.Ops[0].Kind = "file"
	if got.RemoveAll.Opts.Expected.Ops[0].Kind != "dir" {
		t.Fatal("canonical changeset shares the caller's snapshot operations")
	}
}

func TestServerCanonicalChangeSetCopiesRemoveSnapshot(t *testing.T) {
	s := grantTestServer()
	ds := s.auth.Docsets["alfie"]
	ds.Paths = []config.PathMapping{{Source: "/user/alfie"}}
	ds.Aliases = []string{"/alfie"}
	s.auth.Docsets["alfie"] = ds
	snapshot := &vfs.TreeSnapshot{Root: "/alfie/old", Ops: []vfs.TreeOp{{RelPath: ".", Kind: "dir"}}}
	remove := &vfs.RemoveAllChange{Opts: vfs.RemoveOpts{Expected: snapshot}}

	got := s.canonicalChangeSet(vfs.ChangeSet{Target: "/alfie/old", Action: vfs.ChangeActionRemoveAll, RemoveAll: remove})
	snapshot.Ops[0].Kind = "file"
	if got.Target != "/user/alfie/old" || got.RemoveAll.Opts.Expected.Root != "/user/alfie/old" {
		t.Fatalf("canonical changeset = %+v", got)
	}
	if got.RemoveAll.Opts.Expected.Ops[0].Kind != "dir" {
		t.Fatal("canonical changeset shares the caller's snapshot operations")
	}

	batchSnapshot := &vfs.TreeSnapshot{Root: "/alfie/batch", Ops: []vfs.TreeOp{{RelPath: ".", Kind: "dir"}}}
	batch := s.canonicalChangeSet(vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/alfie/new.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new")}},
		{Target: "/alfie/batch", Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{Opts: vfs.RemoveOpts{Expected: batchSnapshot}}},
	}})
	if batch.Changes[0].Target != "/user/alfie/new.md" || batch.Changes[1].Target != "/user/alfie/batch" || batch.Changes[1].RemoveAll.Opts.Expected.Root != "/user/alfie/batch" {
		t.Fatalf("canonical batch = %+v", batch)
	}
}
