package vfs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"syscall"
)

// ChangeAction discriminates the kind of mutation a ChangeSet describes. Every
// mutating WritableFS method has a corresponding action, so every namespace
// mutation — file writes, directory creation, and removals — flows through the
// ordered log as a ChangeSet. Directories are first-class namespace entries
// (like a special filetype), so their creation and removal are logged
// operations too: this is what prevents a write from racing ahead of a remove
// (or landing on an already-removed path) — the single serialized applier
// orders them.
type ChangeAction string

const (
	// ChangeActionWrite is a single-file whole-object write (WriteFileAtomic).
	ChangeActionWrite ChangeAction = "write"
	// ChangeActionMkdir creates a single directory (Mkdir).
	ChangeActionMkdir ChangeAction = "mkdir"
	// ChangeActionMkdirAll creates a directory and any missing parents
	// (MkdirAll).
	ChangeActionMkdirAll ChangeAction = "mkdir_all"
	// ChangeActionRemove removes a single file or empty directory (Remove).
	ChangeActionRemove ChangeAction = "remove"
	// ChangeActionRemoveAll is an atomic whole-tree removal (RemoveAll).
	ChangeActionRemoveAll                 ChangeAction = "remove_all"
	ChangeActionSetXattr                  ChangeAction = "set_xattr"
	ChangeActionRemoveXattr               ChangeAction = "remove_xattr"
	ChangeActionPreserveAndRecreateXattrs ChangeAction = "preserve_and_recreate_xattrs"
	ChangeActionMigrateXattrs             ChangeAction = "migrate_xattrs"
)

// ChangeSet is an immutable, serializable description of either one mutation
// or an ordered, rollback-protected batch. A batch's aggregate committed hash
// is empty.
//
// It is the single primitive a consumer persists (e.g. while a write awaits
// human approval) and later replays with CommitChangeSet. It deliberately
// carries no approver / status / capability / proposer fields: those are the
// consumer's policy concern, not part of the content-addressed change.
//
// Action selects the payload: a write carries the exact proposed bytes plus its
// precondition contract; a remove_all carries the delete precondition. mkdir /
// mkdir_all / remove need only Target.
type ChangeSet struct {
	Target         string             `json:"target"`
	Action         ChangeAction       `json:"action"`
	Write          *WriteChange       `json:"write,omitempty"`
	RemoveAll      *RemoveAllChange   `json:"remove_all,omitempty"`
	Xattr          *XattrChange       `json:"xattr,omitempty"`
	XattrRepair    *XattrRepairChange `json:"xattr_repair,omitempty"`
	XattrMigration *XattrMigration    `json:"xattr_migration,omitempty"`
	Changes        []Change           `json:"changes,omitempty"`
	Moves          []Move             `json:"moves,omitempty"`
}

// Move explicitly pairs a write leaf with the remove leaf that supplies its
// prior path. Indices refer to Changes and remove ambiguity when equal-content
// files are moved in the same batch.
type Move struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// ChangeSetAdmitter is an optional session-filesystem capability for submitting
// one ordered batch through the same scope and admission pipeline as writes.
type ChangeSetAdmitter interface {
	AdmitChangeSet(ChangeSet) error
}

// ChangePreflighter optionally rejects deterministic substrate-policy failures
// before an ordered batch applies its first leaf. It must not mutate state.
type ChangePreflighter interface {
	PreflightChange(Change) error
}

// Change is a non-recursive leaf in a batch ChangeSet.
type Change struct {
	Target         string             `json:"target"`
	Action         ChangeAction       `json:"action"`
	Write          *WriteChange       `json:"write,omitempty"`
	RemoveAll      *RemoveAllChange   `json:"remove_all,omitempty"`
	Xattr          *XattrChange       `json:"xattr,omitempty"`
	XattrRepair    *XattrRepairChange `json:"xattr_repair,omitempty"`
	XattrMigration *XattrMigration    `json:"xattr_migration,omitempty"`
}

// Leaves returns the changes in execution order, presenting a singleton as a
// one-element slice so authorization and plugin middleware can inspect both
// forms uniformly. Callers must treat the returned values as immutable.
func (cs ChangeSet) Leaves() []Change {
	if len(cs.Changes) != 0 {
		out := append([]Change(nil), cs.Changes...)
		for i := range out {
			out[i].Xattr = cloneXattr(out[i].Xattr)
			out[i].XattrRepair = cloneRepair(out[i].XattrRepair)
			out[i].XattrMigration = cloneMigration(out[i].XattrMigration)
		}
		return out
	}
	return []Change{{Target: cs.Target, Action: cs.Action, Write: cs.Write, RemoveAll: cs.RemoveAll, Xattr: cloneXattr(cs.Xattr), XattrRepair: cloneRepair(cs.XattrRepair), XattrMigration: cloneMigration(cs.XattrMigration)}}
}

// ValidateChangeSet rejects empty, mixed, and malformed singleton/batch values.
func ValidateChangeSet(cs ChangeSet) error {
	if len(cs.Changes) != 0 {
		if cs.Target != "" || cs.Action != "" || cs.Write != nil || cs.RemoveAll != nil || cs.Xattr != nil || cs.XattrRepair != nil || cs.XattrMigration != nil {
			return fmt.Errorf("changeset: batch cannot contain singleton fields")
		}
		for i, change := range cs.Changes {
			if err := validateChange(change); err != nil {
				return fmt.Errorf("changeset leaf %d: %w", i, err)
			}
		}
		seenFrom, seenTo := map[int]bool{}, map[int]bool{}
		for i, move := range cs.Moves {
			if move.From < 0 || move.From >= len(cs.Changes) || move.To < 0 || move.To >= len(cs.Changes) || move.From == move.To {
				return fmt.Errorf("changeset move %d: invalid leaf indices", i)
			}
			from, to := cs.Changes[move.From], cs.Changes[move.To]
			if from.Action != ChangeActionRemoveAll || from.RemoveAll == nil || to.Action != ChangeActionWrite || to.Write == nil {
				return fmt.Errorf("changeset move %d: from must be remove_all and to must be write", i)
			}
			if from.RemoveAll.Opts.Expected == nil || snapshotFileHash(from.RemoveAll.Opts.Expected) != hashBytes(to.Write.Bytes) {
				return fmt.Errorf("changeset move %d: source snapshot must match destination content", i)
			}
			if seenFrom[move.From] || seenTo[move.To] {
				return fmt.Errorf("changeset move %d: leaf is paired more than once", i)
			}
			seenFrom[move.From], seenTo[move.To] = true, true
		}
		return nil
	}
	if len(cs.Moves) != 0 {
		return fmt.Errorf("changeset: singleton cannot contain moves")
	}
	return validateChange(Change{Target: cs.Target, Action: cs.Action, Write: cs.Write, RemoveAll: cs.RemoveAll, Xattr: cs.Xattr, XattrRepair: cs.XattrRepair, XattrMigration: cs.XattrMigration})
}

func snapshotFileHash(snapshot *TreeSnapshot) string {
	for _, op := range snapshot.Ops {
		if op.RelPath == "." && op.Kind == "file" {
			return op.Hash
		}
	}
	return ""
}

func hashBytes(content []byte) string {
	hash := sha256.Sum256(content)
	return fmt.Sprintf("%x", hash)
}

func validateChange(c Change) error {
	if c.Target == "" {
		return fmt.Errorf("missing target")
	}
	payloads := 0
	if c.Write != nil {
		payloads++
	}
	if c.RemoveAll != nil {
		payloads++
	}
	if c.Xattr != nil {
		payloads++
	}
	if c.XattrRepair != nil {
		payloads++
	}
	if c.XattrMigration != nil {
		payloads++
	}
	switch c.Action {
	case ChangeActionWrite:
		if c.Write == nil || payloads != 1 {
			return fmt.Errorf("write requires only write payload")
		}
	case ChangeActionRemoveAll:
		if c.RemoveAll == nil || payloads != 1 {
			return fmt.Errorf("remove_all requires only remove_all payload")
		}
	case ChangeActionMkdir, ChangeActionMkdirAll, ChangeActionRemove:
		if payloads != 0 {
			return fmt.Errorf("%s accepts no payload", c.Action)
		}
	case ChangeActionSetXattr:
		if c.Xattr == nil || c.Xattr.Name == "" || !c.Xattr.Flags.Valid() || payloads != 1 {
			return fmt.Errorf("set_xattr requires a valid xattr payload")
		}
	case ChangeActionRemoveXattr:
		if c.Xattr == nil || c.Xattr.Name == "" || len(c.Xattr.Value) != 0 || c.Xattr.Flags != 0 || payloads != 1 {
			return fmt.Errorf("remove_xattr requires a name-only xattr payload")
		}
	case ChangeActionPreserveAndRecreateXattrs:
		if c.XattrRepair == nil || c.Write != nil || c.RemoveAll != nil || c.Xattr != nil || c.XattrMigration != nil {
			return fmt.Errorf("preserve_and_recreate_xattrs requires only repair payload")
		}
	case ChangeActionMigrateXattrs:
		if c.XattrMigration == nil || c.XattrMigration.NamespacePrefix == "" || len(c.XattrMigration.ExpectedEnvelopeSHA256) != 32 || len(c.XattrMigration.Edits) == 0 || c.Write != nil || c.RemoveAll != nil || c.Xattr != nil || c.XattrRepair != nil {
			return fmt.Errorf("migrate_xattrs requires valid migration payload")
		}
	default:
		return fmt.Errorf("unknown action %q", c.Action)
	}
	return nil
}

type XattrChange struct {
	Name  string     `json:"name"`
	Value []byte     `json:"value,omitempty"`
	Flags XattrFlags `json:"flags,omitempty"`
}
type XattrRepairChange struct {
	Attributes map[string][]byte `json:"attributes"`
}

func cloneRepair(r *XattrRepairChange) *XattrRepairChange {
	if r == nil {
		return nil
	}
	c := &XattrRepairChange{Attributes: map[string][]byte{}}
	for k, v := range r.Attributes {
		c.Attributes[k] = append([]byte(nil), v...)
	}
	return c
}
func cloneMigration(m *XattrMigration) *XattrMigration {
	if m == nil {
		return nil
	}
	c := *m
	c.ExpectedEnvelopeSHA256 = append([]byte(nil), m.ExpectedEnvelopeSHA256...)
	c.Edits = append([]XattrEdit(nil), m.Edits...)
	for i := range c.Edits {
		c.Edits[i].Value = append([]byte(nil), m.Edits[i].Value...)
	}
	return &c
}

func cloneXattr(x *XattrChange) *XattrChange {
	if x == nil {
		return nil
	}
	c := *x
	c.Value = append([]byte(nil), x.Value...)
	return &c
}

// WriteChange is the write payload of a ChangeSet: the exact proposed bytes plus
// the precondition contract they commit under. Opts is the caller's own
// WriteOpts, carried verbatim so a replay is byte-for-byte the write the caller
// requested — its zero value is an unconditional (last-write-wins) overwrite,
// an IfMatch pins a compare-and-swap base, and IfNoneMatch is create-only. This
// preserves every write mode through the log and through an approval hold.
type WriteChange struct {
	Bytes []byte    `json:"bytes"`
	Opts  WriteOpts `json:"opts"`
}

// RemoveAllChange is the whole-tree removal payload of a ChangeSet: the
// precondition contract the removal commits under, carried verbatim as the
// caller's own RemoveOpts. Its zero value (Expected nil) is an unconditional
// removal; a non-nil Expected snapshot pins the compare-and-swap base captured
// at proposal time.
type RemoveAllChange struct {
	Opts RemoveOpts `json:"opts"`
}

// CommitChangeSet replays cs against fs under the exact precondition contract it
// carries. A write commits its proposed bytes under its WriteOpts
// (unconditional / IfMatch / IfNoneMatch); a remove_all replays its whole-tree
// removal under its RemoveOpts; mkdir / mkdir_all / remove replay their
// namespace mutation. Compare-and-swap drift fails with *PreconditionError
// (write) or *TreeStaleError (remove_all). Before applying a batch, it snapshots
// every affected namespace root. If any leaf fails, prior leaves are rolled
// back before the error is returned.
//
// It performs no authorization and captures no approver — the caller is
// responsible for deciding a ChangeSet may commit before calling this. For a
// singleton write it returns the hex SHA-256 of the committed bytes; every
// other action and every batch returns an empty hash.
type CommitResult struct {
	Hash      string
	Committed ChangeSet
}

// HasCommitted reports whether at least one mutation was durably applied.
func (r CommitResult) HasCommitted() bool {
	return r.Committed.Target != "" || len(r.Committed.Changes) != 0
}

func CommitChangeSet(fs WritableFS, cs ChangeSet) (result CommitResult, err error) {
	if err := ValidateChangeSet(cs); err != nil {
		return CommitResult{}, err
	}
	if len(cs.Changes) > 0 {
		snapshots, err := captureBatch(fs, cs.Changes)
		if err != nil {
			return CommitResult{}, fmt.Errorf("changeset snapshot: %w", err)
		}
		for i, change := range cs.Changes {
			leaf := ChangeSet{Target: change.Target, Action: change.Action, Write: change.Write, RemoveAll: change.RemoveAll, Xattr: cloneXattr(change.Xattr), XattrRepair: cloneRepair(change.XattrRepair), XattrMigration: cloneMigration(change.XattrMigration)}
			if _, err := CommitChangeSet(fs, leaf); err != nil {
				if i == 0 {
					return CommitResult{}, err
				}
				if rollbackErr := restoreBatch(fs, snapshots); rollbackErr != nil {
					return CommitResult{}, &RollbackError{CommitErr: err, RollbackErr: rollbackErr}
				}
				return CommitResult{}, err
			}
		}
		return CommitResult{Committed: cs}, nil
	}
	switch cs.Action {
	case ChangeActionWrite:
		w := cs.Write
		if w == nil {
			return CommitResult{}, fmt.Errorf("changeset %s: missing write payload", cs.Target)
		}
		h, err := fs.WriteFileAtomic(cs.Target, w.Bytes, w.Opts)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Hash: h, Committed: cs}, nil
	case ChangeActionMkdir:
		err := fs.Mkdir(cs.Target)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Committed: cs}, nil
	case ChangeActionMkdirAll:
		err := fs.MkdirAll(cs.Target)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Committed: cs}, nil
	case ChangeActionRemove:
		err := fs.Remove(cs.Target)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Committed: cs}, nil
	case ChangeActionRemoveAll:
		r := cs.RemoveAll
		if r == nil {
			return CommitResult{}, fmt.Errorf("changeset %s: missing remove_all payload", cs.Target)
		}
		err := fs.RemoveAll(cs.Target, r.Opts)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Committed: cs}, nil
	case ChangeActionSetXattr, ChangeActionRemoveXattr:
		xw, ok := fs.(XattrWriter)
		if !ok {
			return CommitResult{}, syscall.ENOTSUP
		}
		if cs.Action == ChangeActionSetXattr {
			err = xw.SetXattr(cs.Target, cs.Xattr.Name, append([]byte(nil), cs.Xattr.Value...), cs.Xattr.Flags)
		} else {
			err = xw.RemoveXattr(cs.Target, cs.Xattr.Name)
		}
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Committed: cs}, nil
	case ChangeActionPreserveAndRecreateXattrs, ChangeActionMigrateXattrs:
		xm, ok := fs.(XattrMaintenance)
		if !ok {
			return CommitResult{}, syscall.ENOTSUP
		}
		if cs.Action == ChangeActionPreserveAndRecreateXattrs {
			err = xm.PreserveAndRecreateXattrs(cs.Target, cloneRepair(cs.XattrRepair).Attributes)
		} else {
			err = xm.MigrateXattrs(cs.Target, *cloneMigration(cs.XattrMigration))
		}
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Committed: cs}, nil
	default:
		return CommitResult{}, fmt.Errorf("changeset %s: unknown action %q", cs.Target, cs.Action)
	}
}

// RollbackError reports both the leaf failure and a failure restoring the
// pre-batch snapshot. Callers can still use errors.Is/As for the commit error.
type RollbackError struct {
	CommitErr   error
	RollbackErr error
}

func (e *RollbackError) Error() string {
	return fmt.Sprintf("batch commit failed: %v; rollback failed: %v", e.CommitErr, e.RollbackErr)
}
func (e *RollbackError) Unwrap() error { return e.CommitErr }

type rollbackEntry struct {
	path   string
	dir    bool
	data   []byte
	xattrs map[string][]byte
}

type rollbackSnapshot struct {
	root    string
	existed bool
	entries []rollbackEntry
}

type rollbackAttrs struct {
	path  string
	attrs map[string][]byte
}

type batchSnapshot struct {
	roots []rollbackSnapshot
	attrs []rollbackAttrs
}

func captureBatch(fsys WritableFS, changes []Change) (batchSnapshot, error) {
	var roots []string
	attrTargets := map[string]bool{}
	for _, change := range changes {
		switch change.Action {
		case ChangeActionWrite, ChangeActionMkdir, ChangeActionMkdirAll, ChangeActionRemove, ChangeActionRemoveAll:
			root, err := highestMissingRoot(fsys, CleanPath(change.Target))
			if err != nil {
				return batchSnapshot{}, err
			}
			roots = append(roots, root)
		case ChangeActionSetXattr, ChangeActionRemoveXattr, ChangeActionPreserveAndRecreateXattrs, ChangeActionMigrateXattrs:
			attrTargets[CleanPath(change.Target)] = true
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if pathDepth(roots[i]) == pathDepth(roots[j]) {
			return roots[i] < roots[j]
		}
		return pathDepth(roots[i]) < pathDepth(roots[j])
	})
	minimal := roots[:0]
	for _, root := range roots {
		covered := false
		for _, parent := range minimal {
			if root == parent || parent == "/" || strings.HasPrefix(root, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			minimal = append(minimal, root)
		}
	}
	snapshot := batchSnapshot{roots: make([]rollbackSnapshot, 0, len(minimal))}
	for _, root := range minimal {
		rootSnapshot, err := captureRoot(fsys, root)
		if err != nil {
			return batchSnapshot{}, err
		}
		snapshot.roots = append(snapshot.roots, rootSnapshot)
	}
	if len(attrTargets) > 0 {
		reader, readOK := fsys.(XattrReader)
		_, restoreOK := fsys.(XattrWriter)
		if !readOK || !restoreOK {
			return batchSnapshot{}, syscall.ENOTSUP
		}
		for target := range attrTargets {
			attrs, err := readXattrs(reader, target)
			if err != nil {
				return batchSnapshot{}, err
			}
			snapshot.attrs = append(snapshot.attrs, rollbackAttrs{path: target, attrs: attrs})
		}
		sort.Slice(snapshot.attrs, func(i, j int) bool { return snapshot.attrs[i].path < snapshot.attrs[j].path })
	}
	return snapshot, nil
}

func highestMissingRoot(fsys FileSystem, target string) (string, error) {
	if _, err := fsys.Stat(target); err == nil {
		return target, nil
	} else if !missingPath(err) {
		return "", err
	}
	root := target
	for parent := path.Dir(root); parent != root; parent = path.Dir(root) {
		if _, err := fsys.Stat(parent); err == nil {
			return root, nil
		} else if !missingPath(err) {
			return "", err
		}
		root = parent
	}
	return root, nil
}

func missingPath(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func captureRoot(fsys FileSystem, root string) (rollbackSnapshot, error) {
	if _, err := fsys.Stat(root); err != nil {
		if missingPath(err) {
			return rollbackSnapshot{root: root}, nil
		}
		return rollbackSnapshot{}, err
	}
	snapshot := rollbackSnapshot{root: root, existed: true}
	err := WalkDir(fsys, root, func(target string, info *FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entry := rollbackEntry{path: target, dir: info.IsDir()}
		if !entry.dir {
			data, err := fsys.ReadFile(target)
			if err != nil {
				return err
			}
			entry.data = append([]byte(nil), data...)
		}
		if reader, ok := fsys.(XattrReader); ok {
			attrs, err := readXattrs(reader, target)
			if err != nil && !errors.Is(err, syscall.ENOTSUP) {
				return err
			}
			if err == nil {
				if len(attrs) > 0 {
					if _, ok := fsys.(XattrWriter); !ok {
						return syscall.ENOTSUP
					}
				}
				entry.xattrs = attrs
			}
		}
		snapshot.entries = append(snapshot.entries, entry)
		return nil
	})
	return snapshot, err
}

func readXattrs(reader XattrReader, target string) (map[string][]byte, error) {
	names, err := reader.ListXattrs(target)
	if err != nil {
		return nil, err
	}
	attrs := map[string][]byte{}
	for _, name := range names {
		value, err := reader.GetXattr(target, name)
		if err != nil {
			return nil, err
		}
		attrs[name] = append([]byte(nil), value...)
	}
	return attrs, nil
}

func restoreBatch(fsys WritableFS, snapshot batchSnapshot) error {
	for i := len(snapshot.roots) - 1; i >= 0; i-- {
		root := snapshot.roots[i].root
		info, err := fsys.Stat(root)
		if err != nil {
			if missingPath(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			if err := fsys.RemoveAll(root, RemoveOpts{}); err != nil {
				return err
			}
		} else if err := fsys.Remove(root); err != nil {
			return err
		}
	}
	for _, rootSnapshot := range snapshot.roots {
		if !rootSnapshot.existed {
			continue
		}
		for _, entry := range rootSnapshot.entries {
			if entry.dir {
				if err := fsys.MkdirAll(entry.path); err != nil {
					return err
				}
				continue
			}
			if _, err := fsys.WriteFileAtomic(entry.path, entry.data, WriteOpts{ContentPolicyValidated: true}); err != nil {
				return err
			}
		}
		if _, ok := fsys.(XattrWriter); ok {
			for _, entry := range rootSnapshot.entries {
				if entry.xattrs != nil {
					if err := restoreXattrs(fsys, entry.path, entry.xattrs); err != nil {
						return err
					}
				}
			}
		}
	}
	if len(snapshot.attrs) > 0 {
		for _, attrs := range snapshot.attrs {
			if err := restoreXattrs(fsys, attrs.path, attrs.attrs); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreXattrs(fsys WritableFS, target string, snapshot map[string][]byte) error {
	reader := fsys.(XattrReader)
	writer := fsys.(XattrWriter)
	current, err := reader.ListXattrs(target)
	if err != nil {
		return err
	}
	for _, name := range current {
		if _, ok := snapshot[name]; !ok {
			if err := writer.RemoveXattr(target, name); err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writer.SetXattr(target, name, snapshot[name], 0); err != nil {
			return err
		}
	}
	return nil
}

func pathDepth(p string) int { return strings.Count(strings.Trim(p, "/"), "/") }

// PendingChangeError is returned by a mutating operation that an admission
// middleware intercepted and handed off instead of committing — for example,
// parked to await human approval. It is NOT a failure: the change was accepted
// as pending, so callers should report it informationally, not as an error.
//
// ChangeSet is the captured (immutable) mutation; Ref is the consumer-owned
// handle for the pending change (e.g. a held-changeset id) that callers surface
// so it can be resolved later.
type PendingChangeError struct {
	ChangeSet ChangeSet
	Ref       string
}

func (e *PendingChangeError) Error() string {
	if e.Ref != "" {
		return fmt.Sprintf("change to %s pending as %s", e.ChangeSet.Target, e.Ref)
	}
	return fmt.Sprintf("change to %s pending", e.ChangeSet.Target)
}
