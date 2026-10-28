package lore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore"
)

// ScanFolder hashes the files a source selects under its path. stage also
// copies them into the store's local cache under .local.
func (r Repository) ScanFolder(s config.SourceSpec, store string, stage bool) (map[string]openlore.SourceFile, map[string][]byte, error) {
	root, err := expand(s.Path, r.Root)
	if err != nil {
		return nil, nil, err
	}
	files := map[string]openlore.SourceFile{}
	blobs := map[string][]byte{}
	var group agentGroup
	var profile agentProfile
	if s.Type == "claude" {
		group, _, _, err = r.claudeLocation(s, "")
		if err != nil {
			return nil, nil, err
		}
		profile = claudeProfile()
	}
	cache := filepath.Join(r.sourceDir(store), "files")
	if stage {
		if err := os.RemoveAll(cache); err != nil {
			return nil, nil, err
		}
	}
	var total int64
	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p != root && (e.Name() == ".git" || e.Name() == ".local") {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if s.Type == "claude" {
			if !config.SourcePathSelected(group.Detect, nil, rel) || !config.SourcePathSelected(nil, profile.AlwaysExclude, strings.ToLower(rel)) {
				return nil
			}
		}
		if !config.SourcePathSelected(s.Include, s.Exclude, rel) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Size() > 8<<20 {
			return fmt.Errorf("%s exceeds 8 MiB", rel)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(b) > 8<<20 {
			return fmt.Errorf("%s exceeds 8 MiB", rel)
		}
		total += int64(len(b))
		if total > 128<<20 || len(files) >= 10000 {
			return fmt.Errorf("source exceeds snapshot limit (128 MiB / 10000 files)")
		}
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])
		files[rel] = openlore.SourceFile{SHA256: hash, Size: int64(len(b)), Mode: uint32(info.Mode().Perm())}
		blobs[hash] = b
		if stage {
			return atomicFile(filepath.Join(cache, filepath.FromSlash(rel)), b, 0600)
		}
		return nil
	})
	return files, blobs, err
}

// ScanSource resolves where a source's files live and scans them; store is
// the source's StorePath, which names its local cache.
func (r Repository) ScanSource(s config.SourceSpec, store string, stage bool) (map[string]openlore.SourceFile, map[string][]byte, error) {
	switch s.Type {
	case "folder":
		return r.ScanFolder(s, store, stage)
	case "git":
		repo, err := r.RefreshGit(s, store)
		if err != nil {
			return nil, nil, err
		}
		s.Path = repo
		return r.ScanFolder(s, store, stage)
	case "claude":
		_, base, _, err := r.claudeLocation(s, "")
		if err != nil {
			return nil, nil, err
		}
		s.Path = base
		return r.ScanFolder(s, store, stage)
	default:
		return nil, nil, fmt.Errorf("unsupported source type %s", s.Type)
	}
}

// DiffFiles counts the paths added, changed and removed between two scans.
func DiffFiles(before, after map[string]openlore.SourceFile) (added, changed, removed int) {
	for p, f := range after {
		old, ok := before[p]
		if !ok {
			added++
		} else if old != f {
			changed++
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			removed++
		}
	}
	return
}
