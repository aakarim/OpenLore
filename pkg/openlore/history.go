package openlore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type fileHistory struct {
	path  string
	roots []string
}

type fileHistoryEntry struct {
	Time        time.Time
	Attribution string
	Action      string
	Hash        string
}

func historyRoots(docsets []cmds.DocsetInfo) []string {
	var roots []string
	for _, docset := range docsets {
		roots = append(roots, docset.Paths...)
	}
	return roots
}

func (h fileHistory) readable(target string) bool {
	for _, root := range h.roots {
		if pathWithinRoot(root, target) {
			return true
		}
	}
	return false
}

func (h fileHistory) Query(principal, actor string) ([]byte, error) {
	f, err := os.Open(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out bytes.Buffer
	decoder := json.NewDecoder(f)
	for {
		var record CommitRecord
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if principal != "" && record.Attribution.Principal != principal {
			continue
		}
		if actor != "" && record.Attribution.Actor != actor {
			continue
		}
		type change struct {
			Target string `json:"target"`
			Action string `json:"action"`
		}
		var changes []change
		for _, leaf := range record.ChangeSet.Leaves() {
			if h.readable(leaf.Target) {
				changes = append(changes, change{Target: leaf.Target, Action: string(leaf.Action)})
			}
		}
		if len(changes) == 0 {
			continue
		}
		line, _ := json.Marshal(struct {
			Time        string   `json:"time"`
			Attribution string   `json:"attribution"`
			Changes     []change `json:"changes"`
			Hash        string   `json:"hash,omitempty"`
		}{record.Time.Format("2006-01-02T15:04:05Z07:00"), record.Attribution.String(), changes, record.Hash})
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func (h fileHistory) QueryFile(target string) ([]fileHistoryEntry, error) {
	target = vfs.CleanPath(target)
	if !h.readable(target) {
		return nil, nil
	}
	f, err := os.Open(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []fileHistoryEntry
	decoder := json.NewDecoder(f)
	for {
		var record CommitRecord
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		for _, leaf := range record.ChangeSet.Leaves() {
			if vfs.CleanPath(leaf.Target) != target {
				continue
			}
			entries = append(entries, fileHistoryEntry{
				Time: record.Time, Attribution: record.Attribution.String(), Action: string(leaf.Action), Hash: record.Hash,
			})
		}
	}
	// Commit records are append-only (oldest first); the sidebar is most useful
	// with the latest edit at the top.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}
