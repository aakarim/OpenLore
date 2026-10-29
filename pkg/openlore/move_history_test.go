package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

func TestMovePreservesProvenanceAndFindsStaleReferences(t *testing.T) {
	disk, _ := newWritableDirFS(t)
	if err := disk.MkdirAll("/user/alice/archive"); err != nil {
		t.Fatal(err)
	}
	store := NewJSONLHistoryStore(t.TempDir())
	log := newWriteLog(disk, nil, silentLogger(), 1)
	log.SetHistoryRecorder(store)
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	attribution := Attribution{Principal: "alice", Actor: "organiser"}
	view := newMiddlewareFS(disk, attribution, func(ctx context.Context, op WriteOp) (WriteResult, error) {
		hash, err := log.Submit(ctx, op.Attribution, op.persistenceChangeSet())
		return WriteResult{Hash: hash}, err
	})
	sh := shell.NewShell(newAliasFS(view, []pathAlias{{Alias: "/alice", Target: "/user/alice"}}))
	sh.SetDocsets([]cmds.DocsetInfo{
		{Name: "alice", Paths: []string{"/user/alice"}, Writable: true},
		{Name: "alice", Paths: []string{"/alice"}, AliasTarget: "/user/alice"},
	})
	sh.SetHistoryBackend(scopedHistory{store: store, roots: []string{"/user/alice"}})
	run := func(command string) (string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := sh.ExecPipeline(command, &out, &errOut, nil); code != 0 {
			t.Fatalf("%s: exit %d: %s", command, code, &errOut)
		}
		return out.String(), errOut.String()
	}
	run("echo original > /user/alice/a.md")
	run("echo '[relative](a.md#section) and [alias](/alice/a.md)' > /user/alice/index.md")
	run("echo 'Read `/user/alice/a.md` and `../a.md`.' > /user/alice/archive/SKILL.md")
	run("echo '[not a local link](https://example.org/user/alice/a.md)' > /user/alice/unrelated.md")
	_, warning := run("mv /alice/a.md /alice/archive/b.md")
	if !strings.Contains(warning, "/user/alice/archive/SKILL.md") || !strings.Contains(warning, "/user/alice/index.md") {
		t.Fatalf("move did not warn about stale references: %s", warning)
	}
	links, _ := run("lore links --to /alice/a.md")
	if links != "/user/alice/archive/SKILL.md\n/user/alice/index.md\n" {
		t.Fatalf("references (once each, canonical paths): %s", links)
	}
	// Reusing the source after the move must not contaminate b's history.
	run("echo unrelated > /alice/a.md")
	run("mv /alice/archive/b.md /alice/archive/c.md")
	out, _ := run("lore history /alice/archive/c.md")
	var records []struct {
		Action, Target, From, Hash, Attribution string
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var record struct{ Action, Target, From, Hash, Attribution string }
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != 3 || records[0].Action != "move" || records[0].From != "/user/alice/archive/b.md" || records[1].Action != "move" || records[1].From != "/user/alice/a.md" || records[2].Action != "write" || records[2].Target != "/user/alice/a.md" {
		t.Fatalf("lineage: %s", out)
	}
	for _, record := range records {
		if record.Hash != hashOf([]byte("original\n")) || record.Attribution != "alice/organiser" {
			t.Fatalf("lost original hash or author: %+v", record)
		}
	}
	// Storage survives reopening and never follows a move across a read boundary.
	reopened := NewJSONLHistoryStore(store.dir)
	page, err := reopened.Query(context.Background(), HistoryQuery{FileKey: "/user/alice/archive/c.md", Roots: []string{"/user/alice/archive"}, FollowMoves: true})
	if err != nil || len(page.Records) != 1 || page.Records[0].From != "/user/alice/archive/b.md" {
		t.Fatalf("restricted lineage: %+v, %v", page, err)
	}
	// Existing shells resolve live grants again, including nested carve-outs.
	// Hiding an old source must stop lineage even under a readable parent.
	scope := HistoryQuery{Roots: []string{"/user/alice"}}
	live := scopedHistory{store: reopened, liveQuery: func() HistoryQuery { return scope }}
	data, err := live.QueryPath("/user/alice/archive/c.md")
	if err != nil || bytes.Count(data, []byte("\n")) != 3 {
		t.Fatalf("live lineage: %s, %v", data, err)
	}
	scope.Boundaries = []string{"/user/alice", "/user/alice/a.md"}
	data, err = live.QueryPath("/user/alice/archive/c.md")
	if err != nil || bytes.Count(data, []byte("\n")) != 1 || bytes.Contains(data, []byte("/user/alice/a.md")) {
		t.Fatalf("reloaded scope leaked old source: %s, %v", data, err)
	}
}
