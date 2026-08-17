package openlore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// wlRecordingFS is a minimal WritableFS that records applied write targets in
// order and returns a deterministic hash. A per-target error can be programmed.
type wlRecordingFS struct {
	mu      sync.Mutex
	applied []string
	errFor  map[string]error
}

func (f *wlRecordingFS) Stat(string) (*vfs.FileInfo, error)     { return nil, errors.New("no") }
func (f *wlRecordingFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, errors.New("no") }
func (f *wlRecordingFS) ReadFile(string) ([]byte, error)        { return nil, errors.New("no") }
func (f *wlRecordingFS) SetWriteable() error                    { return nil }
func (f *wlRecordingFS) SetReadonly() error                     { return nil }
func (f *wlRecordingFS) Mkdir(string) error                     { return nil }
func (f *wlRecordingFS) MkdirAll(string) error                  { return nil }
func (f *wlRecordingFS) Remove(string) error                    { return nil }
func (f *wlRecordingFS) RemoveAll(string, vfs.RemoveOpts) error { return nil }

func (f *wlRecordingFS) WriteFileAtomic(name string, _ []byte, _ vfs.WriteOpts) (string, error) {
	if f.errFor != nil {
		if err := f.errFor[name]; err != nil {
			return "", err
		}
	}
	f.mu.Lock()
	f.applied = append(f.applied, name)
	f.mu.Unlock()
	return "h:" + name, nil
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
		if h != "h:"+p {
			t.Fatalf("hash = %q, want h:%s", h, p)
		}
	}
	got := fs.order()
	want := []string{"/a", "/b", "/c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("apply order = %v, want %v", got, want)
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
	if h != "h:/a" {
		t.Fatalf("hash = %q", h)
	}
	select {
	case info := <-seen:
		if info.Attribution.Principal != "agent-9" || info.Hash != "h:/a" || info.ChangeSet.Target != "/a" {
			t.Fatalf("post-commit info = %+v", info)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-commit chain did not run")
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
