package vfs

import (
	"errors"
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

type prefixFailFS struct {
	fakeWritableFS
	errFor map[string]error
}

func (f *prefixFailFS) WriteFileAtomic(name string, data []byte, opts WriteOpts) (string, error) {
	if err := f.errFor[name]; err != nil {
		return "", err
	}
	return f.fakeWritableFS.WriteFileAtomic(name, data, opts)
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

func TestCommitChangeSetReturnsExactCommittedPrefix(t *testing.T) {
	boom := errors.New("second failed")
	fs := &prefixFailFS{errFor: map[string]error{"/second": boom}}
	cs := ChangeSet{Changes: []Change{
		{Target: "/first", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("1")}},
		{Target: "/second", Action: ChangeActionWrite, Write: &WriteChange{Bytes: []byte("2")}},
	}}
	result, err := CommitChangeSet(fs, cs)
	if !errors.Is(err, boom) || !result.HasCommitted() || len(result.Committed.Changes) != 1 || result.Committed.Changes[0].Target != "/first" {
		t.Fatalf("result=%+v err=%v", result, err)
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
