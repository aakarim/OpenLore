package lore

import (
	"fmt"
	"slices"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/lore/api"
	"github.com/aakarim/go-openlore/pkg/openlore"
)

// SyncResult reports what one source sync found and did.
type SyncResult struct {
	Files                   int
	Added, Changed, Removed int
	// Missing counts the blobs the server lacked.
	Missing  int
	Snapshot string
	// Unchanged means the server already held exactly these files.
	Unchanged bool
}

// SelectSources returns the definitions named in names, in file order, or
// every definition when names is empty.
func SelectSources(sources config.SourcesFile, names []string) ([]config.SourceSpec, error) {
	for _, name := range names {
		found := false
		for _, s := range sources.Sources {
			if s.Name == name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown source %q", name)
		}
	}
	var selected []config.SourceSpec
	for _, s := range sources.Sources {
		if len(names) > 0 && !slices.Contains(names, s.Name) {
			continue
		}
		selected = append(selected, s)
	}
	return selected, nil
}

// SyncSource pushes a source to store, or with dry only reports the changes,
// recording the resulting snapshot in local.
func (r Repository) SyncSource(client *api.Client, s config.SourceSpec, store string, dry bool, local *IdentityState) (SyncResult, error) {
	files, blobs, err := r.ScanSource(s, store, !dry)
	if err != nil {
		return SyncResult{}, err
	}
	remote, err := client.Stores()
	if err != nil {
		return SyncResult{}, err
	}
	var latest openlore.SourceSnapshot
	for _, rs := range remote {
		if rs.Store == store {
			latest = rs.Latest
		}
	}
	res := SyncResult{Files: len(files)}
	res.Added, res.Changed, res.Removed = DiffFiles(latest.Files, files)
	if !dry && res.Added+res.Changed+res.Removed == 0 && latest.Snapshot != "" {
		local.Snapshots[s.Name] = latest.Snapshot
		res.Snapshot, res.Unchanged = latest.Snapshot, true
		return res, nil
	}
	req := openlore.SnapshotRequest{Files: files, Parent: latest.Snapshot, Meta: map[string]string{"name": s.Name, "type": s.Type, "origin": Origin(s)}}
	pending, err := client.PrepareSnapshot(store, req, dry)
	if err != nil {
		return SyncResult{}, err
	}
	res.Missing = len(pending.Missing)
	if dry {
		return res, nil
	}
	for _, hash := range pending.Missing {
		blob, ok := blobs[hash]
		if !ok {
			return SyncResult{}, fmt.Errorf("server requested an unknown blob")
		}
		if err = client.UploadBlob(hash, blob); err != nil {
			return SyncResult{}, err
		}
	}
	committed, err := client.CommitSnapshot(store, pending.Snapshot)
	if err != nil {
		return SyncResult{}, err
	}
	local.Snapshots[s.Name] = committed.Snapshot
	res.Snapshot = committed.Snapshot
	return res, nil
}
