package openlore

import (
	"context"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestInboxMentionFanout(t *testing.T) {
	p := NewInboxPlugin()
	p.recipients = map[string]struct{}{"author": {}, "benchmark": {}, "regex": {}}
	var committed vfs.ChangeSet
	next := func(_ context.Context, op WriteOp) (WriteResult, error) {
		committed = op.persistenceChangeSet()
		return WriteResult{Hash: "ok"}, nil
	}
	h := p.WriteMiddleware()[0](next)
	body := "ping @benchmark, @regex and @benchmark; not carlos@regex, @missing or @author"
	_, err := h(context.Background(), NewWriteOp(Actor{ID: "author"}, vfs.ChangeSet{
		Target: "/channels/profiling/posts/author/001-author.md",
		Action: vfs.ChangeActionWrite,
		Write:  &vfs.WriteChange{Bytes: []byte(body)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(committed.Changes) != 3 {
		t.Fatalf("changes=%d, want source plus two notifications", len(committed.Changes))
	}
	if committed.Changes[0].Target != "/channels/profiling/posts/author/001-author.md" {
		t.Fatalf("source moved: %s", committed.Changes[0].Target)
	}
	got := []string{committed.Changes[1].Target, committed.Changes[2].Target}
	if !strings.HasPrefix(got[0], "/inboxes/benchmark/") || !strings.HasPrefix(got[1], "/inboxes/regex/") {
		t.Fatalf("fanout targets=%v", got)
	}
	for _, change := range committed.Changes[1:] {
		if !strings.Contains(string(change.Write.Bytes), "source: /channels/profiling/posts/author/001-author.md") {
			t.Fatalf("notification lacks source metadata: %s", change.Write.Bytes)
		}
	}
}

func TestInboxMentionFanoutOnlyHandlesMessages(t *testing.T) {
	p := NewInboxPlugin()
	p.recipients = map[string]struct{}{"benchmark": {}}
	for _, target := range []string{
		"/instructions/contract.md",
		"/inboxes/benchmark/already.md",
		"/channels/profiling/README.md",
	} {
		t.Run(target, func(t *testing.T) {
			var leaves []vfs.Change
			h := p.WriteMiddleware()[0](func(_ context.Context, op WriteOp) (WriteResult, error) {
				leaves = op.Leaves()
				return WriteResult{}, nil
			})
			_, err := h(context.Background(), NewWriteOp(Actor{ID: "author"}, vfs.ChangeSet{
				Target: target,
				Action: vfs.ChangeActionWrite,
				Write:  &vfs.WriteChange{Bytes: []byte("@benchmark")},
			}))
			if err != nil || len(leaves) != 1 || leaves[0].Target != target {
				t.Fatalf("unexpected fanout: leaves=%+v err=%v", leaves, err)
			}
		})
	}
}

func TestInboxMentionNotificationNameIsStableAcrossEdits(t *testing.T) {
	p := NewInboxPlugin()
	p.recipients = map[string]struct{}{"benchmark": {}}
	var names []string
	h := p.WriteMiddleware()[0](func(_ context.Context, op WriteOp) (WriteResult, error) {
		leaves := op.Leaves()
		names = append(names, leaves[1].Target)
		return WriteResult{}, nil
	})
	for _, body := range []string{"first @benchmark", "edited @benchmark"} {
		_, err := h(context.Background(), NewWriteOp(Actor{ID: "author"}, vfs.ChangeSet{
			Target: "/threads/author/scanning/replies/001.md",
			Action: vfs.ChangeActionWrite,
			Write:  &vfs.WriteChange{Bytes: []byte(body)},
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
	if names[0] != names[1] {
		t.Fatalf("edit created a second notification: %v", names)
	}
}
