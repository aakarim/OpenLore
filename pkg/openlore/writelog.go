package openlore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// ErrLogClosed is returned by writeLog.Submit once the log is shutting down.
var ErrLogClosed = errors.New("openlore: write log closed")

// applyResult is the outcome of committing one log entry: the committed content
// hash (empty for anything but a write) and any CAS/commit error.
type applyResult struct {
	hash string
	err  error
}

// logEntry is one queued mutation plus the buffered channel its submitter blocks
// on. reply is buffered (cap 1) so the applier never blocks delivering it.
// Attribution carries the principal and optional delegate that triggered it. It
// never enters the ChangeSet, but is durably recorded alongside each commit.
type logEntry struct {
	cs          vfs.ChangeSet
	attribution Attribution
	identity    *Identity
	reply       chan applyResult
}

// CommitRecord is the durable, queryable provenance record for one committed
// ChangeSet. Fact-level provenance is derived from these records.
type CommitRecord struct {
	Time        time.Time     `json:"time"`
	Attribution Attribution   `json:"attribution"`
	ChangeSet   vfs.ChangeSet `json:"change_set"`
	Hash        string        `json:"hash,omitempty"`
}

type CommitRecorder interface {
	RecordCommit(context.Context, CommitRecord) error
}

type JSONLCommitRecorder struct {
	mu   sync.Mutex
	path string
}

func NewJSONLCommitRecorder(path string) *JSONLCommitRecorder {
	return &JSONLCommitRecorder{path: path}
}

func (r *JSONLCommitRecorder) RecordCommit(_ context.Context, record CommitRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(record); err != nil {
		return err
	}
	return f.Sync()
}

// writeLog is the ordered write log and its single serialized applier — the sole
// writer to the substrate. Both fresh admitted writes and approved held
// ChangeSets are submitted here; one applier goroutine drains them in order and
// commits each via vfs.CommitChangeSet, so compare-and-swap runs against
// current state with no concurrent writers (race-free ordering). Because mkdir,
// remove, and writes all flow through the one log, a write can never race ahead
// of (or land on) a removed path.
//
// It is in-memory and non-durable by design: with await semantics (Submit
// blocks until the applier replies) nothing is ever "accepted" until it is
// applied and durable in the substrate, so a crash loses only in-flight,
// un-acknowledged writes — the caller's Submit returns an error and it retries.
// Durability lives in the substrate and, for pending changes, the consumer's
// held-changeset store.
type writeLog struct {
	substrate vfs.WritableFS
	logger    *slog.Logger

	mu         sync.RWMutex      // guards closed + postCommit + serializes sends against Close
	postCommit PostCommitHandler // optional; runs at the applier after a durable commit
	preApply   func(*Identity, Attribution, vfs.ChangeSet) error
	recorder   CommitRecorder
	closed     bool
	ch         chan logEntry

	done chan struct{} // closed when the applier goroutine has exited
}

// newWriteLog starts the applier goroutine and returns the log. substrate is the
// sole write target; postCommit (nil = none) runs at the applier after each
// durable commit; buffer is the queue depth (<=0 → 256); logger (nil →
// slog.Default) records post-commit failures.
func newWriteLog(substrate vfs.WritableFS, postCommit PostCommitHandler, logger *slog.Logger, buffer int) *writeLog {
	if buffer <= 0 {
		buffer = 256
	}
	if logger == nil {
		logger = slog.Default()
	}
	l := &writeLog{
		substrate:  substrate,
		postCommit: postCommit,
		logger:     logger,
		ch:         make(chan logEntry, buffer),
		done:       make(chan struct{}),
	}
	go l.run()
	return l
}

// run is the single applier loop. It drains the channel in FIFO order, applying
// each entry, and exits once the channel is closed and empty (graceful drain).
//
// The submitter is unblocked as soon as the write is durable (its reply is
// sent), then the post-commit chain runs — still on the applier goroutine, so
// it stays ordered with subsequent commits. A post-commit failure is logged and
// the log keeps moving: the write is already durable, so halting would only
// strand later writes (external side-effects may drift; reconciliation is the
// operator's job).
func (l *writeLog) run() {
	defer close(l.done)
	for e := range l.ch {
		var committed vfs.CommitResult
		var err error
		if preflight, ok := l.substrate.(vfs.ChangePreflighter); ok {
			for _, change := range e.cs.Leaves() {
				if err = preflight.PreflightChange(change); err != nil {
					break
				}
			}
		}
		l.mu.RLock()
		pre := l.preApply
		l.mu.RUnlock()
		if err == nil && pre != nil {
			err = pre(e.identity, e.attribution, e.cs)
		}
		if err == nil {
			committed, err = vfs.CommitChangeSet(l.substrate, e.cs)
		}
		if err == nil && committed.HasCommitted() {
			l.mu.RLock()
			recorder := l.recorder
			l.mu.RUnlock()
			if recorder != nil {
				if recordErr := recorder.RecordCommit(context.Background(), CommitRecord{
					Time: time.Now().UTC(), Attribution: e.attribution,
					ChangeSet: committed.Committed, Hash: committed.Hash,
				}); recordErr != nil {
					l.logger.Error("commit provenance recording failed after durable write",
						"target", e.cs.Target, "action", e.cs.Action, "hash", committed.Hash, "err", recordErr)
				}
			}
		}
		e.reply <- applyResult{hash: committed.Hash, err: err}
		if !committed.HasCommitted() {
			continue
		}
		l.mu.RLock()
		pc := l.postCommit
		l.mu.RUnlock()
		if pc == nil {
			continue
		}
		if perr := pc(context.Background(), CommitInfo{ChangeSet: committed.Committed, Hash: committed.Hash, Attribution: e.attribution}); perr != nil {
			l.logger.Error("post-commit chain failed; log continues",
				"target", e.cs.Target, "action", e.cs.Action, "err", perr)
		}
	}
}

func (l *writeLog) SetPreApply(h func(*Identity, Attribution, vfs.ChangeSet) error) {
	l.mu.Lock()
	l.preApply = h
	l.mu.Unlock()
}

func (l *writeLog) SetCommitRecorder(recorder CommitRecorder) {
	l.mu.Lock()
	l.recorder = recorder
	l.mu.Unlock()
}

// SetPostCommit replaces the post-commit handler run by the applier after each
// durable commit. It lets a consumer late-bind post-commit middleware registered
// after the log was constructed (see Server.RegisterPlugin). Safe to call
// concurrently with the applier.
func (l *writeLog) SetPostCommit(h PostCommitHandler) {
	l.mu.Lock()
	l.postCommit = h
	l.mu.Unlock()
}

// Submit appends cs to the log and blocks until the applier commits it (await),
// returning the committed hash or the CAS/commit error. actor is carried to the
// post-commit chain. It returns ErrLogClosed if the log is shutting down, or
// ctx.Err() if ctx is cancelled first.
func (l *writeLog) Submit(ctx context.Context, attribution Attribution, cs vfs.ChangeSet) (string, error) {
	return l.submit(ctx, nil, attribution, cs)
}

func (l *writeLog) SubmitIdentity(ctx context.Context, identity Identity, cs vfs.ChangeSet) (string, error) {
	return l.submit(ctx, &identity, identity.attribution(), cs)
}

func (l *writeLog) submit(ctx context.Context, identity *Identity, attribution Attribution, cs vfs.ChangeSet) (string, error) {
	reply := make(chan applyResult, 1)

	// Hold the read lock across the send so Close (which takes the write lock)
	// cannot close the channel underneath an in-progress send.
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		return "", ErrLogClosed
	}
	select {
	case l.ch <- logEntry{cs: cs, attribution: attribution, identity: identity, reply: reply}:
		l.mu.RUnlock()
	case <-ctx.Done():
		l.mu.RUnlock()
		return "", ctx.Err()
	}

	select {
	case r := <-reply:
		return r.hash, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r CommitRecord) DisplayAttribution() string { return r.Attribution.String() }

func (r CommitRecord) Validate() error {
	if r.Attribution.Principal == "" {
		return fmt.Errorf("commit attribution principal is required")
	}
	return nil
}

// Close stops accepting new submits, lets the applier drain all queued entries
// (each still gets its reply), and waits for the applier goroutine to exit or
// ctx to expire. It is idempotent.
func (l *writeLog) Close(ctx context.Context) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	close(l.ch) // safe: no send is in flight (sends hold RLock) and none can start
	l.mu.Unlock()

	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
