package openlore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestCapturePreImagesStoresExactBlob(t *testing.T) {
	root := t.TempDir()
	fsys := NewDirFS(root, config.FilesConfig{Allowed: []string{"*.md"}})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.WriteFileAtomic("/doc.md", []byte("before\n"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	blobs, err := OpenBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs := vfs.ChangeSet{Target: "/doc.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("after")}}
	leaves, err := capturePreImages(context.Background(), fsys, blobs, cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 1 || leaves[0].BeforeHash != hashContent([]byte("before\n")) || leaves[0].BeforeSize != 7 {
		t.Fatalf("wrong leaves: %#v", leaves)
	}
	r, size, err := blobs.Get(context.Background(), leaves[0].BeforeHash)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil || size != 7 || string(b) != "before\n" {
		t.Fatalf("wrong blob: %q size=%d err=%v", b, size, err)
	}
	if _, err := os.Stat(root + "/doc.md"); err != nil {
		t.Fatal(err)
	}
}

func TestCapturePreImagesSeparatesExistenceFromUnknownContent(t *testing.T) {
	fsys := NewDirFS(t.TempDir(), config.FilesConfig{Allowed: []string{"*.md"}})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.WriteFileAtomic("/existing.md", []byte("old"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	cs := vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/new.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new")}},
		{Target: "/existing.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("changed")}},
	}}
	leaves, err := capturePreImages(context.Background(), fsys, nil, cs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 2 || leaves[0].BeforeExists || leaves[0].BeforeUnknown || !leaves[1].BeforeExists || leaves[1].BeforeUnknown || leaves[1].BeforeHash != hashContent([]byte("old")) || leaves[1].BeforeSize != 3 {
		t.Fatalf("existence/unknown metadata = %#v", leaves)
	}
}

func TestHistoryCursorDoesNotConsumePartialEOFRecord(t *testing.T) {
	file := filepath.Join(t.TempDir(), "commits.jsonl")
	partial := `{"id":"one"}`
	if err := os.WriteFile(file, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cursor.Next(context.Background()); err != nil || ok {
		t.Fatalf("partial Next = ok %v, err %v", ok, err)
	}
	if got := cursor.Position(); got != (HistoryPosition{}) {
		t.Fatalf("partial record advanced position: %#v", got)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	record, ok, err := cursor.Next(context.Background())
	if err != nil || !ok || record.ID != "one" {
		t.Fatalf("completed Next = %#v, ok %v, err %v", record, ok, err)
	}
}

func TestCommitJournalRotationAndCompressedCursorRestart(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "commits.jsonl")
	if err := appendCommitRecord(file, CommitRecord{ID: "old"}); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	if err := os.Chtimes(file, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	if err := RotateCommitJournal(context.Background(), file, time.Now().UTC(), 0); err != nil {
		t.Fatal(err)
	}
	if err := appendCommitRecord(file, CommitRecord{ID: "new"}); err != nil {
		t.Fatal(err)
	}

	c, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	r, ok, err := c.Next(context.Background())
	if err != nil || !ok || r.ID != "old" {
		t.Fatalf("first = %#v, %v, %v", r, ok, err)
	}
	position := c.Position()
	r, ok, err = c.Next(context.Background())
	if err != nil || !ok || r.ID != "new" {
		t.Fatalf("second = %#v, %v, %v", r, ok, err)
	}
	restarted, err := OpenHistoryCursor(file, position)
	if err != nil {
		t.Fatal(err)
	}
	r, ok, err = restarted.Next(context.Background())
	if err != nil || !ok || r.ID != "new" {
		t.Fatalf("restarted = %#v, %v, %v", r, ok, err)
	}
}

func TestCommitJournalCursorOrdersRepeatedSameDayRotations(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "commits.jsonl")
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	for _, id := range []string{"first", "second"} {
		if err := appendCommitRecord(file, CommitRecord{ID: id, Time: yesterday}); err != nil {
			t.Fatal(err)
		}
		if err := RotateCommitJournal(context.Background(), file, time.Now().UTC(), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendCommitRecord(file, CommitRecord{ID: "active", Time: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "second", "active"} {
		record, ok, err := cursor.Next(context.Background())
		if err != nil || !ok || record.ID != want {
			t.Fatalf("Next = %#v, ok=%v, err=%v; want %q", record, ok, err, want)
		}
	}
}

func TestHistoryCursorRecoversWhenActiveJournalRotatesBehindIt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "commits.jsonl")
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	if err := appendCommitRecord(file, CommitRecord{ID: "old", Time: yesterday}); err != nil {
		t.Fatal(err)
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RotateCommitJournal(context.Background(), file, time.Now().UTC(), 0); err != nil {
		t.Fatal(err)
	}
	if err := appendCommitRecord(file, CommitRecord{ID: "new", Time: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"old", "new"} {
		record, ok, err := cursor.Next(context.Background())
		if err != nil || !ok || record.ID != want {
			t.Fatalf("Next = %#v, %v, %v; want %q", record, ok, err, want)
		}
	}
	if _, ok, err := cursor.Next(context.Background()); err != nil || ok {
		t.Fatalf("cursor did not stop at active tail: ok=%v err=%v", ok, err)
	}
}

func TestHistoryCursorRestoresActiveCheckpointAfterRotation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "commits.jsonl")
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	for _, id := range []string{"first", "second"} {
		if err := appendCommitRecord(file, CommitRecord{ID: id, Time: yesterday}); err != nil {
			t.Fatal(err)
		}
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if record, ok, err := cursor.Next(context.Background()); err != nil || !ok || record.ID != "first" {
		t.Fatalf("first = %#v, ok=%v, err=%v", record, ok, err)
	}
	checkpoint := cursor.Position()
	if checkpoint.Segment != "" {
		t.Fatalf("checkpoint = %#v, want legacy active position", checkpoint)
	}
	if err := RotateCommitJournal(context.Background(), file, time.Now().UTC(), 0); err != nil {
		t.Fatal(err)
	}
	if err := appendCommitRecord(file, CommitRecord{ID: strings.Repeat("active", 100), Time: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenHistoryCursor(file, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if record, ok, err := restarted.Next(context.Background()); err != nil || !ok || record.ID != "second" {
		t.Fatalf("restored = %#v, ok=%v, err=%v", record, ok, err)
	}
}

func TestCommitJournalRetention(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "commits-2020-01-01.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "commits.jsonl")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RotateCommitJournal(context.Background(), file, time.Now(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old segment remains: %v", err)
	}
}

func TestCommitJournalRetentionProtectsCursorAndNewerSegments(t *testing.T) {
	dir := t.TempDir()
	for _, day := range []string{"2020-01-01", "2021-01-01", "2022-01-01"} {
		if err := os.WriteFile(filepath.Join(dir, "commits-"+day+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "commits.jsonl")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	protected := HistoryPosition{Segment: "commits-2021-01-01.jsonl"}
	if err := RotateCommitJournal(context.Background(), file, time.Now().UTC(), 24*time.Hour, protected); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "commits-2020-01-01.jsonl")
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain segment before cursor remains: %v", err)
	}
	if _, err := os.Stat(old + ".zst"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("compressed segment before cursor remains: %v", err)
	}
	for _, day := range []string{"2021-01-01", "2022-01-01"} {
		plain := filepath.Join(dir, "commits-"+day+".jsonl")
		if _, plainErr := os.Stat(plain); plainErr != nil {
			if _, compressedErr := os.Stat(plain + ".zst"); compressedErr != nil {
				t.Fatalf("protected segment %s pruned: plain=%v compressed=%v", day, plainErr, compressedErr)
			}
		}
	}
}

func TestCommitJournalRetentionMapsActiveCursorToDetachedSegment(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "commits.jsonl")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"processed", "pending"} {
		if err := appendCommitRecord(file, CommitRecord{ID: id, Time: old}); err != nil {
			t.Fatal(err)
		}
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cursor.Next(context.Background()); err != nil || !ok {
		t.Fatalf("read checkpoint: ok=%v err=%v", ok, err)
	}
	if err := RotateCommitJournal(context.Background(), file, time.Now().UTC(), 24*time.Hour, cursor.Position()); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "commits-2020-01-01*.jsonl.zst"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("protected detached segment = %v, err=%v", matches, err)
	}
}

func TestHistorySegmentPositionSupportsLargeRecords(t *testing.T) {
	file := filepath.Join(t.TempDir(), "commits-2020-01-01.jsonl")
	record := CommitRecord{ID: "large", ChangeSet: vfs.ChangeSet{Target: "/large.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte(strings.Repeat("x", 128*1024))}}}
	if err := appendCommitRecord(file, record); err != nil {
		t.Fatal(err)
	}
	offset, found, err := historySegmentPosition(file, record.ID)
	if err != nil || !found || offset <= 128*1024 {
		t.Fatalf("large record position = %d, found=%v, err=%v", offset, found, err)
	}
}

func TestGarbageCollectHistoryBlobsRejectsPartialSealedRecord(t *testing.T) {
	dir := t.TempDir()
	blobs, err := OpenBlobStore(filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("must remain")
	hash := hashContent(content)
	if err := blobs.Put(context.Background(), hash, strings.NewReader(string(content))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commits-2020-01-01.jsonl"), []byte(`{"id":"partial"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commits.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GarbageCollectHistoryBlobs(context.Background(), dir); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("GC error = %v, want partial sealed record error", err)
	}
	if ok, err := blobs.Has(context.Background(), hash); err != nil || !ok {
		t.Fatalf("blob deleted after sealed corruption: ok=%v err=%v", ok, err)
	}
}

func TestHistoryCursorCheckpointSurvivesBackgroundCompression(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "commits.jsonl")
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	for _, id := range []string{"first", "second"} {
		if err := appendCommitRecord(file, CommitRecord{ID: id, Time: yesterday}); err != nil {
			t.Fatal(err)
		}
	}
	segment, logicalSize, err := detachCommitJournal(file, time.Now().UTC())
	if err != nil || segment == "" {
		t.Fatalf("detach = %q, %v", segment, err)
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if record, ok, err := cursor.Next(context.Background()); err != nil || !ok || record.ID != "first" {
		t.Fatalf("first = %#v, ok=%v, err=%v", record, ok, err)
	}
	checkpoint := cursor.Position()
	if strings.HasSuffix(checkpoint.Segment, ".zst") {
		t.Fatalf("checkpoint unexpectedly compressed before maintenance: %#v", checkpoint)
	}
	if err := appendCommitRecord(file, CommitRecord{ID: "active", Time: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := compressHistorySegment(context.Background(), segment, logicalSize); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"second", "active"} {
		if record, ok, err := cursor.Next(context.Background()); err != nil || !ok || record.ID != want {
			t.Fatalf("live cursor after compression = %#v, ok=%v, err=%v; want %q", record, ok, err, want)
		}
	}
	restarted, err := OpenHistoryCursor(file, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if record, ok, err := restarted.Next(context.Background()); err != nil || !ok || record.ID != "second" {
		t.Fatalf("restarted = %#v, ok=%v, err=%v", record, ok, err)
	}
	lag, err := HistoryCursorLagBytes(file, checkpoint)
	active, statErr := os.Stat(file)
	if statErr != nil {
		t.Fatal(statErr)
	}
	wantLag := logicalSize - checkpoint.Offset + active.Size()
	if err != nil || lag != wantLag {
		t.Fatalf("lag from pre-compression checkpoint = %d, want %d, err=%v", lag, wantLag, err)
	}
}

func TestGarbageCollectHistoryBlobsPreservesReferencesAndFailsSafe(t *testing.T) {
	dir := t.TempDir()
	blobs, err := OpenBlobStore(filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	kept, stale := []byte("kept"), []byte("stale")
	for _, b := range [][]byte{kept, stale} {
		if err := blobs.Put(context.Background(), hashContent(b), strings.NewReader(string(b))); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "commits.jsonl")
	if err := appendCommitRecord(file, CommitRecord{ID: "one", Leaves: []LeafRecord{{BeforeHash: hashContent(kept)}}}); err != nil {
		t.Fatal(err)
	}
	stats, err := GarbageCollectHistoryBlobs(context.Background(), dir)
	if err != nil || stats.Objects != 1 {
		t.Fatalf("GC = %#v, %v", stats, err)
	}
	if ok, _ := blobs.Has(context.Background(), hashContent(kept)); !ok {
		t.Fatal("referenced blob deleted")
	}
	if err := os.WriteFile(file, []byte("{corrupt}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third := []byte("third")
	if err := blobs.Put(context.Background(), hashContent(third), strings.NewReader(string(third))); err != nil {
		t.Fatal(err)
	}
	if _, err := GarbageCollectHistoryBlobs(context.Background(), dir); err == nil {
		t.Fatal("corrupt journal accepted")
	}
	if ok, _ := blobs.Has(context.Background(), hashContent(third)); !ok {
		t.Fatal("blob deleted after corrupt journal")
	}
}

func TestGarbageCollectHistoryBlobsHonorsCancellationBeforeDeleting(t *testing.T) {
	dir := t.TempDir()
	blobs, err := OpenBlobStore(filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("must remain")
	hash := hashContent(content)
	if err := blobs.Put(context.Background(), hash, strings.NewReader(string(content))); err != nil {
		t.Fatal(err)
	}
	if err := appendCommitRecord(filepath.Join(dir, "commits.jsonl"), CommitRecord{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GarbageCollectHistoryBlobs(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("GC error = %v, want context cancellation", err)
	}
	if ok, err := blobs.Has(context.Background(), hash); err != nil || !ok {
		t.Fatalf("blob deleted by canceled GC: ok=%v err=%v", ok, err)
	}
}

func TestScalarProcessorUsesRecordedSizesWithoutBeforeBlob(t *testing.T) {
	record := CommitRecord{ID: "sizes", ChangeSet: vfs.ChangeSet{Target: "/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("1234567")}}, Leaves: []LeafRecord{{Target: "/a.md", Action: vfs.ChangeActionWrite, BeforeExists: true, BeforeHash: hashContent([]byte("old")), BeforeSize: 3, AfterSize: 7}}}
	events := scalarProcessorForRecords(t, []CommitRecord{record}).Drain(context.Background())
	delta := events[0].Fields["delta"].(map[string]any)
	if delta["bytes"] != float64(4) {
		t.Fatalf("byte delta = %#v, want 4", delta["bytes"])
	}
}

func TestWriteLogCapturesPreImagesForUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	fsys := NewDirFS(t.TempDir(), config.FilesConfig{Allowed: []string{"*.md"}})
	if err := fsys.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	blobs, err := OpenBlobStore(filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(dir, "commits.jsonl")
	log := newWriteLog(fsys, nil, nil, 4)
	log.SetCommitJournal(historyPath, blobs, true)
	defer log.Close(ctx)

	for _, cs := range []vfs.ChangeSet{
		{Target: "/doc.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("A")}},
		{Target: "/doc.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("BB")}},
		{Target: "/doc.md", Action: vfs.ChangeActionRemove},
	} {
		if _, err := log.Submit(ctx, Attribution{Principal: "human"}, cs); err != nil {
			t.Fatal(err)
		}
	}
	cursor, err := OpenHistoryCursor(historyPath, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	var records []CommitRecord
	for {
		record, ok, err := cursor.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		records = append(records, record)
	}
	if len(records) != 3 {
		t.Fatalf("journal records = %d, want 3", len(records))
	}
	aHash, bHash := hashContent([]byte("A")), hashContent([]byte("BB"))
	if got := records[0].Leaves[0]; got.BeforeExists || got.AfterHash != aHash || got.AfterSize != 1 {
		t.Fatalf("create leaf = %#v", got)
	}
	if got := records[1].Leaves[0]; !got.BeforeExists || got.BeforeUnknown || got.BeforeHash != aHash || got.BeforeSize != 1 || got.AfterHash != bHash || got.AfterSize != 2 {
		t.Fatalf("update leaf = %#v", got)
	}
	if got := records[2].Leaves[0]; !got.BeforeExists || got.BeforeUnknown || got.BeforeHash != bHash || got.BeforeSize != 2 || got.AfterHash != "" || got.AfterSize != 0 {
		t.Fatalf("delete leaf = %#v", got)
	}

	replay, err := OpenHistoryCursor(historyPath, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	processor := NewScalarProcessor(replay, blobs, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer { return analytics.WriterHuman }))
	events := processor.(*ScalarProcessor).Drain(ctx)
	if len(events) != 3 {
		t.Fatalf("scalar events = %d, want 3", len(events))
	}
	for i, want := range map[int]float64{1: 1, 2: 2} {
		before := events[i].Fields["before"].(map[string]float64)
		if before["bytes"] != want {
			t.Fatalf("event %d before bytes = %v, want %v", i, before["bytes"], want)
		}
	}
	if events[1].Fields["delta"].(map[string]any)["bytes"] != float64(1) || events[2].Fields["delta"].(map[string]any)["bytes"] != float64(-2) {
		t.Fatalf("update/delete deltas = %#v / %#v", events[1].Fields["delta"], events[2].Fields["delta"])
	}
}

func TestScalarProcessorDerivesExactBeforeAfterDelta(t *testing.T) {
	ctx := context.Background()
	before := []byte("before\n")
	after := []byte("after text\nnext")
	blobs, err := OpenBlobStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := hashContent(before)
	if err := blobs.Put(ctx, beforeHash, strings.NewReader(string(before))); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(t.TempDir(), "commits.jsonl")
	record := CommitRecord{
		ID:          "commit-1",
		Attribution: Attribution{Principal: "adil"},
		ChangeSet:   vfs.ChangeSet{Target: "/docs/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: after}},
		Leaves:      []LeafRecord{{Target: "/docs/a.md", Action: vfs.ChangeActionWrite, BeforeHash: beforeHash, AfterHash: hashContent(after)}},
	}
	if err := appendCommitRecord(historyPath, record); err != nil {
		t.Fatal(err)
	}
	cursor, err := OpenHistoryCursor(historyPath, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	processor := NewScalarProcessor(cursor, blobs, IdentityStoreClassifier(nil))
	events := processor.Process(ctx, analytics.Event{ID: "write-1", Type: "doc.write", InvocationID: "invocation-1", Fields: map[string]any{"commit_id": "commit-1"}})
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.ParentID != "write-1" || event.InvocationID != "invocation-1" || event.Fields["action"] != "update" || event.Fields["writer"] != "human" {
		t.Fatalf("wrong correlation: %#v", event)
	}
	delta := event.Fields["delta"].(map[string]any)
	if delta["bytes"] != float64(8) || delta["lines"] != float64(1) || delta["words"] != float64(2) || delta["tokens"] != float64(2) {
		t.Fatalf("wrong delta: %#v", delta)
	}
}

type testScalarProvider struct{}

func (testScalarProvider) Name() string { return "custom" }
func (testScalarProvider) Scalars(_ string, b []byte) map[string]float64 {
	return map[string]float64{"custom": float64(len(b) * 10)}
}

func TestScalarProcessorUsesCustomProviders(t *testing.T) {
	record := CommitRecord{ID: "custom", ChangeSet: vfs.ChangeSet{Target: "/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("abc")}}, Leaves: []LeafRecord{{Target: "/a.md"}}}
	p := scalarProcessorForRecords(t, []CommitRecord{record}, testScalarProvider{})
	events := p.Process(context.Background(), writeEvent("write-custom", "custom"))
	after := events[0].Fields["after"].(map[string]float64)
	if after["custom"] != 30 || len(after) != 1 {
		t.Fatalf("custom scalars not used exclusively: %#v", after)
	}
	if tokenizer := events[0].Fields["tokenizer"]; tokenizer != "approx" {
		t.Fatalf("tokenizer = %q, want approx", tokenizer)
	}
}

func TestScalarProcessorProcessesHistoryBeforeCorrelatedTarget(t *testing.T) {
	records := []CommitRecord{
		scalarRecord("old", "/old.md", false),
		scalarRecord("new", "/new.md", false),
	}
	p := scalarProcessorForRecords(t, records)
	events := p.Process(context.Background(), writeEvent("write-new", "new"))
	if len(events) != 2 || events[0].Fields["commit_id"] != "old" || events[0].ParentID != "" || events[1].Fields["commit_id"] != "new" || events[1].ParentID != "write-new" {
		t.Fatalf("history was skipped or misattributed: %#v", events)
	}
}

type collectingSink struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (s *collectingSink) Record(_ context.Context, event analytics.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *collectingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestPipelineDrainsHistoryAndRestoresScalarCursor(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "commits.jsonl")
	for _, record := range []CommitRecord{scalarRecord("one", "/one.md", false), scalarRecord("two", "/two.md", false)} {
		if err := appendCommitRecord(historyPath, record); err != nil {
			t.Fatal(err)
		}
	}
	eventLog, err := analytics.OpenEventLog(t.TempDir(), analytics.LogOptions{Compress: "none"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "pipeline.checkpoint")
	newProcessor := func() analytics.Processor {
		cursor, err := OpenHistoryCursor(historyPath, HistoryPosition{})
		if err != nil {
			t.Fatal(err)
		}
		return NewScalarProcessor(cursor, nil, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer { return analytics.WriterAgent }))
	}
	firstSink := &collectingSink{}
	first := analytics.NewPipeline(eventLog, checkpoint, analytics.PipelineOptions{Processors: []analytics.Processor{newProcessor()}, Sink: firstSink})
	first.Run(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for firstSink.count() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if firstSink.count() != 2 {
		t.Fatalf("startup history events = %d, want 2", firstSink.count())
	}
	b, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Processors map[string]json.RawMessage `json:"processors"`
	}
	if json.Unmarshal(b, &state) != nil || len(state.Processors["doc-scalars"]) == 0 {
		t.Fatalf("checkpoint does not include history position: %s", b)
	}
	secondSink := &collectingSink{}
	second := analytics.NewPipeline(eventLog, checkpoint, analytics.PipelineOptions{Processors: []analytics.Processor{newProcessor()}, Sink: secondSink})
	second.Run(context.Background())
	time.Sleep(25 * time.Millisecond)
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if secondSink.count() != 0 {
		t.Fatalf("restart duplicated %d history events", secondSink.count())
	}
}

func TestScalarProcessorMultiLeafDocWritesAreIdempotent(t *testing.T) {
	multi := CommitRecord{ID: "multi", ChangeSet: vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/a.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("a")}},
		{Target: "/b.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("b")}},
	}}, Leaves: []LeafRecord{{Target: "/a.md"}, {Target: "/b.md"}}}
	p := scalarProcessorForRecords(t, []CommitRecord{multi, scalarRecord("later", "/later.md", false)})
	if got := p.Process(context.Background(), writeEvent("write-a", "multi")); len(got) != 2 {
		t.Fatalf("first event produced %d scalar events", len(got))
	}
	if got := p.Process(context.Background(), writeEvent("write-b", "multi")); len(got) != 0 {
		t.Fatalf("duplicate advanced history: %#v", got)
	}
	got := p.Process(context.Background(), writeEvent("write-later", "later"))
	if len(got) != 1 || got[0].Fields["commit_id"] != "later" || got[0].ParentID != "write-later" {
		t.Fatalf("later commit misattributed: %#v", got)
	}
}

func TestScalarProcessorReplayRewindsHistoryAndAppliesCutoff(t *testing.T) {
	cutoff := time.Now().UTC()
	records := []CommitRecord{
		scalarRecord("old", "/old.md", false),
		scalarRecord("new", "/new.md", false),
	}
	records[0].Time = cutoff.Add(-time.Hour)
	records[1].Time = cutoff.Add(time.Hour)
	p := scalarProcessorForRecords(t, records)
	if got := p.Drain(context.Background()); len(got) != 2 {
		t.Fatalf("initial drain = %d events", len(got))
	}
	if err := p.ResetForReplay(cutoff); err != nil {
		t.Fatal(err)
	}
	got := p.Drain(context.Background())
	if len(got) != 1 || got[0].Fields["commit_id"] != "new" {
		t.Fatalf("replay events = %#v", got)
	}
}

func TestScalarProcessorIgnoresMetadataLeaves(t *testing.T) {
	record := CommitRecord{ID: "metadata", Leaves: []LeafRecord{{Target: "/a.md", Action: vfs.ChangeActionSetXattr}}}
	if got := scalarProcessorForRecords(t, []CommitRecord{record}).Drain(context.Background()); len(got) != 0 {
		t.Fatalf("metadata produced scalar events: %#v", got)
	}
}

func TestScalarProcessorClassifiesCreateAndUnknownOverwrite(t *testing.T) {
	record := CommitRecord{ID: "actions", ChangeSet: vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/new.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new")}},
		{Target: "/existing.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("changed")}},
	}}, Leaves: []LeafRecord{
		{Target: "/new.md"},
		{Target: "/existing.md", BeforeExists: true, BeforeUnknown: true},
	}}
	events := scalarProcessorForRecords(t, []CommitRecord{record}).Process(context.Background(), writeEvent("write", "actions"))
	if events[0].Fields["action"] != "create" || events[0].Fields["first_seen"] != true || events[1].Fields["action"] != "update" || events[1].Fields["first_seen"] != true {
		t.Fatalf("actions/first_seen = %#v, %#v", events[0].Fields, events[1].Fields)
	}
}

func scalarRecord(id, target string, existed bool) CommitRecord {
	b := []byte(id)
	return CommitRecord{ID: id, ChangeSet: vfs.ChangeSet{Target: target, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: b}}, Leaves: []LeafRecord{{Target: target, BeforeExists: existed}}}
}

func writeEvent(id, commit string) analytics.Event {
	return analytics.Event{ID: id, Type: "doc.write", InvocationID: id + "-invocation", Fields: map[string]any{"commit_id": commit}}
}

func scalarProcessorForRecords(t *testing.T, records []CommitRecord, providers ...analytics.ContentScalarProvider) *ScalarProcessor {
	t.Helper()
	file := filepath.Join(t.TempDir(), "commits.jsonl")
	for _, record := range records {
		if err := appendCommitRecord(file, record); err != nil {
			t.Fatal(err)
		}
	}
	cursor, err := OpenHistoryCursor(file, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	return NewScalarProcessor(cursor, nil, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer { return analytics.WriterAgent }), providers...).(*ScalarProcessor)
}
