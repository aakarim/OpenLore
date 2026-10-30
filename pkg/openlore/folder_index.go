package openlore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Folder indexes opt in solely through one ordered pair of lore:index markers
// in index.md. Only direct-child, case-sensitive *.md files are listed (not
// index.md itself). Entries sort by filename; parseable frontmatter supplies
// title and description, and files without it appear under Unindexed. Missing
// titles fall back to filenames. No recursive depth/grouping configuration is
// implied. All bytes outside the marker contents are retained verbatim.
//
// Expansion precedes ALL admission middleware. Generated writes carry CAS
// preconditions, so concurrent or deferred stale projections fail atomically
// rather than lose an entry. Callers may retry the complete original operation.
// No post-commit write or direct substrate mutation is used. CommitChangeSet is
// deliberately not expanded: it replays already-admitted, possibly held batches.
func (s *Server) folderIndexAdmission(next WriteHandler) WriteHandler {
	return func(ctx context.Context, op WriteOp) (WriteResult, error) {
		cs := s.canonicalChangeSet(op.persistenceChangeSet())
		if err := vfs.ValidateChangeSet(cs); err != nil {
			return WriteResult{}, err
		}
		// Use the caller's read view, not the unscoped substrate: an index must
		// never disclose documents the initiating identity cannot read.
		var readView vfs.FileSystem = s.merge
		if op.identity != nil {
			readView = s.buildCanonicalRawSessionFS(*op.identity)
		}
		expanded, err := expandFolderIndexes(readView, cs)
		if err != nil {
			return WriteResult{}, err
		}
		for _, leaf := range expanded.Leaves() {
			if op.identity != nil && !s.identityCanWrite(*op.identity, leaf.Action, leaf.Target) {
				return WriteResult{}, mutationDeniedError(s.merge, leaf.Action, leaf.Target)
			}
		}
		op.changeSet = cloneWriteChangeSet(expanded)
		result, err := next(ctx, op)
		// The log returns no aggregate hash for a batch. Preserve the original
		// single-file API's hash contract, including a regenerated index write.
		if err == nil && len(cs.Changes) == 0 && cs.Action == vfs.ChangeActionWrite {
			data := cs.Write.Bytes
			for _, leaf := range expanded.Leaves() {
				if leaf.Target == cs.Target && leaf.Action == vfs.ChangeActionWrite {
					data = leaf.Write.Bytes
				}
			}
			result.Hash = folderIndexHash(data)
		}
		return result, err
	}
}

func folderIndexHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// Validate again at the serialized pre-apply seam. In particular an index may
// have opted in after an older operation passed admission. Reject that stale
// operation rather than silently commit content without its materialized index.
// This is read-only and never calls admission or submits to the log.
func validateFolderIndexes(fsys vfs.FileSystem, cs vfs.ChangeSet) error {
	expanded, err := expandFolderIndexes(fsys, cs)
	if err != nil {
		return err
	}
	original := cs.Leaves()
	finalIndexes := map[string][]byte{}
	for _, leaf := range expanded.Leaves() {
		if path.Base(leaf.Target) == "index.md" && leaf.Action == vfs.ChangeActionWrite {
			finalIndexes[leaf.Target] = leaf.Write.Bytes
		}
	}
	for target, desired := range finalIndexes {
		var current []byte
		written := false
		for _, leaf := range original {
			if leaf.Target == target && leaf.Action == vfs.ChangeActionWrite {
				current = leaf.Write.Bytes
				written = true
			}
		}
		if !written {
			current, err = fsys.ReadFile(target)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if !bytes.Equal(current, desired) {
			return fmt.Errorf("folder index %s changed since admission; resubmit the original operation", target)
		}
	}
	return nil
}

func expandFolderIndexes(fsys vfs.FileSystem, cs vfs.ChangeSet) (vfs.ChangeSet, error) {
	leaves := cs.Leaves()
	changed := false
	dirs := map[string]bool{}
	for _, leaf := range leaves {
		if path.Ext(leaf.Target) == ".md" && (leaf.Action == vfs.ChangeActionWrite || leaf.Action == vfs.ChangeActionRemove || leaf.Action == vfs.ChangeActionRemoveAll) {
			dirs[path.Dir(leaf.Target)] = true
		}
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Strings(ordered)
	for _, dir := range ordered {
		indexPath := path.Join(dir, "index.md")
		base, err := fsys.ReadFile(indexPath)
		exists := err == nil
		// An unreadable neighboring index is not an opt-in signal. Do not
		// change admission for ordinary documents merely because it exists.
		// The serialized validator still rejects stale opted-in projections.
		if errors.Is(err, fs.ErrPermission) {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return vfs.ChangeSet{}, err
		}
		index := base
		lastWrite := -1
		removed := false
		for i, leaf := range leaves {
			if (leaf.Action == vfs.ChangeActionRemoveAll && pathWithinRoot(leaf.Target, indexPath)) || (leaf.Target == indexPath && leaf.Action == vfs.ChangeActionRemove) {
				index, removed, lastWrite = nil, true, -1
			}
			if leaf.Target == indexPath && leaf.Action == vfs.ChangeActionWrite {
				index, removed, lastWrite = leaf.Write.Bytes, false, i
			}
		}
		start, end, enabled, err := folderIndexMarkers(index)
		if err != nil {
			return vfs.ChangeSet{}, fmt.Errorf("%s: %w", indexPath, err)
		}
		if !enabled || removed {
			continue
		}
		entries, err := fsys.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return vfs.ChangeSet{}, err
		}
		documents := map[string][]byte{}
		for _, entry := range entries {
			if entry.IsDir() || path.Ext(entry.Name()) != ".md" || entry.Name() == "index.md" {
				continue
			}
			data, err := fsys.ReadFile(path.Join(dir, entry.Name()))
			if err != nil {
				return vfs.ChangeSet{}, err
			}
			documents[entry.Name()] = data
		}
		for _, leaf := range leaves {
			if leaf.Action == vfs.ChangeActionRemoveAll && pathWithinRoot(leaf.Target, dir) {
				clear(documents)
			}
			if path.Dir(leaf.Target) != dir || path.Base(leaf.Target) == "index.md" || path.Ext(leaf.Target) != ".md" {
				continue
			}
			switch leaf.Action {
			case vfs.ChangeActionWrite:
				documents[path.Base(leaf.Target)] = leaf.Write.Bytes
			case vfs.ChangeActionRemove, vfs.ChangeActionRemoveAll:
				delete(documents, path.Base(leaf.Target))
			}
		}
		generated := append([]byte(nil), index[:start]...)
		generated = append(generated, renderFolderIndex(documents)...)
		generated = append(generated, index[end:]...)
		// No maintenance event for an unchanged projection. The serialized
		// validator still rejects a projection made stale after admission.
		if lastWrite < 0 && bytes.Equal(index, generated) {
			continue
		}
		// An explicit index write must also guard the base used to discover
		// siblings. Keep caller-provided CAS contracts; narrow unconditional
		// overwrites to CAS so they cannot overwrite a concurrent projection.
		if lastWrite >= 0 {
			write := *leaves[lastWrite].Write
			if write.Opts.IfMatch == nil && !write.Opts.IfNoneMatch {
				if exists {
					hash := folderIndexHash(base)
					write.Opts.IfMatch = &hash
				} else {
					write.Opts.IfNoneMatch = true
				}
			}
			leaves[lastWrite].Write = &write
			changed = true // retain destination CAS even if a move needs no refresh
			// Usually replace the explicit index write with its final bytes so
			// rules/baselines see one write, not an obsolete intermediate leaf.
			// A move's destination must retain its source content hash, however;
			// preserve that leaf and append the generated write in that case.
			moved := false
			for _, move := range cs.Moves {
				moved = moved || move.To == lastWrite
			}
			if !moved {
				write.Bytes = generated
				changed = true
				continue
			}
		}
		if bytes.Equal(index, generated) {
			continue
		}
		hash := folderIndexHash(index)
		leaves = append(leaves, vfs.Change{Target: indexPath, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: generated, Opts: vfs.WriteOpts{IfMatch: &hash}, Generated: true}})
		changed = true
	}
	if !changed {
		return cs, nil
	}
	if len(cs.Changes) == 0 && len(leaves) == 1 {
		cs.Write = leaves[0].Write
		return cs, nil
	}
	return vfs.ChangeSet{Changes: leaves, Moves: cs.Moves}, nil
}

func folderIndexMarkers(data []byte) (start, end int, enabled bool, err error) {
	const open, close = "<!-- lore:index -->", "<!-- /lore:index -->"
	a, b := bytes.Index(data, []byte(open)), bytes.Index(data, []byte(close))
	if a < 0 && b < 0 {
		return 0, 0, false, nil
	}
	if a < 0 || b < a+len(open) || bytes.Count(data, []byte(open)) != 1 || bytes.Count(data, []byte(close)) != 1 {
		return 0, 0, false, fmt.Errorf("expected exactly one ordered lore:index marker pair")
	}
	return a + len(open), b, true, nil
}

func renderFolderIndex(documents map[string][]byte) []byte {
	names := make([]string, 0, len(documents))
	for name := range documents {
		names = append(names, name)
	}
	sort.Strings(names)
	var indexed, unindexed strings.Builder
	for _, name := range names {
		fm, _, hasFM, err := okf.ParseFrontmatter(documents[name])
		title, _ := fm["title"].(string)
		description, _ := fm["description"].(string)
		if strings.TrimSpace(title) == "" {
			title = name
		}
		out := &indexed
		if !hasFM || err != nil {
			out = &unindexed
		}
		link := url.PathEscape(name)
		if strings.Contains(name, ":") {
			link = "./" + link // a filename must never become a URL scheme
		}
		fmt.Fprintf(out, "- [%s](%s)", folderIndexText(title), link)
		if strings.TrimSpace(description) != "" {
			fmt.Fprintf(out, " — %s", folderIndexText(description))
		}
		out.WriteByte('\n')
	}
	result := "\n" + indexed.String()
	if unindexed.Len() != 0 {
		result += "\n## Unindexed\n\n" + unindexed.String()
	}
	return []byte(result)
}

func folderIndexText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(s)
}
