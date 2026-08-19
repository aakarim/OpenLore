package openlore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestHistoryHandlesLargeBatchesAndFiltersUnreadableLeaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commits.jsonl")
	recorder := NewJSONLCommitRecorder(path)
	record := CommitRecord{
		Time: time.Now().UTC(), Attribution: Attribution{Principal: "alice", Actor: "agent"}, Hash: "hash",
		ChangeSet: vfs.ChangeSet{Changes: []vfs.Change{
			{Target: "/allowed/large.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte(strings.Repeat("x", 100_000))}},
			{Target: "/secret/hidden.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("secret")}},
		}},
	}
	if err := recorder.RecordCommit(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	b, err := (fileHistory{path: path, roots: []string{"/allowed"}}).Query("alice", "agent")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Changes []struct {
			Target string `json:"target"`
			Action string `json:"action"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Target != "/allowed/large.md" || result.Changes[0].Action != string(vfs.ChangeActionWrite) {
		t.Fatalf("filtered history=%s", b)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatalf("history leaked unreadable path: %s", b)
	}
}
