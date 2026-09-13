package openlore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	processor := NewScalarProcessor(cursor, blobs, writerClassifierFunc(func(context.Context, Attribution) analytics.Writer {
		return analytics.WriterHuman
	}))
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
