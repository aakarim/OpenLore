package openlore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// wlRecordingFS is a small in-memory WritableFS that records applied write
// targets in order. It enforces the same write and whole-tree preconditions as
// a real substrate. A per-target error can be programmed.
type wlRecordingFS struct {
	mu      sync.Mutex
	applied []string
	errFor  map[string]error
	nodes   map[string]*vfs.FileInfo
}

func (f *wlRecordingFS) ensureNodesLocked() {
	if f.nodes == nil {
		f.nodes = map[string]*vfs.FileInfo{
			"/": {FileName: "/", FilePath: "/", Dir: true},
		}
	}
}

func (f *wlRecordingFS) Stat(name string) (*vfs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	info, ok := f.nodes[vfs.CleanPath(name)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	copy := *info
	copy.Content = append([]byte(nil), info.Content...)
	return &copy, nil
}

func (f *wlRecordingFS) ReadDir(name string) ([]vfs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	clean := vfs.CleanPath(name)
	info, ok := f.nodes[clean]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if !info.Dir {
		return nil, vfs.ErrNotDirectory(clean)
	}
	var children []vfs.FileInfo
	for candidate, child := range f.nodes {
		if candidate != clean && path.Dir(candidate) == clean {
			copy := *child
			copy.Content = append([]byte(nil), child.Content...)
			children = append(children, copy)
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].FileName < children[j].FileName })
	return children, nil
}

func (f *wlRecordingFS) ReadFile(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	info, ok := f.nodes[vfs.CleanPath(name)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if info.Dir {
		return nil, vfs.ErrIsDirectory(name)
	}
	return append([]byte(nil), info.Content...), nil
}

func (f *wlRecordingFS) SetWriteable() error { return nil }
func (f *wlRecordingFS) SetReadonly() error  { return nil }

func (f *wlRecordingFS) Mkdir(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	clean := vfs.CleanPath(name)
	if _, exists := f.nodes[clean]; exists {
		return fs.ErrExist
	}
	parent, ok := f.nodes[path.Dir(clean)]
	if !ok {
		return fs.ErrNotExist
	}
	if !parent.Dir {
		return vfs.ErrNotDirectory(path.Dir(clean))
	}
	f.nodes[clean] = &vfs.FileInfo{FileName: path.Base(clean), FilePath: clean, Dir: true}
	return nil
}

func (f *wlRecordingFS) mkdirAllLocked(name string) error {
	current := "/"
	for _, part := range strings.Split(strings.Trim(vfs.CleanPath(name), "/"), "/") {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		if info, ok := f.nodes[current]; ok {
			if !info.Dir {
				return vfs.ErrNotDirectory(current)
			}
			continue
		}
		f.nodes[current] = &vfs.FileInfo{FileName: path.Base(current), FilePath: current, Dir: true}
	}
	return nil
}

func (f *wlRecordingFS) MkdirAll(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	return f.mkdirAllLocked(name)
}

func (f *wlRecordingFS) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	clean := vfs.CleanPath(name)
	if _, exists := f.nodes[clean]; !exists {
		return fs.ErrNotExist
	}
	for candidate := range f.nodes {
		if candidate != clean && strings.HasPrefix(candidate, clean+"/") {
			return errors.New("directory not empty")
		}
	}
	delete(f.nodes, clean)
	return nil
}

func (f *wlRecordingFS) RemoveAll(name string, opts vfs.RemoveOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureNodesLocked()
	clean := vfs.CleanPath(name)
	if _, exists := f.nodes[clean]; !exists {
		return fs.ErrNotExist
	}
	if opts.Expected != nil && !wlSnapshotsEqual(opts.Expected, f.snapshotLocked(clean)) {
		return &vfs.TreeStaleError{Path: clean, Detail: "subtree does not match expected snapshot"}
	}
	for candidate := range f.nodes {
		if candidate == clean || strings.HasPrefix(candidate, clean+"/") {
			delete(f.nodes, candidate)
		}
	}
	return nil
}

func (f *wlRecordingFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errFor != nil {
		if err := f.errFor[name]; err != nil {
			return "", err
		}
	}
	f.ensureNodesLocked()
	clean := vfs.CleanPath(name)
	current, exists := f.nodes[clean]
	currentHash := ""
	if exists && !current.Dir {
		currentHash = wlHash(current.Content)
	}
	if opts.IfNoneMatch && exists {
		return "", &vfs.PreconditionError{Path: clean, Current: currentHash}
	}
	if opts.IfMatch != nil && (!exists || current.Dir || currentHash != *opts.IfMatch) {
		return "", &vfs.PreconditionError{Path: clean, Current: currentHash}
	}
	if err := f.mkdirAllLocked(path.Dir(clean)); err != nil {
		return "", err
	}
	if exists && current.Dir {
		return "", vfs.ErrIsDirectory(clean)
	}
	copy := append([]byte(nil), data...)
	f.nodes[clean] = &vfs.FileInfo{FileName: path.Base(clean), FilePath: clean, Content: copy, FileSize: int64(len(copy))}
	f.applied = append(f.applied, clean)
	return wlHash(copy), nil
}

func (f *wlRecordingFS) snapshotLocked(root string) *vfs.TreeSnapshot {
	snapshot := &vfs.TreeSnapshot{Root: root}
	for candidate, info := range f.nodes {
		if candidate != root && !strings.HasPrefix(candidate, root+"/") {
			continue
		}
		rel := "."
		if candidate != root {
			rel = strings.TrimPrefix(candidate, root+"/")
		}
		op := vfs.TreeOp{RelPath: rel, Kind: "dir"}
		if !info.Dir {
			op.Kind = "file"
			op.Hash = wlHash(info.Content)
			op.Size = int64(len(info.Content))
		}
		snapshot.Ops = append(snapshot.Ops, op)
	}
	return snapshot
}

func wlSnapshotsEqual(want, got *vfs.TreeSnapshot) bool {
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

func wlHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (f *wlRecordingFS) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.applied...)
}

// wlGatedFS blocks each WriteFileAtomic until the test releases a gate token,
// announcing entry on `entered`. Used to hold the applier deterministically.
type wlGatedFS struct {
	wlRecordingFS
	entered chan string
	gate    chan struct{}
}

type failingCommitRecorder struct{ err error }

func (r failingCommitRecorder) RecordCommit(context.Context, CommitRecord) error { return r.err }

func (g *wlGatedFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	g.entered <- name
	<-g.gate
	return g.wlRecordingFS.WriteFileAtomic(name, data, opts)
}

func writeCS(target string) vfs.ChangeSet {
	return vfs.ChangeSet{
		Target: target,
		Action: vfs.ChangeActionWrite,
		Write:  &vfs.WriteChange{Bytes: []byte("x")},
	}
}

func TestWriteLog_AppliesInOrderAndAwaits(t *testing.T) {
	fs := &wlRecordingFS{}
	l := newWriteLog(fs, nil, nil, 8)
	defer l.Close(context.Background())

	for _, p := range []string{"/a", "/b", "/c"} {
		h, err := l.Submit(context.Background(), Attribution{}, writeCS(p))
		if err != nil {
			t.Fatalf("submit %s: %v", p, err)
		}
		if h != wlHash([]byte("x")) {
			t.Fatalf("hash = %q, want SHA-256 of committed bytes", h)
		}
	}
	got := fs.order()
	want := []string{"/a", "/b", "/c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("apply order = %v, want %v", got, want)
	}
}

func TestWriteLogPreservesAndEnforcesWritePreconditions(t *testing.T) {
	fs := &wlRecordingFS{}
	l := newWriteLog(fs, nil, nil, 4)
	defer l.Close(context.Background())

	base, err := l.Submit(context.Background(), Attribution{}, writeCS("/cas"))
	if err != nil {
		t.Fatal(err)
	}
	stale := "stale"
	for name, opts := range map[string]vfs.WriteOpts{
		"IfMatch":     {IfMatch: &stale},
		"IfNoneMatch": {IfNoneMatch: true},
	} {
		cs := vfs.ChangeSet{Target: "/cas", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("lost"), Opts: opts}}
		_, err := l.Submit(context.Background(), Attribution{}, cs)
		var precondition *vfs.PreconditionError
		if !errors.As(err, &precondition) {
			t.Fatalf("%s: want PreconditionError, got %v", name, err)
		}
	}
	if got, err := fs.ReadFile("/cas"); err != nil || string(got) != "x" {
		t.Fatalf("failed precondition changed file: content=%q err=%v", got, err)
	}

	cs := vfs.ChangeSet{Target: "/cas", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("updated"), Opts: vfs.WriteOpts{IfMatch: &base}}}
	if _, err := l.Submit(context.Background(), Attribution{}, cs); err != nil {
		t.Fatalf("matching IfMatch: %v", err)
	}
}

func TestWriteLogPreservesAndEnforcesRemovePrecondition(t *testing.T) {
	fs := &wlRecordingFS{}
	l := newWriteLog(fs, nil, nil, 4)
	defer l.Close(context.Background())

	if _, err := l.Submit(context.Background(), Attribution{}, vfs.ChangeSet{Target: "/tree", Action: vfs.ChangeActionMkdirAll}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Submit(context.Background(), Attribution{}, writeCS("/tree/a")); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	expected := fs.snapshotLocked("/tree")
	fs.mu.Unlock()
	if _, err := l.Submit(context.Background(), Attribution{}, writeCS("/tree/concurrent")); err != nil {
		t.Fatal(err)
	}

	cs := vfs.ChangeSet{
		Target:    "/tree",
		Action:    vfs.ChangeActionRemoveAll,
		RemoveAll: &vfs.RemoveAllChange{Opts: vfs.RemoveOpts{Expected: expected}},
	}
	_, err := l.Submit(context.Background(), Attribution{}, cs)
	var stale *vfs.TreeStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("want TreeStaleError, got %v", err)
	}
	if _, err := fs.Stat("/tree/concurrent"); err != nil {
		t.Fatalf("stale removal changed tree: %v", err)
	}
}

func TestWriteLog_PostCommitRunsWithActorAndDoesNotBlockSubmit(t *testing.T) {
	fs := &wlRecordingFS{}
	seen := make(chan CommitInfo, 1)
	pc := func(_ context.Context, info CommitInfo) error {
		seen <- info
		return errors.New("post-commit boom") // must NOT surface to the submitter
	}
	l := newWriteLog(fs, pc, nil, 4)
	defer l.Close(context.Background())

	h, err := l.Submit(context.Background(), Attribution{Principal: "agent-9"}, writeCS("/a"))
	if err != nil {
		t.Fatalf("submit err (post-commit failure must not surface): %v", err)
	}
	if h != wlHash([]byte("x")) {
		t.Fatalf("hash = %q", h)
	}
	select {
	case info := <-seen:
		if info.Attribution.Principal != "agent-9" || info.Hash != wlHash([]byte("x")) || info.ChangeSet.Target != "/a" {
			t.Fatalf("post-commit info = %+v", info)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-commit chain did not run")
	}
}

func TestWriteLogCommitStateRunsBeforeSuccessAndSurfacesFailure(t *testing.T) {
	fs := &wlRecordingFS{}
	stateErr := errors.New("state disk full")
	var stateSawContent bool
	l := newWriteLog(fs, nil, nil, 1)
	l.SetCommitState(func(_ context.Context, info CommitInfo) error {
		content, err := fs.ReadFile(info.ChangeSet.Target)
		stateSawContent = err == nil && string(content) == "x"
		return stateErr
	})
	defer l.Close(context.Background())

	if _, err := l.Submit(context.Background(), Attribution{}, writeCS("/committed")); !errors.Is(err, stateErr) {
		t.Fatalf("state failure = %v", err)
	}
	if !stateSawContent {
		t.Fatal("state hook ran before content commit")
	}
	if content, err := fs.ReadFile("/committed"); err != nil || string(content) != "x" {
		t.Fatalf("content was not committed: content=%q err=%v", content, err)
	}
}

func TestWriteLog_RecorderFailureDoesNotTurnCommittedWriteIntoFailure(t *testing.T) {
	fs := &wlRecordingFS{}
	l := newWriteLog(fs, nil, nil, 1)
	l.SetCommitRecorder(failingCommitRecorder{err: errors.New("history disk full")})
	defer l.Close(context.Background())

	hash, err := l.Submit(context.Background(), Attribution{Principal: "alice"}, writeCS("/committed"))
	if err != nil || hash != wlHash([]byte("x")) {
		t.Fatalf("durable commit reported hash=%q err=%v", hash, err)
	}
	if got := fs.order(); len(got) != 1 || got[0] != "/committed" {
		t.Fatalf("applied=%v", got)
	}
}

func TestWriteLog_ApplyErrorSkipsPostCommit(t *testing.T) {
	boom := errors.New("cas drift")
	fs := &wlRecordingFS{errFor: map[string]error{"/x": boom}}
	ran := make(chan struct{}, 1)
	pc := func(_ context.Context, _ CommitInfo) error { ran <- struct{}{}; return nil }
	l := newWriteLog(fs, pc, nil, 4)
	defer l.Close(context.Background())

	if _, err := l.Submit(context.Background(), Attribution{}, writeCS("/x")); !errors.Is(err, boom) {
		t.Fatalf("want cas drift, got %v", err)
	}
	select {
	case <-ran:
		t.Fatal("post-commit must not run when the commit failed")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWriteLog_ApplyErrorPropagates(t *testing.T) {
	boom := errors.New("cas drift")
	fs := &wlRecordingFS{errFor: map[string]error{"/x": boom}}
	l := newWriteLog(fs, nil, nil, 4)
	defer l.Close(context.Background())

	_, err := l.Submit(context.Background(), Attribution{}, writeCS("/x"))
	if !errors.Is(err, boom) {
		t.Fatalf("want cas drift error, got %v", err)
	}
}

func TestWriteLog_SubmitAfterCloseReturnsClosed(t *testing.T) {
	l := newWriteLog(&wlRecordingFS{}, nil, nil, 4)
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := l.Submit(context.Background(), Attribution{}, writeCS("/a")); !errors.Is(err, ErrLogClosed) {
		t.Fatalf("want ErrLogClosed, got %v", err)
	}
	// Close is idempotent.
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestWriteLog_CloseDrainsInFlightAndQueued proves no acknowledged write is lost
// on shutdown: an in-flight apply completes and a queued entry is still applied
// after Close() closes the log.
func TestWriteLog_CloseDrainsInFlightAndQueued(t *testing.T) {
	fs := &wlGatedFS{entered: make(chan string, 8), gate: make(chan struct{})}
	l := newWriteLog(fs, nil, nil, 8)

	results := make(chan error, 2)
	go func() { _, err := l.Submit(context.Background(), Attribution{}, writeCS("/a")); results <- err }()

	// Applier has consumed /a and is blocked applying it.
	if got := <-fs.entered; got != "/a" {
		t.Fatalf("first apply = %q, want /a", got)
	}

	// Submit /b; it lands in the buffer (applier is busy on /a).
	go func() { _, err := l.Submit(context.Background(), Attribution{}, writeCS("/b")); results <- err }()
	waitFor(t, func() bool { return len(l.ch) == 1 }) // /b is buffered

	// Close now: channel closes while /a is in-flight and /b is queued.
	closed := make(chan error, 1)
	go func() { closed <- l.Close(context.Background()) }()

	// Release /a, then /b (which the applier drains from the closed channel).
	gate := func(expect string) {
		if got := <-fs.entered; got != expect { // /b announces entry after /a finishes
			t.Errorf("apply = %q, want %q", got, expect)
		}
	}
	fs.gate <- struct{}{} // /a proceeds
	gate("/b")            // applier picked queued /b and is applying it
	fs.gate <- struct{}{} // /b proceeds

	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("submit err: %v", err)
		}
	}
	if err := <-closed; err != nil {
		t.Fatalf("close err: %v", err)
	}
	if got := fmt.Sprint(fs.order()); got != "[/a /b]" {
		t.Fatalf("applied = %s, want [/a /b]", got)
	}
}

func TestWriteLogPreflightsWholeBatchBeforeFirstMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := NewDirFS(root, config.FilesConfig{Allowed: []string{"*.md"}}).WithDocsetRoots([]string{"/skills"})
	if err := dir.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	merged := NewMergeFS()
	merged.SetRoot(dir)
	log := newWriteLog(merged, nil, nil, 1)
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	cs := vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/skills/imported", Action: vfs.ChangeActionMkdir},
		{Target: "/skills/imported/payload.exe", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("bad")}},
	}}
	if _, err := log.Submit(context.Background(), Attribution{}, cs); err == nil {
		t.Fatal("policy-invalid batch accepted")
	}
	if _, err := dir.Stat("/skills/imported"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("batch partially created destination: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for condition")
		}
		time.Sleep(time.Millisecond)
	}
}
