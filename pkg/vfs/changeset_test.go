package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

// fakeWritableFS captures the last mutating call and returns programmed errors,
// so CommitChangeSet's ChangeSet→(WriteFileAtomic/RemoveAll) translation can be
// asserted without a full CAS-capable substrate (that is tested elsewhere).
type fakeWritableFS struct {
	FileSystem

	wrotePath string
	wroteData []byte
	wroteOpts WriteOpts
	writeHash string
	writeErr  error

	mkdirPath    string
	mkdirAllPath string
	removePath   string

	removedPath string
	removedOpts RemoveOpts
	removeErr   error
}

type fakeXattrFS struct {
	fakeWritableFS
	path, name string
	value      []byte
	flags      XattrFlags
	removed    bool
}

func (f *fakeXattrFS) SetXattr(p, n string, v []byte, flags XattrFlags) error {
	f.path, f.name, f.value, f.flags = p, n, append([]byte(nil), v...), flags
	return nil
}
func (f *fakeXattrFS) RemoveXattr(p, n string) error {
	f.path, f.name, f.removed = p, n, true
	return nil
}

func TestCommitChangeSetXattrsAndImmutableLeaves(t *testing.T) {
	f := &fakeXattrFS{}
	value := []byte("v")
	cs := ChangeSet{Target: "/d", Action: ChangeActionSetXattr, Xattr: &XattrChange{Name: "user.lore.x", Value: value, Flags: XattrCreate}}
	leaves := cs.Leaves()
	leaves[0].Xattr.Value[0] = 'z'
	if _, err := CommitChangeSet(f, cs); err != nil {
		t.Fatal(err)
	}
	if string(f.value) != "v" || f.path != "/d" || f.flags != XattrCreate {
		t.Fatalf("dispatch: %#v", f)
	}
	if _, err := CommitChangeSet(f, ChangeSet{Target: "/d", Action: ChangeActionRemoveXattr, Xattr: &XattrChange{Name: "user.lore.x"}}); err != nil || !f.removed {
		t.Fatalf("remove: %v", err)
	}
}

func (f *fakeWritableFS) SetWriteable() error     { return nil }
func (f *fakeWritableFS) SetReadonly() error      { return nil }
func (f *fakeWritableFS) Mkdir(p string) error    { f.mkdirPath = p; return nil }
func (f *fakeWritableFS) MkdirAll(p string) error { f.mkdirAllPath = p; return nil }
func (f *fakeWritableFS) Remove(p string) error   { f.removePath = p; return nil }

func (f *fakeWritableFS) WriteFileAtomic(name string, data []byte, opts WriteOpts) (string, error) {
	f.wrotePath, f.wroteData, f.wroteOpts = name, data, opts
	return f.writeHash, f.writeErr
}

func (f *fakeWritableFS) RemoveAll(name string, opts RemoveOpts) error {
	f.removedPath, f.removedOpts = name, opts
	return f.removeErr
}

func TestCommitChangeSet_WriteCarriesOptsVerbatim(t *testing.T) {
	base := "base123"
	fs := &fakeWritableFS{writeHash: "newhash"}
	cs := ChangeSet{
		Target: "/wiki/a.md",
		Action: ChangeActionWrite,
		Write:  &WriteChange{Bytes: []byte("hi"), Opts: WriteOpts{IfMatch: &base}},
	}
	got, err := CommitChangeSet(fs, cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Hash != "newhash" {
		t.Fatalf("newHash = %q, want newhash", got.Hash)
	}
	if fs.wrotePath != "/wiki/a.md" || string(fs.wroteData) != "hi" {
		t.Fatalf("wrote (%q,%q)", fs.wrotePath, fs.wroteData)
	}
	if fs.wroteOpts.IfMatch == nil || *fs.wroteOpts.IfMatch != "base123" {
		t.Fatalf("want IfMatch=base123, got %+v", fs.wroteOpts)
	}
	if fs.wroteOpts.IfNoneMatch {
		t.Fatalf("IfNoneMatch must be false")
	}
}

type rollbackFS struct {
	nodes      map[string]*FileInfo
	failTarget string
	failed     bool
}

func newRollbackFS() *rollbackFS {
	return &rollbackFS{nodes: map[string]*FileInfo{"/": {FileName: "/", FilePath: "/", Dir: true}}}
}
func (f *rollbackFS) SetWriteable() error { return nil }
func (f *rollbackFS) SetReadonly() error  { return nil }
func (f *rollbackFS) Stat(target string) (*FileInfo, error) {
	target = CleanPath(target)
	info, ok := f.nodes[target]
	if !ok {
		return nil, fs.ErrNotExist
	}
	copy := *info
	copy.Content = append([]byte(nil), info.Content...)
	return &copy, nil
}
func (f *rollbackFS) ReadFile(target string) ([]byte, error) {
	info, err := f.Stat(target)
	if err != nil {
		return nil, err
	}
	if info.Dir {
		return nil, ErrIsDirectory(target)
	}
	return append([]byte(nil), info.Content...), nil
}
func (f *rollbackFS) ReadDir(target string) ([]FileInfo, error) {
	info, err := f.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.Dir {
		return nil, ErrNotDirectory(target)
	}
	var out []FileInfo
	for candidate, child := range f.nodes {
		if candidate != target && path.Dir(candidate) == CleanPath(target) {
			out = append(out, *child)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	return out, nil
}
func (f *rollbackFS) Mkdir(target string) error {
	target = CleanPath(target)
	if _, exists := f.nodes[target]; exists {
		return errors.New("exists")
	}
	if parent := f.nodes[path.Dir(target)]; parent == nil || !parent.Dir {
		return fs.ErrNotExist
	}
	f.nodes[target] = &FileInfo{FileName: path.Base(target), FilePath: target, Dir: true}
	return nil
}
func (f *rollbackFS) MkdirAll(target string) error {
	current := "/"
	for _, part := range strings.Split(strings.Trim(CleanPath(target), "/"), "/") {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		if info := f.nodes[current]; info != nil {
			if !info.Dir {
				return ErrNotDirectory(current)
			}
			continue
		}
		f.nodes[current] = &FileInfo{FileName: path.Base(current), FilePath: current, Dir: true}
	}
	return nil
}
func (f *rollbackFS) WriteFileAtomic(target string, data []byte, opts WriteOpts) (string, error) {
	target = CleanPath(target)
	if target == f.failTarget && !f.failed {
		f.failed = true
		return "", errors.New("injected I/O failure")
	}
	existing, exists := f.nodes[target]
	currentHash := ""
	if exists && !existing.Dir {
		currentHash = testContentHash(existing.Content)
	}
	if opts.IfNoneMatch && exists {
		return "", &PreconditionError{Path: target, Current: currentHash}
	}
	if opts.IfMatch != nil && (!exists || existing.Dir || currentHash != *opts.IfMatch) {
		return "", &PreconditionError{Path: target, Current: currentHash}
	}
	if err := f.MkdirAll(path.Dir(target)); err != nil {
		return "", err
	}
	if existing != nil && existing.Dir {
		return "", ErrIsDirectory(target)
	}
	f.nodes[target] = &FileInfo{FileName: path.Base(target), FilePath: target, Content: append([]byte(nil), data...), FileSize: int64(len(data))}
	return testContentHash(data), nil
}
func (f *rollbackFS) Remove(target string) error {
	target = CleanPath(target)
	if _, ok := f.nodes[target]; !ok {
		return fs.ErrNotExist
	}
	for candidate := range f.nodes {
		if candidate != target && strings.HasPrefix(candidate, target+"/") {
			return errors.New("not empty")
		}
	}
	delete(f.nodes, target)
	return nil
}
func (f *rollbackFS) RemoveAll(target string, opts RemoveOpts) error {
	target = CleanPath(target)
	if _, ok := f.nodes[target]; !ok {
		return fs.ErrNotExist
	}
	if opts.Expected != nil && !testTreeSnapshotsEqual(opts.Expected, f.treeSnapshot(target)) {
		return &TreeStaleError{Path: target, Detail: "subtree does not match expected snapshot"}
	}
	for candidate := range f.nodes {
		if candidate == target || strings.HasPrefix(candidate, target+"/") {
			delete(f.nodes, candidate)
		}
	}
	return nil
}
func (f *rollbackFS) treeSnapshot(root string) *TreeSnapshot {
	root = CleanPath(root)
	snapshot := &TreeSnapshot{Root: root}
	for candidate, info := range f.nodes {
		if candidate != root && !strings.HasPrefix(candidate, root+"/") {
			continue
		}
		rel := "."
		if candidate != root {
			rel = strings.TrimPrefix(candidate, root+"/")
		}
		op := TreeOp{RelPath: rel, Kind: "dir"}
		if !info.Dir {
			op.Kind = "file"
			op.Hash = testContentHash(info.Content)
			op.Size = int64(len(info.Content))
		}
		snapshot.Ops = append(snapshot.Ops, op)
	}
	return snapshot
}
func testTreeSnapshotsEqual(want, got *TreeSnapshot) bool {
	if want.Root != got.Root || len(want.Ops) != len(got.Ops) {
		return false
	}
	wantByPath := make(map[string]TreeOp, len(want.Ops))
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
func testContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (f *rollbackFS) dump() string {
	var rows []string
	for target, info := range f.nodes {
		kind := "dir"
		if !info.Dir {
			kind = "file:" + string(info.Content)
		}
		rows = append(rows, target+"="+kind)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

func TestRollbackFSHonorsPreconditions(t *testing.T) {
	f := newRollbackFS()
	base, err := f.WriteFileAtomic("/tree/a.md", []byte("original"), WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	staleHash := "stale"
	_, err = f.WriteFileAtomic("/tree/a.md", []byte("lost"), WriteOpts{IfMatch: &staleHash})
	var precondition *PreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("stale IfMatch: want PreconditionError, got %v", err)
	}
	if got, _ := f.ReadFile("/tree/a.md"); string(got) != "original" {
		t.Fatalf("stale IfMatch changed content to %q", got)
	}
	if _, err := f.WriteFileAtomic("/tree/a.md", []byte("updated"), WriteOpts{IfMatch: &base}); err != nil {
		t.Fatalf("matching IfMatch: %v", err)
	}

	expected := f.treeSnapshot("/tree")
	if _, err := f.WriteFileAtomic("/tree/concurrent.md", []byte("concurrent"), WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	err = f.RemoveAll("/tree", RemoveOpts{Expected: expected})
	var treeStale *TreeStaleError
	if !errors.As(err, &treeStale) {
		t.Fatalf("stale tree snapshot: want TreeStaleError, got %v", err)
	}
	if _, err := f.Stat("/tree/concurrent.md"); err != nil {
		t.Fatalf("stale tree removal mutated state: %v", err)
	}
}

func TestCommitChangeSetRollsBackFailedBatch(t *testing.T) {
	f := newRollbackFS()
	_ = f.MkdirAll("/skill/assets")
	_, _ = f.WriteFileAtomic("/skill/SKILL.md", []byte("old skill"), WriteOpts{})
	_, _ = f.WriteFileAtomic("/skill/stale.md", []byte("stale"), WriteOpts{})
	_, _ = f.WriteFileAtomic("/skill/assets/old.md", []byte("old asset"), WriteOpts{})
	before := f.dump()
	f.failTarget = "/skill/fail.md"
	cs := ChangeSet{Changes: []Change{
		{Target: "/skill/SKILL.md", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("old skill")}},
		{Target: "/skill/stale.md", Action: ChangeActionRemove},
		{Target: "/skill/assets", Action: ChangeActionRemoveAll, RemoveAll: &RemoveAllChange{}},
		{Target: "/skill/new", Action: ChangeActionMkdir},
		{Target: "/skill/new/a.md", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("new")}},
		{Target: "/skill/fail.md", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("fail")}},
		{Target: "/skill/SKILL.md", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("new skill")}},
	}}
	result, err := CommitChangeSet(f, cs)
	if err == nil || result.HasCommitted() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if after := f.dump(); after != before {
		t.Fatalf("tree changed after rollback\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestCommitChangeSetRollsBackFileDirectoryTransitions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*rollbackFS)
		first []Change
	}{
		{
			name: "file to directory",
			setup: func(f *rollbackFS) {
				_, _ = f.WriteFileAtomic("/node", []byte("original file"), WriteOpts{})
			},
			first: []Change{
				{Target: "/node", Action: ChangeActionRemove},
				{Target: "/node", Action: ChangeActionMkdir},
				{Target: "/node/new.md", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("new")}},
			},
		},
		{
			name: "directory to file",
			setup: func(f *rollbackFS) {
				_ = f.MkdirAll("/node")
				_, _ = f.WriteFileAtomic("/node/original.md", []byte("original child"), WriteOpts{})
			},
			first: []Change{
				{Target: "/node", Action: ChangeActionRemoveAll, RemoveAll: &RemoveAllChange{}},
				{Target: "/node", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("replacement file")}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newRollbackFS()
			test.setup(f)
			before := f.dump()
			f.failTarget = "/fail.md"
			changes := append([]Change(nil), test.first...)
			changes = append(changes, Change{Target: "/fail.md", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("fail")}})
			result, err := CommitChangeSet(f, ChangeSet{Changes: changes})
			if err == nil || result.HasCommitted() {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if after := f.dump(); after != before {
				t.Fatalf("tree changed after rollback\n--- before\n%s\n--- after\n%s", before, after)
			}
		})
	}
}

func TestCommitChangeSet_WriteCreateOnlyOpts(t *testing.T) {
	fs := &fakeWritableFS{}
	cs := ChangeSet{
		Target: "/wiki/new.md",
		Action: ChangeActionWrite,
		Write:  &WriteChange{Bytes: []byte("x"), Opts: WriteOpts{IfNoneMatch: true}},
	}
	if _, err := CommitChangeSet(fs, cs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fs.wroteOpts.IfNoneMatch {
		t.Fatalf("want IfNoneMatch=true, got %+v", fs.wroteOpts)
	}
	if fs.wroteOpts.IfMatch != nil {
		t.Fatalf("IfMatch must be nil")
	}
}

func TestCommitChangeSet_WriteUnconditionalOpts(t *testing.T) {
	fs := &fakeWritableFS{}
	cs := ChangeSet{
		Target: "/wiki/lww.md",
		Action: ChangeActionWrite,
		Write:  &WriteChange{Bytes: []byte("x")}, // zero WriteOpts = last-write-wins
	}
	if _, err := CommitChangeSet(fs, cs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fs.wroteOpts.IfMatch != nil || fs.wroteOpts.IfNoneMatch {
		t.Fatalf("want unconditional WriteOpts, got %+v", fs.wroteOpts)
	}
}

func TestCommitChangeSet_RemoveAllCarriesOptsVerbatim(t *testing.T) {
	snap := TreeSnapshot{Root: "/wiki/dir", Ops: []TreeOp{{RelPath: ".", Kind: "dir"}}}
	fs := &fakeWritableFS{}
	cs := ChangeSet{
		Target:    "/wiki/dir",
		Action:    ChangeActionRemoveAll,
		RemoveAll: &RemoveAllChange{Opts: RemoveOpts{Expected: &snap}},
	}
	if _, err := CommitChangeSet(fs, cs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fs.removedPath != "/wiki/dir" {
		t.Fatalf("removedPath = %q", fs.removedPath)
	}
	if fs.removedOpts.Expected == nil || fs.removedOpts.Expected.Root != "/wiki/dir" {
		t.Fatalf("want Expected snapshot with root /wiki/dir, got %+v", fs.removedOpts)
	}
}

func TestCommitChangeSet_RemoveAllUnconditionalOpts(t *testing.T) {
	fs := &fakeWritableFS{}
	cs := ChangeSet{
		Target:    "/wiki/dir",
		Action:    ChangeActionRemoveAll,
		RemoveAll: &RemoveAllChange{}, // zero RemoveOpts = unconditional
	}
	if _, err := CommitChangeSet(fs, cs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fs.removedOpts.Expected != nil {
		t.Fatalf("want unconditional delete, got %+v", fs.removedOpts)
	}
}

func TestCommitChangeSet_MkdirRemoveActions(t *testing.T) {
	fs := &fakeWritableFS{}
	if _, err := CommitChangeSet(fs, ChangeSet{Target: "/a", Action: ChangeActionMkdir}); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if fs.mkdirPath != "/a" {
		t.Fatalf("mkdirPath = %q", fs.mkdirPath)
	}
	if _, err := CommitChangeSet(fs, ChangeSet{Target: "/a/b/c", Action: ChangeActionMkdirAll}); err != nil {
		t.Fatalf("mkdir_all: %v", err)
	}
	if fs.mkdirAllPath != "/a/b/c" {
		t.Fatalf("mkdirAllPath = %q", fs.mkdirAllPath)
	}
	if _, err := CommitChangeSet(fs, ChangeSet{Target: "/a/f", Action: ChangeActionRemove}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fs.removePath != "/a/f" {
		t.Fatalf("removePath = %q", fs.removePath)
	}
}

func TestCommitChangeSet_WritePreconditionErrorPropagates(t *testing.T) {
	base := "base123"
	want := &PreconditionError{Path: "/wiki/a.md", Current: "other"}
	fs := &fakeWritableFS{writeErr: want}
	cs := ChangeSet{
		Target: "/wiki/a.md",
		Action: ChangeActionWrite,
		Write:  &WriteChange{Bytes: []byte("hi"), Opts: WriteOpts{IfMatch: &base}},
	}
	_, err := CommitChangeSet(fs, cs)
	var pe *PreconditionError
	if !errors.As(err, &pe) {
		t.Fatalf("want *PreconditionError, got %v", err)
	}
}

func TestCommitChangeSet_MissingPayloadErrors(t *testing.T) {
	fs := &fakeWritableFS{}
	if _, err := CommitChangeSet(fs, ChangeSet{Target: "/a", Action: ChangeActionWrite}); err == nil {
		t.Fatal("want error for missing write payload")
	}
	if _, err := CommitChangeSet(fs, ChangeSet{Target: "/a", Action: ChangeActionRemoveAll}); err == nil {
		t.Fatal("want error for missing remove_all payload")
	}
	if _, err := CommitChangeSet(fs, ChangeSet{Target: "/a", Action: "bogus"}); err == nil {
		t.Fatal("want error for unknown action")
	}
}

func TestValidateChangeSetBatchRejectsEmptyMixedMalformed(t *testing.T) {
	valid := Change{Target: "/a", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("a")}}
	for _, cs := range []ChangeSet{
		{},
		{Target: "/batch", Changes: []Change{valid}},
		{Changes: []Change{{Target: "/a", Action: ChangeActionWrite}}},
	} {
		if err := ValidateChangeSet(cs); err == nil {
			t.Fatalf("accepted invalid changeset: %+v", cs)
		}
	}
	if err := ValidateChangeSet(ChangeSet{Changes: []Change{valid}}); err != nil {
		t.Fatalf("valid batch: %v", err)
	}
}
