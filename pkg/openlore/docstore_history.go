package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// docstoreJournal runs inside the ordered applier, before it acknowledges a
// durable write. Like the existing journal, this is provenance after the file
// commit, not a transaction with the filesystem substrate.
type docstoreJournal struct {
	server  *Server
	journal CommitJournal
}

func (j docstoreJournal) Append(ctx context.Context, record CommitRecord) error {
	ids, err := j.server.snapshotNativeCommit(ctx, record)
	record.DocstoreSnapshots = ids
	if j.server.agentSkills != nil {
		err = errors.Join(err, j.server.agentSkills.reconcileInstallations(&record.ChangeSet))
	}
	// Never lose the base commit record when snapshot storage fails.
	return errors.Join(err, j.journal.Append(ctx, record))
}
func nativeFilePath(ds config.DocsetSpec, p string) string {
	if len(ds.Paths) == 1 {
		root := displayPath(ds.Paths[0])
		return strings.TrimPrefix(strings.TrimPrefix(vfs.CleanPath(p), strings.TrimSuffix(root, "/")+"/"), "/")
	}
	// Multiple roots keep canonical VFS paths to avoid basename collisions.
	return strings.TrimPrefix(vfs.CleanPath(p), "/")
}
func (s *Server) snapshotNativeCommit(ctx context.Context, record CommitRecord) (map[string]string, error) {
	docstoreMu.Lock()
	defer docstoreMu.Unlock()
	ids := map[string]string{}
	affected := map[string]config.DocsetSpec{}
	for _, leaf := range record.ChangeSet.Leaves() {
		name, ds, ok := s.mostSpecificDocset(leaf.Target)
		if ok && ds.Projection == nil {
			affected[name] = ds
		}
	}
	for name, ds := range affected {
		store := "openlore/" + name
		state, err := s.docstore.state(store)
		if err != nil {
			return ids, err
		}
		previous := latestSnapshot(state)
		files := map[string]SourceFile{}
		for p, f := range previous.Files {
			files[p] = f
		}
		storeFile := func(p string, b []byte) error {
			hash := hashContent(b)
			if err := s.docstore.blobs.Put(ctx, hash, bytes.NewReader(b)); err != nil {
				return err
			}
			files[nativeFilePath(ds, p)] = SourceFile{SHA256: hash, Size: int64(len(b)), Mode: 0644}
			return nil
		}
		capture := func(p string) error {
			info, err := s.merge.Stat(p)
			if err != nil {
				return err
			}
			if info.Dir {
				return nil
			}
			b, err := s.merge.ReadFile(p)
			if err != nil {
				return err
			}
			return storeFile(p, b)
		}
		if previous.Snapshot == "" {
			var walk func(string) error
			walk = func(p string) error {
				for _, system := range s.merge.SystemMountPaths() {
					if pathWithinRoot(system, p) {
						return nil
					}
				}
				governing, _, ok := s.mostSpecificDocset(p)
				if !ok || governing != name {
					return nil
				}
				info, err := s.merge.Stat(p)
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				if err != nil {
					return err
				}
				if !info.Dir {
					return capture(p)
				}
				entries, err := s.merge.ReadDir(p)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if err := walk(path.Join(p, entry.FileName)); err != nil {
						return err
					}
				}
				return nil
			}
			for _, mapping := range ds.Paths {
				if err := walk(displayPath(mapping)); err != nil {
					return ids, fmt.Errorf("snapshot %s: %w", store, err)
				}
			}
		} else {
			for _, leaf := range record.ChangeSet.Leaves() {
				governing, _, ok := s.mostSpecificDocset(leaf.Target)
				if !ok || governing != name {
					continue
				}
				p := nativeFilePath(ds, leaf.Target)
				switch leaf.Action {
				case vfs.ChangeActionRemove, vfs.ChangeActionRemoveAll:
					for existing := range files {
						if existing == p || strings.HasPrefix(existing, p+"/") {
							delete(files, existing)
						}
					}
				case vfs.ChangeActionWrite:
					if err := storeFile(leaf.Target, leaf.Write.Bytes); err != nil {
						return ids, err
					}
				}
			}
		}
		req := SnapshotRequest{Files: files, Parent: previous.Snapshot, Meta: map[string]string{"type": "openlore", "origin": "server filesystem", "commit": record.ID, "docset": name}}
		// Native writes have already passed the server's own admission limits.
		// Do not reinterpret them as client uploads with a different size policy.
		b, _ := json.Marshal(struct{ Store, Commit string }{store, record.ID})
		id := hashContent(b)
		if state.Owner == "" {
			state.Owner = "openlore"
		}
		state.Pending[id] = SourceSnapshot{SnapshotRequest: req, Snapshot: id}
		if err := s.docstore.save(state); err != nil {
			return ids, err
		}
		snap, err := s.docstore.commit(ctx, store, "openlore", id)
		if err != nil {
			return ids, err
		}
		ids[store] = snap.Snapshot
	}
	return ids, nil
}
