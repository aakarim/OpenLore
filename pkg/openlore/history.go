package openlore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

type fileHistory struct {
	path  string
	roots []string
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
