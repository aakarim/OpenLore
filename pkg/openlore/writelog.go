package openlore

import (
	"context"
	"errors"
	"log/slog"
	"sync"

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
// on. reply is buffered (cap 1) so the applier never blocks delivering it. actor
// is the (non-durable) principal that triggered the mutation; it never enters
// the ChangeSet but flows to the post-commit chain for attribution/audit.
type logEntry struct {
	cs    vfs.ChangeSet
	actor Actor
	reply chan applyResult
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
	preApply   func(Actor, vfs.ChangeSet) error
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
			err = pre(e.actor, e.cs)
		}
		if err == nil {
			committed, err = vfs.CommitChangeSet(l.substrate, e.cs)
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
		if perr := pc(context.Background(), CommitInfo{ChangeSet: committed.Committed, Hash: committed.Hash, Actor: e.actor}); perr != nil {
			l.logger.Error("post-commit chain failed; log continues",
				"target", e.cs.Target, "action", e.cs.Action, "err", perr)
		}
	}
}

func (l *writeLog) SetPreApply(h func(Actor, vfs.ChangeSet) error) {
	l.mu.Lock()
	l.preApply = h
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
func (l *writeLog) Submit(ctx context.Context, actor Actor, cs vfs.ChangeSet) (string, error) {
	reply := make(chan applyResult, 1)

	// Hold the read lock across the send so Close (which takes the write lock)
	// cannot close the channel underneath an in-progress send.
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		return "", ErrLogClosed
	}
	select {
	case l.ch <- logEntry{cs: cs, actor: actor, reply: reply}:
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
