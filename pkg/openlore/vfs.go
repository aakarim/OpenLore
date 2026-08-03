package openlore

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// DirFS serves files from a real directory on disk. It is the reference
// vfs.WritableFS implementation.
//
// Write capability is a stateful flag (the substrate-wide readonly lock). A
// freshly constructed DirFS is read-only; call SetWriteable to enable writes.
// WriteFileAtomic commits whole objects via temp-file + fsync + rename(2)
// (POSIX atomic swap). Post-commit reactions run through the server's
// post-commit middleware chain, not a fan-out bus.
type DirFS struct {
	root  string
	files config.FilesConfig

	// docsetRoots are the logical paths (relative to this DirFS root) that are
	// docset boundaries for Mkdir: a folder may only be created strictly below
	// one of them. Empty means the whole DirFS is treated as a single docset
	// (any non-root path is allowed).
	docsetRoots []string

	// stateMu guards the writeable flag and drains in-flight writes. Writers
	// take RLock for the duration of a mutation; SetReadonly takes the
	// exclusive Lock, which blocks until in-flight writers release (drain).
	stateMu   sync.RWMutex
	writeable bool

	// commitMu serializes the precondition check-and-swap so concurrent writers
	// to the same DirFS never interleave their read-current → check → rename.
	commitMu sync.Mutex

	// maxWriteBytes caps a single atomic write; 0 means use the default.
	maxWriteBytes int64
}

// defaultMaxWriteBytes bounds a single buffered atomic write (knowledge
// objects are small).
const defaultMaxWriteBytes = 8 << 20 // 8 MiB

// NewDirFS creates a new (read-only) DirFS rooted at the given directory.
func NewDirFS(root string, files config.FilesConfig) *DirFS {
	return &DirFS{root: root, files: files}
}

// WithMaxWriteBytes sets the substrate cap for one atomic write. A zero value
// retains the 8 MiB default. Configure before sharing the DirFS.
func (d *DirFS) WithMaxWriteBytes(max int64) *DirFS {
	d.maxWriteBytes = max
	return d
}

// WithDocsetRoots sets the Mkdir boundary to the given logical docset roots — a
// folder may only be created strictly below one of them — and returns the
// receiver for chaining. Configure before the DirFS is shared across
// goroutines.
func (d *DirFS) WithDocsetRoots(roots []string) *DirFS {
	cleaned := make([]string, 0, len(roots))
	for _, r := range roots {
		cleaned = append(cleaned, vfs.CleanPath(r))
	}
	d.docsetRoots = cleaned
	return d
}

// SetWriteable transitions the substrate to writable. Idempotent. It also
// sweeps any staging tree left behind by a delete interrupted by a crash.
func (d *DirFS) SetWriteable() error {
	d.stateMu.Lock()
	d.writeable = true
	d.stateMu.Unlock()
	d.sweepTrash()
	return nil
}

// SetReadonly transitions the substrate back to read-only, draining in-flight
// writes first (the exclusive lock blocks until current writers release).
// Idempotent.
func (d *DirFS) SetReadonly() error {
	d.stateMu.Lock()
	d.writeable = false
	d.stateMu.Unlock()
	return nil
}

// WriteFileAtomic commits content to p as a single atomic object. The
// precondition (opts) is checked under the same lock that guards the commit, so
// the read-current → check → swap sequence is atomic. Returns the hex SHA-256
// of the committed bytes.
func (d *DirFS) WriteFileAtomic(p string, content []byte, opts vfs.WriteOpts) (string, error) {
	max := d.maxWriteBytes
	if max == 0 {
		max = defaultMaxWriteBytes
	}
	if !opts.ContentPolicyValidated && int64(len(content)) > max {
		return "", fmt.Errorf("write rejected: %d bytes exceeds limit of %d", len(content), max)
	}
	if isTrashPath(vfs.CleanPath(p)) {
		return "", fmt.Errorf("access denied: %s", p)
	}
	if !opts.ContentPolicyValidated && !isAllowed(path.Base(p), d.files) {
		return "", fmt.Errorf("access denied: %s", p)
	}
	if isIgnored(p, d.files) {
		return "", fmt.Errorf("access denied: %s", p)
	}

	// Hold RLock for the whole mutation: it permits concurrent writers but
	// blocks SetReadonly from completing until we release (drain semantics).
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return "", vfs.ErrReadOnly
	}

	full := d.resolve(p)

	// Precondition check + commit must be atomic with respect to other writers
	// to the same DirFS. A single mutex serializes the check-and-swap.
	d.commitMu.Lock()
	defer d.commitMu.Unlock()

	if opts.IfMatch != nil || opts.IfNoneMatch {
		cur, exists, err := currentHash(full)
		if err != nil {
			return "", err
		}
		if opts.IfNoneMatch && exists {
			return "", &vfs.PreconditionError{Path: vfs.CleanPath(p), Current: cur}
		}
		if opts.IfMatch != nil {
			if !exists || cur != *opts.IfMatch {
				return "", &vfs.PreconditionError{Path: vfs.CleanPath(p), Current: cur}
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := atomicWrite(full, content); err != nil {
		return "", err
	}

	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	return hash, nil
}

// Mkdir creates a folder at p using plain mkdir semantics (the parent must
// exist). It errors if p is not strictly below a docset root.
func (d *DirFS) Mkdir(p string) error {
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return fmt.Errorf("cannot create docset root: %s", p)
	}
	if isTrashPath(clean) || isIgnored(p, d.files) {
		return fmt.Errorf("access denied: %s", p)
	}
	if !d.insideDocset(clean) {
		return fmt.Errorf("cannot create folder outside a docset: %s", p)
	}

	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return vfs.ErrReadOnly
	}

	full := d.resolve(p)
	if err := os.Mkdir(full, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return nil
}

// insideDocset reports whether the cleaned logical path sits strictly below a
// docset root. With no docset roots configured, the whole DirFS is one docset,
// so any non-root path qualifies.
func (d *DirFS) insideDocset(clean string) bool {
	if len(d.docsetRoots) == 0 {
		return clean != "/"
	}
	// A nested docset root remains protected even when another docset (such as
	// "/") contains it.
	for _, root := range d.docsetRoots {
		if clean == root {
			return false
		}
	}
	for _, root := range d.docsetRoots {
		if root == "/" {
			if clean != "/" {
				return true
			}
			continue
		}
		if strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

// trashDirName is the hardcoded-hidden staging root (relative to the DirFS
// root) that RemoveAll renames doomed subtrees into before destroying them. It
// is suppressed from every read path so it never appears in the VFS or in a
// snapshot walk.
const trashDirName = ".openlore-trash"

// isTrashPath reports whether a cleaned logical path is (or is inside) the
// hidden staging root.
func isTrashPath(clean string) bool {
	return clean == "/"+trashDirName || strings.HasPrefix(clean, "/"+trashDirName+"/")
}

// docsetRootFor returns the docset root that clean sits strictly below, and
// whether one exists. With no docset roots configured the whole DirFS is one
// docset rooted at "/" (which always exists).
func (d *DirFS) docsetRootFor(clean string) (string, bool) {
	if len(d.docsetRoots) == 0 {
		if clean != "/" {
			return "/", true
		}
		return "", false
	}
	best := ""
	for _, root := range d.docsetRoots {
		if root == "/" {
			if clean != "/" && best == "" {
				best = "/"
			}
			continue
		}
		if strings.HasPrefix(clean, root+"/") && len(root) > len(best) {
			best = root
		}
	}
	return best, best != ""
}

// materializeDir creates physical upper-layer scaffolding after an overlay has
// already established that the virtual directory is valid and visible.
func (d *DirFS) materializeDir(p string) error {
	clean := vfs.CleanPath(p)
	if isTrashPath(clean) || isIgnored(clean, d.files) {
		return fmt.Errorf("access denied: %s", p)
	}
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return vfs.ErrReadOnly
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()
	return os.MkdirAll(d.resolve(clean), 0o755)
}

// MkdirAll creates p and any missing ancestors (mkdir -p). The enclosing docset
// root must already exist — MkdirAll will not create a docset root — and every
// folder it creates sits strictly below that root. An existing directory is a
// no-op success.
func (d *DirFS) MkdirAll(p string) error {
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return fmt.Errorf("cannot create docset root: %s", p)
	}
	if isTrashPath(clean) || isIgnored(p, d.files) {
		return fmt.Errorf("access denied: %s", p)
	}
	root, ok := d.docsetRootFor(clean)
	if !ok {
		return fmt.Errorf("cannot create folder outside a docset: %s", p)
	}

	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return vfs.ErrReadOnly
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()

	// The docset root must exist on disk so MkdirAll only creates strictly
	// below it (never a docset root itself).
	if root != "/" {
		info, err := os.Stat(d.resolve(root))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("docset root does not exist: %s", root)
		}
	}
	if err := os.MkdirAll(d.resolve(p), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return nil
}

// Remove deletes a single file or empty directory at p. It refuses a docset
// root (or anything at/above one) and a non-empty directory.
func (d *DirFS) Remove(p string) error {
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return fmt.Errorf("cannot delete docset root: %s", p)
	}
	if isTrashPath(clean) || isIgnored(p, d.files) {
		return fmt.Errorf("access denied: %s", p)
	}
	if !d.insideDocset(clean) {
		return fmt.Errorf("cannot delete outside a docset: %s", p)
	}

	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return vfs.ErrReadOnly
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()

	full := d.resolve(p)
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	if !info.IsDir() && !isAllowed(info.Name(), d.files) {
		return fmt.Errorf("access denied: %s", p)
	}
	if err := os.Remove(full); err != nil {
		return err
	}
	return nil
}

// RemoveAll deletes p and everything under it atomically. It snapshots the raw
// physical subtree (refusing the delete if it holds any file/dir hidden by file
// policy), enforces opts.Expected as an exact compare-and-swap, then renames the
// subtree into the hidden staging root (atomic visibility) and destroys the
// staged copy synchronously.
func (d *DirFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return fmt.Errorf("cannot delete docset root: %s", p)
	}
	if isTrashPath(clean) || isIgnored(p, d.files) {
		return fmt.Errorf("access denied: %s", p)
	}
	if !d.insideDocset(clean) {
		return fmt.Errorf("cannot delete outside a docset: %s", p)
	}

	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return vfs.ErrReadOnly
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()

	full := d.resolve(p)
	if _, err := os.Stat(full); err != nil {
		return err
	}

	// Snapshot the physical subtree and refuse if it hides anything from the
	// VFS view (so the atomic rename cannot destroy invisible bytes). After
	// this passes, the physical subtree equals the visible subtree, making the
	// compare-and-swap against opts.Expected meaningful.
	snap, err := d.rawSnapshot(clean, full)
	if err != nil {
		return err
	}
	if opts.Expected != nil {
		if detail, equal := snapshotsEqual(opts.Expected, snap); !equal {
			return &vfs.TreeStaleError{Path: clean, Detail: detail}
		}
	}

	trashRoot := filepath.Join(d.root, trashDirName)
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return fmt.Errorf("prepare staging: %w", err)
	}
	staged := filepath.Join(trashRoot, randomToken())
	if err := os.Rename(full, staged); err != nil {
		return fmt.Errorf("stage delete: %w", err)
	}
	// The subtree is now invisible at its original path; destroy the staged
	// copy synchronously (best-effort — the namespace change already committed).
	_ = os.RemoveAll(staged)
	return nil
}

// rawSnapshot walks the physical subtree at full (logical root clean) into an
// exact TreeSnapshot with RelPaths relative to clean. It returns an error if any
// descendant is hidden or denied by file policy.
func (d *DirFS) rawSnapshot(clean, full string) (*vfs.TreeSnapshot, error) {
	snap := &vfs.TreeSnapshot{Root: clean}
	err := filepath.WalkDir(full, func(fp string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rerr := filepath.Rel(full, fp)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		logical := clean
		if rel != "." {
			logical = path.Join(clean, rel)
		}
		if entry.IsDir() {
			if rel != "." && isIgnored(logical, d.files) {
				return fmt.Errorf("refusing delete: %s contains hidden directory %s", clean, logical)
			}
			snap.Ops = append(snap.Ops, vfs.TreeOp{RelPath: rel, Kind: "dir"})
			return nil
		}
		if !isAllowed(entry.Name(), d.files) || isIgnored(logical, d.files) {
			return fmt.Errorf("refusing delete: %s contains hidden file %s", clean, logical)
		}
		data, rerr := os.ReadFile(fp)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		snap.Ops = append(snap.Ops, vfs.TreeOp{
			RelPath: rel,
			Kind:    "file",
			Hash:    hex.EncodeToString(sum[:]),
			Size:    int64(len(data)),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// snapshotsEqual reports whether got matches want exactly (same set of paths,
// kinds, and file hashes), returning a human-readable detail on the first
// divergence.
func snapshotsEqual(want, got *vfs.TreeSnapshot) (string, bool) {
	wm := make(map[string]vfs.TreeOp, len(want.Ops))
	for _, op := range want.Ops {
		wm[op.RelPath] = op
	}
	gm := make(map[string]vfs.TreeOp, len(got.Ops))
	for _, op := range got.Ops {
		gm[op.RelPath] = op
	}
	for rel, wop := range wm {
		gop, ok := gm[rel]
		if !ok {
			return fmt.Sprintf("%s was removed", rel), false
		}
		if gop.Kind != wop.Kind {
			return fmt.Sprintf("%s changed kind", rel), false
		}
		if wop.Kind == "file" && gop.Hash != wop.Hash {
			return fmt.Sprintf("%s changed content", rel), false
		}
	}
	for rel := range gm {
		if _, ok := wm[rel]; !ok {
			return fmt.Sprintf("%s was added", rel), false
		}
	}
	return "", true
}

// sweepTrash removes any leftover staging tree from a crashed delete. Safe to
// call when the staging root does not exist.
func (d *DirFS) sweepTrash() {
	_ = os.RemoveAll(filepath.Join(d.root, trashDirName))
}

func (d *DirFS) resolve(p string) string {
	p = path.Clean("/" + p)
	return filepath.Join(d.root, filepath.FromSlash(p))
}

// currentHash returns the hex SHA-256 of the bytes currently at full, and
// whether the file exists. A directory is treated as nonexistent for hashing.
func currentHash(full string) (hash string, exists bool, err error) {
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		// A directory read returns an error; treat as "exists, no hash".
		if info, statErr := os.Stat(full); statErr == nil && info.IsDir() {
			return "", true, nil
		}
		return "", false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true, nil
}

// atomicWrite writes content to a temp file in the destination directory,
// fsyncs it, then atomically renames it into place (POSIX atomic swap).
func atomicWrite(full string, content []byte) error {
	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(full)+"-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (d *DirFS) Stat(p string) (*vfs.FileInfo, error) {
	if isTrashPath(vfs.CleanPath(p)) {
		return nil, os.ErrNotExist
	}
	full := d.resolve(p)
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() && !isAllowed(info.Name(), d.files) {
		return nil, fmt.Errorf("access denied: %s", p)
	}

	if info.IsDir() && isIgnored(p, d.files) {
		return nil, fmt.Errorf("access denied: %s", p)
	}

	return &vfs.FileInfo{
		FileName:    info.Name(),
		FilePath:    vfs.CleanPath(p),
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
		Dir:         info.IsDir(),
	}, nil
}

func (d *DirFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	if isTrashPath(vfs.CleanPath(p)) {
		return nil, os.ErrNotExist
	}
	if isIgnored(p, d.files) {
		return nil, fmt.Errorf("access denied: %s", p)
	}

	full := d.resolve(p)
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}

	var result []vfs.FileInfo
	for _, e := range entries {
		childPath := path.Join(p, e.Name())
		if isTrashPath(vfs.CleanPath(childPath)) {
			continue
		}
		if e.IsDir() {
			if isIgnored(childPath, d.files) {
				continue
			}
		} else {
			if !isAllowed(e.Name(), d.files) {
				continue
			}
		}

		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, vfs.FileInfo{
			FileName:    e.Name(),
			FilePath:    vfs.CleanPath(childPath),
			FileSize:    info.Size(),
			FileModTime: info.ModTime(),
			Dir:         e.IsDir(),
		})
	}
	return result, nil
}

func (d *DirFS) ReadFile(p string) ([]byte, error) {
	if isTrashPath(vfs.CleanPath(p)) {
		return nil, os.ErrNotExist
	}
	if !isAllowed(path.Base(p), d.files) {
		return nil, fmt.Errorf("access denied: %s", p)
	}

	full := d.resolve(p)
	return os.ReadFile(full)
}

// MergeFS merges multiple filesystems under named mount points.
// An optional root filesystem serves content directly at "/".
type MergeFS struct {
	root   vfs.FileSystem
	mounts map[string]vfs.FileSystem
	// system marks mount names that are control-plane (e.g. "requests") rather
	// than lore docsets. System mounts are always readable regardless of an
	// identity's RBAC policy (see Server.readableRoots / SystemMountPaths).
	system map[string]bool
}

// NewMergeFS creates an empty MergeFS.
func NewMergeFS() *MergeFS {
	return &MergeFS{
		mounts: make(map[string]vfs.FileSystem),
		system: make(map[string]bool),
	}
}

// SetRoot sets the root filesystem that serves content at "/".
func (m *MergeFS) SetRoot(fs vfs.FileSystem) {
	m.root = fs
}

// Mount adds a filesystem under the given name.
func (m *MergeFS) Mount(name string, fs vfs.FileSystem) {
	m.mounts[name] = fs
}

// MountSystem adds a control-plane mount that is preserved across FilteredView
// for every session (it is not a lore docset). Used for /requests.
func (m *MergeFS) MountSystem(name string, fs vfs.FileSystem) {
	m.mounts[name] = fs
	m.system[name] = true
}

// SystemMountPaths returns the display paths ("/name") of every control-plane
// (system) mount. These are always readable regardless of an identity's roles,
// so the read-scoping layer includes them.
func (m *MergeFS) SystemMountPaths() []string {
	var out []string
	for name := range m.system {
		out = append(out, "/"+name)
	}
	return out
}

// mountPaths returns every named mount root, including control-plane mounts.
// Alias validation uses it to prevent aliases from shadowing mounted backends.
func (m *MergeFS) mountPaths() []string {
	var out []string
	for name := range m.mounts {
		out = append(out, "/"+name)
	}
	return out
}

// SetWriteable fans out to every writable-capable backend (root + mounts).
// Read-only backends (EmbedFS, FSAdapter) are skipped. It fails fast if no
// backend can be made writable at all (e.g. a fully embedded, read-only
// distribution), so a misconfigured readonly=false is rejected at startup.
func (m *MergeFS) SetWriteable() error {
	var enabled int
	if w, ok := m.root.(vfs.WritableFS); ok {
		if err := w.SetWriteable(); err != nil {
			return err
		}
		enabled++
	}
	for _, fsys := range m.mounts {
		if w, ok := fsys.(vfs.WritableFS); ok {
			if err := w.SetWriteable(); err != nil {
				return err
			}
			enabled++
		}
	}
	if enabled == 0 {
		return fmt.Errorf("%w: no writable backend (cannot enable writes)", vfs.ErrReadOnly)
	}
	return nil
}

// SetReadonly fans out to every writable-capable backend, draining in-flight
// writes on each.
func (m *MergeFS) SetReadonly() error {
	return m.fanout(func(w vfs.WritableFS) error { return w.SetReadonly() })
}

func (m *MergeFS) fanout(fn func(vfs.WritableFS) error) error {
	if w, ok := m.root.(vfs.WritableFS); ok {
		if err := fn(w); err != nil {
			return err
		}
	}
	for _, fsys := range m.mounts {
		if w, ok := fsys.(vfs.WritableFS); ok {
			if err := fn(w); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteFileAtomic routes the write to the resolved mount (or root). It errors
// if the path resolves to the merge root itself or to a read-only backend.
func (m *MergeFS) WriteFileAtomic(p string, content []byte, opts vfs.WriteOpts) (string, error) {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return "", err
	}
	if fsys == nil {
		return "", fmt.Errorf("cannot write to filesystem root: %s", p)
	}
	w, ok := fsys.(vfs.WritableFS)
	if !ok {
		return "", fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
	}
	return w.WriteFileAtomic(subPath, content, opts)
}

// Mkdir routes the folder creation to the resolved mount. Creating a docset
// (the merge root, or a mount root) is not allowed.
func (m *MergeFS) Mkdir(p string) error {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return err
	}
	if fsys == nil {
		return fmt.Errorf("cannot create docset at filesystem root: %s", p)
	}
	if vfs.CleanPath(subPath) == "/" {
		return fmt.Errorf("cannot create docset root: %s", p)
	}
	w, ok := fsys.(vfs.WritableFS)
	if !ok {
		return fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
	}
	return w.Mkdir(subPath)
}

// MkdirAll routes recursive folder creation to the resolved mount. Creating a
// docset (the merge root, or a mount root) is not allowed.
func (m *MergeFS) MkdirAll(p string) error {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return err
	}
	if fsys == nil {
		return fmt.Errorf("cannot create docset at filesystem root: %s", p)
	}
	if vfs.CleanPath(subPath) == "/" {
		return fmt.Errorf("cannot create docset root: %s", p)
	}
	w, ok := fsys.(vfs.WritableFS)
	if !ok {
		return fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
	}
	return w.MkdirAll(subPath)
}

// Remove routes a single-file/empty-dir delete to the resolved mount. Deleting
// the merge root or a mount root is not allowed.
func (m *MergeFS) Remove(p string) error {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return err
	}
	if fsys == nil {
		return fmt.Errorf("cannot delete filesystem root: %s", p)
	}
	if vfs.CleanPath(subPath) == "/" {
		return fmt.Errorf("cannot delete docset root: %s", p)
	}
	w, ok := fsys.(vfs.WritableFS)
	if !ok {
		return fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
	}
	return w.Remove(subPath)
}

// RemoveAll routes a whole-tree delete to the resolved mount. A changeset spans
// exactly one writable backend; deleting the merge root or a mount root is not
// allowed. The Expected snapshot is passed through unchanged (its RelPaths are
// relative to the target, so they are mount-agnostic).
func (m *MergeFS) RemoveAll(p string, opts vfs.RemoveOpts) error {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return err
	}
	if fsys == nil {
		return fmt.Errorf("cannot delete filesystem root: %s", p)
	}
	if vfs.CleanPath(subPath) == "/" {
		return fmt.Errorf("cannot delete docset root: %s", p)
	}
	w, ok := fsys.(vfs.WritableFS)
	if !ok {
		return fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
	}
	return w.RemoveAll(subPath, opts)
}

func (m *MergeFS) resolve(p string) (string, vfs.FileSystem, error) {
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")

	if p == "" || p == "." {
		return "", nil, nil // root listing
	}

	// Check named mounts first
	parts := strings.SplitN(p, "/", 2)
	mountName := parts[0]

	if fsys, ok := m.mounts[mountName]; ok {
		subPath := "/"
		if len(parts) > 1 {
			subPath = "/" + parts[1]
		}
		return subPath, fsys, nil
	}

	// Fall back to root filesystem
	if m.root != nil {
		return "/" + p, m.root, nil
	}

	return "", nil, fmt.Errorf("not found: %s", p)
}

func (m *MergeFS) Stat(p string) (*vfs.FileInfo, error) {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return nil, err
	}

	// Root directory
	if fsys == nil {
		return &vfs.FileInfo{
			FileName: "/",
			FilePath: "/",
			Dir:      true,
		}, nil
	}

	return fsys.Stat(subPath)
}

func (m *MergeFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return nil, err
	}

	// Root: merge root FS entries with mount points
	if fsys == nil {
		var entries []vfs.FileInfo
		if m.root != nil {
			rootEntries, err := m.root.ReadDir("/")
			if err == nil {
				entries = append(entries, rootEntries...)
			}
		}
		for name := range m.mounts {
			entries = append(entries, vfs.FileInfo{
				FileName: name,
				FilePath: "/" + name,
				Dir:      true,
			})
		}
		return entries, nil
	}

	return fsys.ReadDir(subPath)
}

func (m *MergeFS) ReadFile(p string) ([]byte, error) {
	subPath, fsys, err := m.resolve(p)
	if err != nil {
		return nil, err
	}

	if fsys == nil {
		return nil, fmt.Errorf("cannot read directory")
	}

	return fsys.ReadFile(subPath)
}

// EmbedFS serves files from an embed.FS.
type EmbedFS struct {
	fs    embed.FS
	root  string
	files config.FilesConfig
}

// NewEmbedFS creates a new EmbedFS.
func NewEmbedFS(efs embed.FS, root string, files config.FilesConfig) *EmbedFS {
	return &EmbedFS{fs: efs, root: root, files: files}
}

func (e *EmbedFS) resolve(p string) string {
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return e.root
	}
	return path.Join(e.root, p)
}

func (e *EmbedFS) Stat(p string) (*vfs.FileInfo, error) {
	full := e.resolve(p)
	f, err := e.fs.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	if !info.IsDir() && !isAllowed(info.Name(), e.files) {
		return nil, fmt.Errorf("access denied: %s", p)
	}

	return &vfs.FileInfo{
		FileName:    info.Name(),
		FilePath:    vfs.CleanPath(p),
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
		Dir:         info.IsDir(),
	}, nil
}

func (e *EmbedFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	full := e.resolve(p)
	entries, err := e.fs.ReadDir(full)
	if err != nil {
		return nil, err
	}

	var result []vfs.FileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			childPath := path.Join(p, entry.Name())
			if isIgnored(childPath, e.files) {
				continue
			}
		} else {
			if !isAllowed(entry.Name(), e.files) {
				continue
			}
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, vfs.FileInfo{
			FileName:    entry.Name(),
			FilePath:    vfs.CleanPath(path.Join(p, entry.Name())),
			FileSize:    info.Size(),
			FileModTime: info.ModTime(),
			Dir:         entry.IsDir(),
		})
	}
	return result, nil
}

func (e *EmbedFS) ReadFile(p string) ([]byte, error) {
	if !isAllowed(path.Base(p), e.files) {
		return nil, fmt.Errorf("access denied: %s", p)
	}

	full := e.resolve(p)
	return e.fs.ReadFile(full)
}

// isAllowed checks if a filename matches the allowed patterns (and is not denied).
// If no allowed patterns are configured, all files are allowed.
func isAllowed(name string, files config.FilesConfig) bool {
	// Check denied first
	for _, pattern := range files.Denied {
		if matched, _ := path.Match(pattern, name); matched {
			return false
		}
	}

	// If no allowed patterns, everything is allowed
	if len(files.Allowed) == 0 {
		return true
	}

	for _, pattern := range files.Allowed {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}

	return false
}

// isIgnored checks if a path matches any ignore patterns.
func isIgnored(p string, files config.FilesConfig) bool {
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")

	for _, pattern := range files.Ignore {
		if matched, _ := path.Match(pattern, p); matched {
			return true
		}
		if matched, _ := path.Match(pattern, path.Base(p)); matched {
			return true
		}
		parts := strings.Split(p, "/")
		for i := range parts {
			segment := strings.Join(parts[:i+1], "/")
			if matched, _ := path.Match(pattern, segment); matched {
				return true
			}
		}
	}
	return false
}

// FSAdapter adapts a standard fs.FS to the vfs.FileSystem interface.
type FSAdapter struct {
	fsys fs.FS
}

// NewFSAdapter creates a new FSAdapter.
func NewFSAdapter(fsys fs.FS) *FSAdapter {
	return &FSAdapter{fsys: fsys}
}

func (a *FSAdapter) Stat(p string) (*vfs.FileInfo, error) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		p = "."
	}
	info, err := fs.Stat(a.fsys, p)
	if err != nil {
		return nil, err
	}
	return &vfs.FileInfo{
		FileName:    info.Name(),
		FilePath:    vfs.CleanPath("/" + p),
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
		Dir:         info.IsDir(),
	}, nil
}

func (a *FSAdapter) ReadDir(p string) ([]vfs.FileInfo, error) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		p = "."
	}
	entries, err := fs.ReadDir(a.fsys, p)
	if err != nil {
		return nil, err
	}
	var result []vfs.FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, vfs.FileInfo{
			FileName:    e.Name(),
			FilePath:    vfs.CleanPath("/" + path.Join(p, e.Name())),
			FileSize:    info.Size(),
			FileModTime: info.ModTime(),
			Dir:         e.IsDir(),
		})
	}
	return result, nil
}

func (a *FSAdapter) ReadFile(p string) ([]byte, error) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		p = "."
	}
	return fs.ReadFile(a.fsys, p)
}

// Ensure all types implement vfs.FileSystem; DirFS and MergeFS are writable.
// EmbedFS and FSAdapter are deliberately read-only (no WritableFS).
var (
	_ vfs.FileSystem = (*DirFS)(nil)
	_ vfs.FileSystem = (*MergeFS)(nil)
	_ vfs.FileSystem = (*EmbedFS)(nil)
	_ vfs.FileSystem = (*FSAdapter)(nil)
	_ vfs.WritableFS = (*DirFS)(nil)
	_ vfs.WritableFS = (*MergeFS)(nil)
)
