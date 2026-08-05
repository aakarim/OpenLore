package vfs

import (
	"fmt"
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
// or an ordered batch of mutations. Batch leaves commit in order without
// rollback; their aggregate committed hash is empty.
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
		return nil
	}
	return validateChange(Change{Target: cs.Target, Action: cs.Action, Write: cs.Write, RemoveAll: cs.RemoveAll, Xattr: cs.Xattr, XattrRepair: cs.XattrRepair, XattrMigration: cs.XattrMigration})
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
// (write) or *TreeStaleError (remove_all). A batch is ordered and has no
// rollback: leaves committed before a later failure remain committed.
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
		committed := make([]Change, 0, len(cs.Changes))
		for _, change := range cs.Changes {
			leaf := ChangeSet{Target: change.Target, Action: change.Action, Write: change.Write, RemoveAll: change.RemoveAll, Xattr: cloneXattr(change.Xattr), XattrRepair: cloneRepair(change.XattrRepair), XattrMigration: cloneMigration(change.XattrMigration)}
			if _, err := CommitChangeSet(fs, leaf); err != nil {
				return CommitResult{Committed: ChangeSet{Changes: committed}}, err
			}
			committed = append(committed, change)
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
