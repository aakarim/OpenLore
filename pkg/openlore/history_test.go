package openlore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestHistoryRecordsExcludeWritePayloads(t *testing.T) {
	content := []byte(strings.Repeat("private payload", 10_000))
	committed := vfs.CommitResult{Committed: vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/allowed/large.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: content}},
		{Target: "/secret/hidden.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("secret")}},
	}}}
	records := historyRecords(time.Now().UTC(), Attribution{Principal: "alice", Actor: "agent"}, committed)
	store := NewJSONLHistoryStore(t.TempDir())
	if err := store.Record(context.Background(), records); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(store.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) >= len(content) || strings.Contains(string(b), "private payload") {
		t.Fatalf("history persisted write payload: bytes=%d", len(b))
	}
	page, err := store.Query(context.Background(), HistoryQuery{Roots: []string{"/allowed"}, Principal: "alice", Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].FileKey != "/allowed/large.md" || page.Records[0].Action != string(vfs.ChangeActionWrite) || page.Records[0].ContentHash == "" {
		t.Fatalf("filtered history=%+v", page)
	}
}

func TestHistoryQueriesFileShardNewestFirstAndPaginates(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	records := []HistoryRecord{
		{Time: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice"}, FileKey: "/allowed/note.md", Action: "write", ContentHash: "old"},
		{Time: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice", Actor: "agent"}, FileKey: "/allowed/other.md", Action: "write", ContentHash: "other"},
		{Time: time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice", Actor: "agent"}, FileKey: "/allowed/note.md", Action: "write", ContentHash: "new"},
	}
	if err := store.Record(context.Background(), records); err != nil {
		t.Fatal(err)
	}

	page, err := store.Query(context.Background(), HistoryQuery{FileKey: "/allowed/note.md", Roots: []string{"/allowed"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].ContentHash != "new" || page.NextCursor == "" {
		t.Fatalf("first page=%+v", page)
	}
	if err := store.Record(context.Background(), []HistoryRecord{{
		Time: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "alice"}, FileKey: "/allowed/note.md", Action: "write", ContentHash: "newest",
	}}); err != nil {
		t.Fatal(err)
	}
	next, err := store.Query(context.Background(), HistoryQuery{FileKey: "/allowed/note.md", Roots: []string{"/allowed"}, Limit: 1, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Records) != 1 || next.Records[0].ContentHash != "old" || next.NextCursor != "" {
		t.Fatalf("next page=%+v", next)
	}
	if denied, err := store.Query(context.Background(), HistoryQuery{FileKey: "/allowed/note.md", Roots: []string{"/other"}}); err != nil || len(denied.Records) != 0 {
		t.Fatalf("unreadable page=%+v err=%v", denied, err)
	}
}

func TestHistoryUsesReverseCommitOrderForEqualTimestamps(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	at := time.Now().UTC()
	if err := store.Record(context.Background(), []HistoryRecord{
		{Time: at, FileKey: "/docs/note.md", Action: "write", ContentHash: "intermediate"},
		{Time: at, FileKey: "/docs/note.md", Action: "write", ContentHash: "final"},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.Query(context.Background(), HistoryQuery{FileKey: "/docs/note.md", Roots: []string{"/docs"}, Limit: 1})
	if err != nil || len(page.Records) != 1 || page.Records[0].ContentHash != "final" {
		t.Fatalf("latest equal-time record=%+v err=%v", page, err)
	}
}

func TestHistoryDeletePurgesFileShardUntilPathIsRecreated(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	key := "/docs/note.md"
	if err := store.Record(context.Background(), []HistoryRecord{{FileKey: key, Action: string(vfs.ChangeActionWrite), ContentHash: "old"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), []HistoryRecord{{FileKey: key, Action: string(vfs.ChangeActionRemove)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.filePath(key)); !os.IsNotExist(err) {
		t.Fatalf("deleted file history shard still exists: %v", err)
	}
	page, err := store.Query(context.Background(), HistoryQuery{FileKey: key, Roots: []string{"/docs"}})
	if err != nil || len(page.Records) != 0 {
		t.Fatalf("deleted file history=%+v err=%v", page, err)
	}

	if err := store.Record(context.Background(), []HistoryRecord{{FileKey: key, Action: string(vfs.ChangeActionWrite), ContentHash: "new"}}); err != nil {
		t.Fatal(err)
	}
	page, err = store.Query(context.Background(), HistoryQuery{FileKey: key, Roots: []string{"/docs"}})
	if err != nil || len(page.Records) != 1 || page.Records[0].ContentHash != "new" {
		t.Fatalf("recreated file history=%+v err=%v", page, err)
	}
}

func TestHistoryRemoveAllPurgesDescendantShardsOnly(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	records := []HistoryRecord{
		{FileKey: "/docs/tree/a.md", Action: string(vfs.ChangeActionWrite)},
		{FileKey: "/docs/tree/nested/b.md", Action: string(vfs.ChangeActionWrite)},
		{FileKey: "/docs/tree-sibling.md", Action: string(vfs.ChangeActionWrite)},
	}
	if err := store.Record(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(store.dir, "events.jsonl")
	if err := os.WriteFile(globalPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), []HistoryRecord{{FileKey: "/docs/tree", Action: string(vfs.ChangeActionRemoveAll)}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"/docs/tree/a.md", "/docs/tree/nested/b.md"} {
		if _, err := os.Stat(store.filePath(key)); !os.IsNotExist(err) {
			t.Fatalf("descendant history shard %q still exists: %v", key, err)
		}
	}
	if _, err := os.Stat(store.filePath("/docs/tree-sibling.md")); err != nil {
		t.Fatalf("sibling history shard removed: %v", err)
	}
}

func TestScopedHistoryConsumesAllPages(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	records := make([]HistoryRecord, 0, defaultHistoryPageSize+1)
	for i := 0; i <= defaultHistoryPageSize; i++ {
		records = append(records, HistoryRecord{Time: time.Unix(int64(i), 0), FileKey: "/docs/note.md", Action: "write"})
	}
	if err := store.Record(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	b, err := (scopedHistory{store: store, roots: []string{"/docs"}}).Query("", "")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(b), "\n"); lines != len(records) {
		t.Fatalf("history lines=%d want=%d", lines, len(records))
	}
}

func TestFileHistoryDoesNotReadGlobalJournal(t *testing.T) {
	store := NewJSONLHistoryStore(t.TempDir())
	target := HistoryRecord{Time: time.Now(), Attribution: Attribution{Principal: "alice"}, FileKey: "/docs/target.md", Action: "write"}
	if err := store.Record(context.Background(), []HistoryRecord{target}); err != nil {
		t.Fatal(err)
	}
	// A corrupt global journal sized like the production incident proves the
	// indexed file query opens only the file-key shard.
	globalPath := filepath.Join(store.dir, "events.jsonl")
	if err := os.WriteFile(globalPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(globalPath, 260<<20); err != nil {
		t.Fatal(err)
	}
	page, err := store.Query(context.Background(), HistoryQuery{FileKey: target.FileKey, Roots: []string{"/docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].FileKey != target.FileKey {
		t.Fatalf("page=%+v", page)
	}
}

func BenchmarkJSONLHistoryStoreQueryByFile(b *testing.B) {
	store := NewJSONLHistoryStore(b.TempDir())
	records := make([]HistoryRecord, 0, 10_100)
	for i := 0; i < 10_000; i++ {
		records = append(records, HistoryRecord{Time: time.Unix(int64(i), 0), Attribution: Attribution{Principal: "alice"}, FileKey: "/docs/unrelated/" + strconv.Itoa(i%100), Action: "write"})
	}
	for i := 0; i < 100; i++ {
		records = append(records, HistoryRecord{Time: time.Unix(int64(i), 0), Attribution: Attribution{Principal: "alice"}, FileKey: "/docs/target.md", Action: "write"})
	}
	if err := store.Record(context.Background(), records); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		page, err := store.Query(context.Background(), HistoryQuery{FileKey: "/docs/target.md", Roots: []string{"/docs"}, Limit: 50})
		if err != nil || len(page.Records) != 50 {
			b.Fatalf("records=%d err=%v", len(page.Records), err)
		}
	}
}

func TestHistoryRecordJSONShape(t *testing.T) {
	b, err := json.Marshal(HistoryRecord{FileKey: "/docs/a.md", Action: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "bytes") || strings.Contains(string(b), "write") && strings.Contains(string(b), "opts") {
		t.Fatalf("payload fields leaked into history schema: %s", b)
	}
}
