// Package vfs defines the virtual filesystem contract that the shell, its
// commands, and all backends share. It depends on nothing but the standard
// library: it is the seam between the shell (which operates on a filesystem)
// and the backends (which implement one).
package vfs

import (
	"fmt"
	"io/fs"
	"path"
	"time"
)

// FileInfo represents a file or directory in the virtual filesystem.
type FileInfo struct {
	FileName    string
	FilePath    string
	Content     []byte
	Dir         bool
	FileModTime time.Time
	FileSize    int64
}

func (fi *FileInfo) Name() string       { return fi.FileName }
func (fi *FileInfo) Size() int64        { return fi.FileSize }
func (fi *FileInfo) ModTime() time.Time { return fi.FileModTime }
func (fi *FileInfo) IsDir() bool        { return fi.Dir }
func (fi *FileInfo) Sys() interface{}   { return nil }

func (fi *FileInfo) Mode() fs.FileMode {
	if fi.Dir {
		return fs.ModeDir | 0555
	}
	return 0444
}

// FileSystem is the read-only filesystem interface that all commands operate on.
type FileSystem interface {
	Stat(path string) (*FileInfo, error)
	ReadDir(path string) ([]FileInfo, error)
	ReadFile(path string) ([]byte, error)
}

// WriteConflictPolicy selects how a whole-file overwrite (the `>`, tee, sed -i
// and publish verbs) defends against concurrent writers. It does not affect
// append (`>>`) or patch, which are always compare-and-swap by construction.
type WriteConflictPolicy string

const (
	// PolicyHash (the default) makes overwrites compare-and-swap: the write
	// carries an IfMatch precondition on the base the caller read, so a
	// concurrent change is rejected with a PreconditionError instead of being
	// silently clobbered. For a read-modify-write verb (sed -i) the base is the
	// content it transformed, giving true optimistic concurrency; for a blind
	// redirect the base is read at command time, so the guarantee narrows to
	// the command's own read→write window.
	PolicyHash WriteConflictPolicy = "hash"

	// PolicyLastWriteWins makes overwrites unconditional (the zero WriteOpts):
	// atomic, but the last writer silently wins. Opt-in per docset or globally.
	PolicyLastWriteWins WriteConflictPolicy = "last_write_wins"
)

// DefaultWriteConflictPolicy is the policy applied when none is configured.
const DefaultWriteConflictPolicy = PolicyHash

// ParseWriteConflictPolicy validates and normalizes a policy string. An empty
// string resolves to the default (hash). Unknown values are rejected.
func ParseWriteConflictPolicy(s string) (WriteConflictPolicy, error) {
	switch WriteConflictPolicy(s) {
	case "":
		return DefaultWriteConflictPolicy, nil
	case PolicyHash:
		return PolicyHash, nil
	case PolicyLastWriteWins:
		return PolicyLastWriteWins, nil
	default:
		return "", fmt.Errorf("invalid write_conflict_policy %q (want %q or %q)", s, PolicyHash, PolicyLastWriteWins)
	}
}

// WriteOpts carries the precondition contract for an atomic write. The zero
// value means "unconditional overwrite" (last-write-wins, but still atomic).
//
// Precedence: IfNoneMatch (create-only) is mutually exclusive with IfMatch.
type WriteOpts struct {
	// IfMatch, when non-nil, requires the current content hash of the target
	// to equal *IfMatch before the write commits. A nonexistent target fails
	// (you cannot match a hash that has no bytes). This is the hex SHA-256 of
	// the exact bytes ReadFile returns.
	IfMatch *string

	// IfNoneMatch, when true, requires the target to not already exist
	// (create-only). Fails with a conflict if it does.
	IfNoneMatch bool

	// ContentPolicyValidated is set only by trusted, narrowly scoped ingress
	// adapters after they enforce their own extension/MIME and request limits.
	// Substrates bypass their ordinary extension and per-write size policies for
	// this leaf; admission and precondition checks still apply.
	ContentPolicyValidated bool
}

// WritableFS is the read+write filesystem interface. A backend only satisfies
// it if it can physically persist writes; embedded/read-only backends
// (embed.FS, plain fs.FS adapters) deliberately do not implement it.
//
// Writes are whole-object and atomic by construction — there is no streaming,
// offset, or partial-write surface. The substrate's write capability is a
// stateful flag toggled by SetWriteable/SetReadonly; while read-only every
// mutating call returns an error.
type WritableFS interface {
	FileSystem

	// SetWriteable transitions the backend to writable. It returns an error
	// if the backend can't support writes at all (not if it is already
	// writable — that is an idempotent no-op success).
	SetWriteable() error

	// SetReadonly transitions the backend back to read-only. It is idempotent
	// and drains in-flight writes: a write that already passed its precondition
	// check is allowed to complete, and SetReadonly blocks until the substrate
	// is quiescent before returning.
	SetReadonly() error

	// WriteFileAtomic commits data to name as a single atomic whole-object
	// operation, honoring the WriteOpts precondition. It returns the hex
	// SHA-256 of the committed bytes. It errors when the backend is currently
	// read-only or when the precondition fails.
	WriteFileAtomic(name string, data []byte, opts WriteOpts) (newHash string, err error)

	// Mkdir creates a folder. It uses plain mkdir semantics (the parent must
	// exist) but errors if name is not strictly inside a docset — you cannot
	// create a docset, nor anything at or above a docset root, through the FS.
	Mkdir(name string) error

	// MkdirAll creates a folder and any missing ancestors (mkdir -p). Like
	// Mkdir it errors if name is not strictly inside a docset: the enclosing
	// docset root must already exist, and every folder it creates must sit
	// strictly below that root. An already-existing directory is not an error.
	MkdirAll(name string) error

	// Remove deletes a single file or empty directory at name. It errors if
	// name is a docset root or anything at or above one, and refuses a
	// non-empty directory (use RemoveAll). It participates in the readonly
	// lock like every mutation.
	Remove(name string) error

	// RemoveAll deletes name and everything under it as a single atomic
	// whole-tree operation, honoring opts.Expected as a compare-and-swap
	// precondition on the exact subtree. It errors if name is a docset root or
	// anything at or above one, and refuses the delete if the physical subtree
	// contains any descendant hidden or denied by file policy (so the atomic
	// rename cannot destroy invisible bytes). A mismatch against opts.Expected
	// returns a *TreeStaleError and removes nothing.
	RemoveAll(name string, opts RemoveOpts) error
}

// RemoveOpts carries the precondition contract for an atomic whole-tree delete.
// The zero value (Expected nil) means an unconditional delete.
type RemoveOpts struct {
	// Expected, when non-nil, requires the current visible subtree at the
	// target to match this snapshot exactly before the delete commits. Any
	// drift — a changed file, an added or removed descendant, a target that
	// vanished or changed kind — fails with a *TreeStaleError and deletes
	// nothing.
	Expected *TreeSnapshot
}

// TreeSnapshot is an exact, order-independent description of a subtree, used as
// the compare-and-swap base for an atomic delete. Root is the cleaned target
// path the snapshot was taken at; Ops lists every descendant (files and
// directories) with paths relative to Root.
type TreeSnapshot struct {
	Root string
	Ops  []TreeOp
}

// TreeOp describes one descendant within a TreeSnapshot. RelPath is relative to
// the snapshot Root (the root itself is RelPath "."). Kind is "file" or "dir".
// Hash is the hex SHA-256 of a file's content (empty for a directory); Size is
// the file's byte length (0 for a directory).
type TreeOp struct {
	RelPath string
	Kind    string // "file" | "dir"
	Hash    string
	Size    int64
}

// TreeStaleError is returned by RemoveAll when the current subtree no longer
// matches opts.Expected — the tree drifted since it was snapshotted, so the
// delete was refused. Detail names the first divergence found.
type TreeStaleError struct {
	Path   string
	Detail string
}

func (e *TreeStaleError) Error() string {
	return fmt.Sprintf("tree changed at %s: %s; re-scan and retry", e.Path, e.Detail)
}

// WriteScopeFS is optionally implemented by a session filesystem that confines
// writes to a subset of paths. CanWrite reports whether a write to path would be
// permitted by the session's scope, without performing one — used for fail-fast
// checks (e.g. `spawn` validating its target before scheduling a job). It says
// nothing about content preconditions or approval gating, only scope.
type WriteScopeFS interface {
	CanWrite(path string) bool
}

// PathCanonicalizer is optionally implemented by filesystems that expose more
// than one virtual path for the same file. Commands use it when path identity
// matters, such as avoiding a move from an alias onto its canonical path.
type PathCanonicalizer interface {
	CanonicalPath(path string) string
}

// ReadTracker is optionally implemented by a session filesystem that remembers
// the content hash of every file read during the session. It lets the write
// seam compare-and-swap a whole-file overwrite against the version the caller
// last saw — without the caller naming a hash — so an overwrite fails if the
// file changed since it was last read. A successful write updates the tracked
// hash so repeated writes in one session chain correctly.
type ReadTracker interface {
	// LastReadHash returns the hex SHA-256 recorded when path was last read
	// (or written) in this session, and whether such a record exists.
	LastReadHash(path string) (hash string, seen bool)
}

// ErrReadOnly is returned by mutating operations when the substrate is in
// read-only mode.
var ErrReadOnly = fmt.Errorf("read-only filesystem")

// PreconditionError is returned when a WriteOpts precondition (IfMatch /
// IfNoneMatch) is not satisfied. Current is the hash of the existing bytes
// (empty if the target does not exist).
type PreconditionError struct {
	Path    string
	Current string
}

func (e *PreconditionError) Error() string {
	if e.Current == "" {
		return fmt.Sprintf("precondition failed: %s does not exist; re-read and retry", e.Path)
	}
	return fmt.Sprintf("precondition failed: %s current hash %s; re-read and retry", e.Path, e.Current)
}

// WalkDir walks the filesystem tree rooted at root, calling fn for each file or directory.
func WalkDir(fsys FileSystem, root string, fn func(path string, info *FileInfo, err error) error) error {
	root = CleanPath(root)
	f, err := fsys.Stat(root)
	if err != nil {
		return fn(root, nil, err)
	}
	if err := fn(root, f, nil); err != nil {
		return err
	}
	if !f.Dir {
		return nil
	}
	entries, err := fsys.ReadDir(root)
	if err != nil {
		return fn(root, nil, err)
	}
	for _, entry := range entries {
		childPath := path.Join(root, entry.FileName)
		if err := WalkDir(fsys, childPath, fn); err != nil {
			return err
		}
	}
	return nil
}

// CleanPath normalises a path to always start with / and removes . and .. segments.
func CleanPath(p string) string {
	p = path.Clean("/" + p)
	if p == "." {
		p = "/"
	}
	return p
}

// Dir is a convenience constructor for a directory FileInfo.
func Dir(p string, modTime time.Time) *FileInfo {
	return &FileInfo{
		FileName:    path.Base(p),
		FilePath:    p,
		Dir:         true,
		FileModTime: modTime,
	}
}

// File is a convenience constructor for a file FileInfo.
func File(name, filePath string, content []byte, modTime time.Time) *FileInfo {
	return &FileInfo{
		FileName:    name,
		FilePath:    filePath,
		Content:     content,
		Dir:         false,
		FileModTime: modTime,
		FileSize:    int64(len(content)),
	}
}

// ErrNotFound is returned when a path does not exist.
func ErrNotFound(p string) error {
	return fmt.Errorf("not found: %s", p)
}

// ErrIsDirectory is returned when a file operation is attempted on a directory.
func ErrIsDirectory(p string) error {
	return fmt.Errorf("is a directory: %s", p)
}

// ErrNotDirectory is returned when a directory operation is attempted on a file.
func ErrNotDirectory(p string) error {
	return fmt.Errorf("not a directory: %s", p)
}
