package openlore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
)

type fileHistory struct{ path string }

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
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record CommitRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if principal != "" && record.Attribution.Principal != principal {
			continue
		}
		if actor != "" && record.Attribution.Actor != actor {
			continue
		}
		line, _ := json.Marshal(struct {
			Time        string `json:"time"`
			Attribution string `json:"attribution"`
			Target      string `json:"target"`
			Action      string `json:"action"`
			Hash        string `json:"hash,omitempty"`
		}{record.Time.Format("2006-01-02T15:04:05Z07:00"), record.Attribution.String(), record.ChangeSet.Target, string(record.ChangeSet.Action), record.Hash})
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), scanner.Err()
}
