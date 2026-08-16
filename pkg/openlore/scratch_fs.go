package openlore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// scratchFS is a session-local writable filesystem. It deliberately lives
// outside the durable write log and docset authorization layers: its contents
// are private to one session and disappear when that session filesystem does.
type scratchFS struct {
	mu      sync.RWMutex
	entries map[string]scratchEntry
}

type scratchEntry struct {
	data    []byte
	dir     bool
	modTime time.Time
}

func newScratchFS() *scratchFS {
	return &scratchFS{entries: map[string]scratchEntry{
		"/": {dir: true, modTime: time.Now()},
	}}
}

func (s *scratchFS) SetWriteable() error { return nil }
func (s *scratchFS) SetReadonly() error  { return nil }

func (s *scratchFS) Stat(name string) (*vfs.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clean := vfs.CleanPath(name)
	entry, ok := s.entries[clean]
	if !ok {
		return nil, vfs.ErrNotFound(clean)
	}
	return scratchFileInfo(clean, entry), nil
}

func (s *scratchFS) ReadDir(name string) ([]vfs.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clean := vfs.CleanPath(name)
	entry, ok := s.entries[clean]
	if !ok {
		return nil, vfs.ErrNotFound(clean)
	}
	if !entry.dir {
		return nil, vfs.ErrNotDirectory(clean)
	}
	var result []vfs.FileInfo
	for child, childEntry := range s.entries {
		if child == clean || path.Dir(child) != clean {
			continue
		}
		result = append(result, *scratchFileInfo(child, childEntry))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].FileName < result[j].FileName })
	return result, nil
}

func (s *scratchFS) ReadFile(name string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clean := vfs.CleanPath(name)
	entry, ok := s.entries[clean]
	if !ok {
		return nil, vfs.ErrNotFound(clean)
	}
	if entry.dir {
		return nil, vfs.ErrIsDirectory(clean)
	}
	return append([]byte(nil), entry.data...), nil
}

func (s *scratchFS) WriteFileAtomic(name string, data []byte, opts vfs.WriteOpts) (string, error) {
	if !opts.ContentPolicyValidated && len(data) > defaultMaxWriteBytes {
		return "", fmt.Errorf("write rejected: %d bytes exceeds limit of %d", len(data), defaultMaxWriteBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := vfs.CleanPath(name)
	if clean == "/" {
		return "", vfs.ErrIsDirectory(clean)
	}
	parent, ok := s.entries[path.Dir(clean)]
	if !ok || !parent.dir {
		return "", vfs.ErrNotDirectory(path.Dir(clean))
	}
	current, exists := s.entries[clean]
	if exists && current.dir {
		return "", vfs.ErrIsDirectory(clean)
	}
	currentHash := ""
	if exists {
		currentHash = scratchHash(current.data)
	}
	if opts.IfNoneMatch && exists {
		return "", &vfs.PreconditionError{Path: clean, Current: currentHash}
	}
	if opts.IfMatch != nil && (!exists || currentHash != *opts.IfMatch) {
		return "", &vfs.PreconditionError{Path: clean, Current: currentHash}
	}
	content := append([]byte(nil), data...)
	s.entries[clean] = scratchEntry{data: content, modTime: time.Now()}
	return scratchHash(content), nil
}

func (s *scratchFS) Mkdir(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mkdir(vfs.CleanPath(name))
}

func (s *scratchFS) MkdirAll(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := vfs.CleanPath(name)
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		if entry, ok := s.entries[current]; ok {
			if !entry.dir {
				return vfs.ErrNotDirectory(current)
			}
			continue
		}
		if err := s.mkdir(current); err != nil {
			return err
		}
	}
	return nil
}

func (s *scratchFS) mkdir(clean string) error {
	if _, exists := s.entries[clean]; exists {
		return fmt.Errorf("file exists: %s", clean)
	}
	parent, ok := s.entries[path.Dir(clean)]
	if !ok || !parent.dir {
		return vfs.ErrNotDirectory(path.Dir(clean))
	}
	s.entries[clean] = scratchEntry{dir: true, modTime: time.Now()}
	return nil
}

func (s *scratchFS) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := vfs.CleanPath(name)
	entry, ok := s.entries[clean]
	if !ok {
		return vfs.ErrNotFound(clean)
	}
	if entry.dir {
		prefix := strings.TrimSuffix(clean, "/") + "/"
		for candidate := range s.entries {
			if strings.HasPrefix(candidate, prefix) {
				return fmt.Errorf("directory not empty: %s", clean)
			}
		}
	}
	delete(s.entries, clean)
	return nil
}

func (s *scratchFS) RemoveAll(name string, opts vfs.RemoveOpts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := vfs.CleanPath(name)
	if clean == "/" {
		return fmt.Errorf("cannot remove scratch root")
	}
	if _, ok := s.entries[clean]; !ok {
		return vfs.ErrNotFound(clean)
	}
	if opts.Expected != nil {
		actual := s.snapshot(clean)
		if detail, equal := snapshotsEqual(opts.Expected, actual); !equal {
			return &vfs.TreeStaleError{Path: clean, Detail: detail}
		}
	}
	prefix := strings.TrimSuffix(clean, "/") + "/"
	for candidate := range s.entries {
		if candidate == clean || strings.HasPrefix(candidate, prefix) {
			delete(s.entries, candidate)
		}
	}
	return nil
}

func (s *scratchFS) snapshot(root string) *vfs.TreeSnapshot {
	snapshot := &vfs.TreeSnapshot{Root: root}
	prefix := strings.TrimSuffix(root, "/") + "/"
	for name, entry := range s.entries {
		if name != root && !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if name == root {
			rel = "."
		}
		op := vfs.TreeOp{RelPath: rel, Kind: "file", Size: int64(len(entry.data))}
		if entry.dir {
			op.Kind = "dir"
		} else {
			op.Hash = scratchHash(entry.data)
		}
		snapshot.Ops = append(snapshot.Ops, op)
	}
	return snapshot
}

func scratchFileInfo(name string, entry scratchEntry) *vfs.FileInfo {
	if entry.dir {
		return vfs.Dir(name, entry.modTime)
	}
	return vfs.File(path.Base(name), name, append([]byte(nil), entry.data...), entry.modTime)
}

func scratchHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var _ vfs.WritableFS = (*scratchFS)(nil)
