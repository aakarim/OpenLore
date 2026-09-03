package openlore

import (
	"context"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Attribution identifies the principal whose authority is being exercised and,
// for delegated execution, the actor exercising it. It remains outside the
// content-addressed ChangeSet and is persisted alongside each commit.
type Attribution struct {
	Principal  string            `json:"principal"`
	Actor      string            `json:"actor,omitempty"`
	ClientAuth ClientAuthLevel   `json:"client_auth,omitempty"`
	Extra      map[string]string `json:"-"`
	// internal is an unforgeable package capability. Public callers can set ID
	// for attribution, but only OpenLore's own submission path can set this bit.
	internal bool
}

func (a Attribution) String() string {
	if a.Actor == "" {
		return a.Principal
	}
	return a.Principal + "/" + a.Actor
}

// ── Admission (pre-commit write) chain ──────────────────────────────────────
//
// The admission chain runs synchronously in the caller's goroutine BEFORE a
// mutation reaches the write log. It sits inside the fixed, deployment-owned
// scope layer, so it only ever sees in-scope writes. Middleware inspect the
// (immutable) ChangeSet and either allow, defer, or reject:
//
//	allow  → return next(ctx, op)                                 (commit path)
//	defer  → return WriteResult{}, &vfs.PendingChangeError{...}   (park; do NOT call next)
//	reject → return WriteResult{}, err                            (refuse)
//
// Middleware MUST inspect every op.Leaves() entry (a ChangeSet may be
// a batch) and treat the ChangeSet as immutable — inspect and decide only,
// never rewrite the proposed bytes or snapshot.

// WriteOp is the input to the admission chain.
type WriteOp struct {
	changeSet   vfs.ChangeSet
	Attribution Attribution
	identity    *Identity
}

// NewWriteOp constructs an immutable admission operation. The changeset is
// intentionally not exposed: policy middleware must inspect every leaf.
func NewWriteOp(attribution Attribution, cs vfs.ChangeSet) WriteOp {
	return WriteOp{changeSet: cloneWriteChangeSet(cs), Attribution: attribution}
}

func newIdentityWriteOp(identity Identity, cs vfs.ChangeSet) WriteOp {
	op := NewWriteOp(identity.attribution(), cs)
	op.identity = &identity
	return op
}

// Leaves returns every proposed mutation in execution order.
func (op WriteOp) Leaves() []vfs.Change { return cloneWriteChangeSet(op.changeSet).Leaves() }

// Pending captures the complete operation for durable deferred processing.
func (op WriteOp) Pending(ref string) *vfs.PendingChangeError {
	return &vfs.PendingChangeError{ChangeSet: cloneWriteChangeSet(op.changeSet), Ref: ref}
}

// persistenceChangeSet is restricted to package-owned commit/persistence seams.
func (op WriteOp) persistenceChangeSet() vfs.ChangeSet { return cloneWriteChangeSet(op.changeSet) }

func cloneWriteChangeSet(cs vfs.ChangeSet) vfs.ChangeSet {
	cloneLeaf := func(leaf vfs.Change) vfs.Change {
		out := leaf
		if leaf.Write != nil {
			write := *leaf.Write
			write.Bytes = append([]byte(nil), leaf.Write.Bytes...)
			out.Write = &write
		}
		if leaf.RemoveAll != nil {
			remove := *leaf.RemoveAll
			if remove.Opts.Expected != nil {
				snapshot := *remove.Opts.Expected
				snapshot.Ops = append([]vfs.TreeOp(nil), snapshot.Ops...)
				remove.Opts.Expected = &snapshot
			}
			out.RemoveAll = &remove
		}
		if leaf.Xattr != nil {
			xattr := *leaf.Xattr
			xattr.Value = append([]byte(nil), leaf.Xattr.Value...)
			out.Xattr = &xattr
		}
		if leaf.XattrRepair != nil {
			r := &vfs.XattrRepairChange{Attributes: map[string][]byte{}}
			for k, v := range leaf.XattrRepair.Attributes {
				r.Attributes[k] = append([]byte(nil), v...)
			}
			out.XattrRepair = r
		}
		if leaf.XattrMigration != nil {
			m := *leaf.XattrMigration
			m.ExpectedEnvelopeSHA256 = append([]byte(nil), m.ExpectedEnvelopeSHA256...)
			m.Edits = append([]vfs.XattrEdit(nil), m.Edits...)
			for i := range m.Edits {
				m.Edits[i].Value = append([]byte(nil), m.Edits[i].Value...)
			}
			out.XattrMigration = &m
		}
		return out
	}
	out := cs
	leaf := cloneLeaf(vfs.Change{Target: cs.Target, Action: cs.Action, Write: cs.Write, RemoveAll: cs.RemoveAll, Xattr: cs.Xattr, XattrRepair: cs.XattrRepair, XattrMigration: cs.XattrMigration})
	out.Write, out.RemoveAll, out.Xattr, out.XattrRepair, out.XattrMigration = leaf.Write, leaf.RemoveAll, leaf.Xattr, leaf.XattrRepair, leaf.XattrMigration
	if cs.Changes != nil {
		out.Changes = make([]vfs.Change, len(cs.Changes))
		for i, change := range cs.Changes {
			out.Changes[i] = cloneLeaf(change)
		}
	}
	return out
}

// WriteResult is the outcome of a committed mutation.
type WriteResult struct {
	// Hash is the committed content hash (empty for a delete, or when the
	// mutation was deferred/rejected).
	Hash string
}

// WriteHandler commits or hands off a WriteOp. The terminal handler submits to
// the write log; middleware wrap it.
type WriteHandler func(ctx context.Context, op WriteOp) (WriteResult, error)

// WriteMiddleware wraps a WriteHandler.
type WriteMiddleware func(next WriteHandler) WriteHandler

// WriteMiddlewareProvider is implemented by a plugin that contributes admission
// middleware. The server composes providers' middleware in registration order,
// after the fixed scope layer.
type WriteMiddlewareProvider interface {
	WriteMiddleware() []WriteMiddleware
}

// chainWrite composes mws around terminal. mws[0] is the OUTERMOST layer (runs
// first); execution order == registration order.
func chainWrite(terminal WriteHandler, mws ...WriteMiddleware) WriteHandler {
	h := terminal
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ── Post-commit chain ───────────────────────────────────────────────────────
//
// The post-commit chain runs at the applier AFTER a change is durably committed,
// for both fresh writes and approved held changesets. It is where the feed and
// external (post_write) hooks fire. It cannot veto — the write already happened.

// CommitInfo describes a committed change.
type CommitInfo struct {
	ChangeSet   vfs.ChangeSet
	Hash        string
	Attribution Attribution
}

// PostCommitHandler processes a committed change.
type PostCommitHandler func(ctx context.Context, info CommitInfo) error

// PostCommitMiddleware wraps a PostCommitHandler.
type PostCommitMiddleware func(next PostCommitHandler) PostCommitHandler

// PostCommitProvider is implemented by a plugin that contributes post-commit
// middleware.
type PostCommitProvider interface {
	PostCommitMiddleware() []PostCommitMiddleware
}

// chainPostCommit composes mws around terminal. mws[0] is outermost.
func chainPostCommit(terminal PostCommitHandler, mws ...PostCommitMiddleware) PostCommitHandler {
	h := terminal
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ── Read chain ──────────────────────────────────────────────────────────────
//
// The read chain runs BEFORE a read reaches the substrate. It is a
// before-read gate: a middleware can run work (e.g. a debounced git pull) and,
// on failure, abort the read by returning an error. It does not transform the
// bytes.

// ReadKind names the read operation a ReadOp refers to.
type ReadKind string

const (
	ReadKindStat ReadKind = "stat"
	ReadKindDir  ReadKind = "readdir"
	ReadKindFile ReadKind = "readfile"
)

// ReadOp is the input to the read chain.
type ReadOp struct {
	Path        string
	Kind        ReadKind
	Attribution Attribution
}

// ReadHandler runs the before-read step. A non-nil error aborts the read.
type ReadHandler func(ctx context.Context, op ReadOp) error

// ReadMiddleware wraps a ReadHandler.
type ReadMiddleware func(next ReadHandler) ReadHandler

// ReadMiddlewareProvider is implemented by a plugin that contributes read
// middleware.
type ReadMiddlewareProvider interface {
	ReadMiddleware() []ReadMiddleware
}

// ContentTransform changes bytes presented to a caller without changing stored
// bytes. Transforms run outside read tracking so CAS always records storage.
type ContentTransform func(path string, content []byte) []byte
type ContentTransformProvider interface{ ContentTransforms() []ContentTransform }

// chainRead composes mws around terminal. mws[0] is outermost.
func chainRead(terminal ReadHandler, mws ...ReadMiddleware) ReadHandler {
	h := terminal
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
