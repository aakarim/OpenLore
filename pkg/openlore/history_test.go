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

func TestHistoryQueriesOneFileNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commits.jsonl")
	recorder := NewJSONLCommitRecorder(path)
	for i, record := range []CommitRecord{
		{Time: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice"}, Hash: "old", ChangeSet: vfs.ChangeSet{Target: "/allowed/note.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("old")}}},
		{Time: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice", Actor: "agent"}, Hash: "other", ChangeSet: vfs.ChangeSet{Target: "/allowed/other.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("other")}}},
		{Time: time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice", Actor: "agent"}, Hash: "new", ChangeSet: vfs.ChangeSet{Target: "/allowed/note.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new")}}},
	} {
		if err := recorder.RecordCommit(context.Background(), record); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	entries, err := (fileHistory{path: path, roots: []string{"/allowed"}}).QueryFile("/allowed/note.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%+v", entries)
	}
	if entries[0].Hash != "new" || entries[0].Attribution != "alice/agent" || entries[1].Hash != "old" {
		t.Fatalf("entries=%+v", entries)
	}
	if entries, err := (fileHistory{path: path, roots: []string{"/other"}}).QueryFile("/allowed/note.md"); err != nil || len(entries) != 0 {
		t.Fatalf("unreadable entries=%+v err=%v", entries, err)
	}
}
