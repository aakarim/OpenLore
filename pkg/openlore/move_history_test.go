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

func TestMoveHistoryOccupantBoundaries(t *testing.T) {
	type record struct {
		Action, Target, From, Hash string
	}
	write := func(path, content string) record {
		return record{Action: "write", Target: path, Hash: hashOf([]byte(content + "\n"))}
	}
	move := func(from, to, content string) record {
		return record{Action: "move", Target: to, From: from, Hash: hashOf([]byte(content + "\n"))}
	}
	for _, tt := range []struct {
		name     string
		commands []string
		path     string
		want     []record
	}{
		{
			name: "reused source moved again",
			commands: []string{
				"echo original > /docs/a",
				"mv /docs/a /docs/b",
				"echo unrelated > /docs/a",
				"mv /docs/a /docs/c",
			},
			path: "/docs/c",
			want: []record{move("/docs/a", "/docs/c", "unrelated"), write("/docs/a", "unrelated")},
		},
		{
			name: "reused source still occupied",
			commands: []string{
				"echo original > /docs/a",
				"mv /docs/a /docs/b",
				"echo unrelated > /docs/a",
			},
			path: "/docs/a",
			want: []record{write("/docs/a", "unrelated")},
		},
		{
			name:     "moved away path",
			commands: []string{"echo original > /docs/a", "mv /docs/a /docs/b"},
			path:     "/docs/a",
			want:     []record{move("/docs/a", "/docs/b", "original"), write("/docs/a", "original")},
		},
		{
			name: "round trip and cycle",
			commands: []string{
				"echo original > /docs/a",
				"mv /docs/a /docs/b",
				"mv /docs/b /docs/a",
				"mv /docs/a /docs/c",
				"mv /docs/c /docs/b",
			},
			path: "/docs/b",
			want: []record{
				move("/docs/c", "/docs/b", "original"),
				move("/docs/a", "/docs/c", "original"),
				move("/docs/b", "/docs/a", "original"),
				move("/docs/a", "/docs/b", "original"),
				write("/docs/a", "original"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			disk, _ := newWritableDirFS(t)
			store := NewJSONLHistoryStore(t.TempDir())
			log := newWriteLog(disk, nil, silentLogger(), 1)
			log.SetHistoryRecorder(store)
			t.Cleanup(func() { _ = log.Close(context.Background()) })
			view := newMiddlewareFS(disk, Attribution{Principal: "alice", Actor: "organiser"}, func(ctx context.Context, op WriteOp) (WriteResult, error) {
				hash, err := log.Submit(ctx, op.Attribution, op.persistenceChangeSet())
				return WriteResult{Hash: hash}, err
			})
			sh := shell.NewShell(view)
			sh.SetHistoryBackend(scopedHistory{store: store, roots: []string{"/docs"}})
			run := func(command string) string {
				t.Helper()
				var out, errOut bytes.Buffer
				if code := sh.ExecPipeline(command, &out, &errOut, nil); code != 0 {
					t.Fatalf("%s: exit %d: %s", command, code, &errOut)
				}
				return out.String()
			}
			run("mkdir -p /docs")
			for _, command := range tt.commands {
				run(command)
			}
			out := run("lore history " + tt.path)
			var got []record
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				var r record
				if err := json.Unmarshal([]byte(line), &r); err != nil {
					t.Fatal(err)
				}
				got = append(got, r)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("history has %d records, want %d: %s", len(got), len(tt.want), out)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Fatalf("history[%d] = %+v, want %+v", i, got[i], want)
				}
			}
			// Exercise the same lineage through the public paginated store API.
			query := HistoryQuery{FileKey: tt.path, Roots: []string{"/docs"}, FollowMoves: true, Limit: 1}
			for i, want := range tt.want {
				page, err := store.Query(context.Background(), query)
				if err != nil || len(page.Records) != 1 {
					t.Fatalf("page %d: %+v, %v", i, page, err)
				}
				r := page.Records[0]
				if got := (record{Action: r.Action, Target: r.FileKey, From: r.From, Hash: r.ContentHash}); got != want {
					t.Fatalf("page %d = %+v, want %+v", i, got, want)
				}
				if (page.NextCursor == "") != (i == len(tt.want)-1) {
					t.Fatalf("page %d: unexpected cursor %q", i, page.NextCursor)
				}
				query.Cursor = page.NextCursor
			}
		})
	}
}
