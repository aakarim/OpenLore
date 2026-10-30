package openlore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

const folderIndexFixture = "---\ntitle: Handbook\n---\n# Hand-written\r\n\x00<!-- lore:index -->old<!-- /lore:index -->\r\nFooter\x00"

func folderIndexServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, root := bootRulesServer(t, serverAuth(map[string]config.DocsetSpec{
		"docs": docset("/docs", nil),
	}, nil), nil)
	return s, root
}

func folderIndexWrite(t *testing.T, s *Server, target, body string) WriteResult {
	t.Helper()
	result, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), writeCSBytes(target, body))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFolderIndexMaterializedBeforeReturn(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", folderIndexFixture)
	doc := "---\ntitle: Runbook\ndescription: Restart the service\n---\nBody\n"
	result := folderIndexWrite(t, s, "/docs/runbook.md", doc)
	if result.Hash != folderIndexHash([]byte(doc)) {
		t.Fatalf("write hash = %q", result.Hash)
	}
	sh := s.buildSessionShell(testIdentity("alice"))
	var out, stderr bytes.Buffer
	sh.ExecPipeline("cat /docs/index.md", &out, &stderr, nil)
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	want := strings.Replace(folderIndexFixture, "-->old<!--", "-->\n- [Runbook](runbook.md) — Restart the service\n<!--", 1)
	if out.String() != want {
		t.Fatalf("cat index = %q; want %q", out.String(), want)
	}
	// A normal read sees both title/description and all original prefix/suffix
	// bytes immediately, without a second write or asynchronous refresh.
	folderIndexWrite(t, s, "/docs/runbook.md", "---\ntitle: Updated\ndescription: New description\n---\n")
	folderIndexWrite(t, s, "/docs/plain.md", "No frontmatter")
	index, err := s.merge.ReadFile("/docs/index.md")
	if err != nil || !strings.Contains(string(index), "[Updated](runbook.md) — New description") || !strings.Contains(string(index), "## Unindexed\n\n- [plain.md](plain.md)") || bytes.Contains(index, []byte("Runbook")) {
		t.Fatalf("updated index=%q err=%v", index, err)
	}
	if _, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), vfs.ChangeSet{Target: "/docs/runbook.md", Action: vfs.ChangeActionRemove}); err != nil {
		t.Fatal(err)
	}
	index, _ = s.merge.ReadFile("/docs/index.md")
	if bytes.Contains(index, []byte("runbook.md")) {
		t.Fatalf("deleted document remains: %s", index)
	}
}

func TestFolderIndexJournalSeparatesMaintenanceAndSkipsUnchangedProjection(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	folderIndexWrite(t, s, "/docs/a.md", "---\ntitle: A\n---\nfirst body")
	folderIndexWrite(t, s, "/docs/a.md", "---\ntitle: A\n---\nsecond body")
	cursor, err := OpenHistoryCursor(s.historyPath, HistoryPosition{})
	if err != nil {
		t.Fatal(err)
	}
	for i, wantLeaves := range []int{1, 2, 1} {
		record, ok, err := cursor.Next(context.Background())
		if err != nil || !ok || len(record.Leaves) != wantLeaves {
			t.Fatalf("commit %d leaves=%+v ok=%t err=%v", i, record.Leaves, ok, err)
		}
		for j, leaf := range record.Leaves {
			wantGenerated := i == 1 && j == 1
			if leaf.Generated != wantGenerated || record.ChangeSet.Leaves()[j].Write.Generated != wantGenerated {
				t.Fatalf("commit %d leaf %d: generated metadata lost: %+v", i, j, record)
			}
		}
	}
}

func TestFolderIndexMoveUpdatesBothFolders(t *testing.T) {
	s, _ := folderIndexServer(t)
	if err := s.buildSessionFS(testIdentity("alice")).(vfs.WritableFS).Mkdir("/docs/other"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/docs/index.md", "/docs/other/index.md"} {
		folderIndexWrite(t, s, p, "<!-- lore:index --><!-- /lore:index -->")
	}
	folderIndexWrite(t, s, "/docs/a.md", "---\ntitle: Move me\n---\n")
	sh := s.buildSessionShell(testIdentity("alice"))
	var out, stderr bytes.Buffer
	sh.ExecPipeline("mv /docs/a.md /docs/other/a.md", &out, &stderr, nil)
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	before, _ := s.merge.ReadFile("/docs/index.md")
	after, _ := s.merge.ReadFile("/docs/other/index.md")
	if bytes.Contains(before, []byte("Move me")) || !bytes.Contains(after, []byte("[Move me](a.md)")) {
		t.Fatalf("source=%q destination=%q", before, after)
	}
}

func TestFolderIndexOptInAndMalformedMarkers(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", "# Manual index\r\n")
	folderIndexWrite(t, s, "/docs/a.md", "hello")
	index, _ := s.merge.ReadFile("/docs/index.md")
	if string(index) != "# Manual index\r\n" {
		t.Fatalf("non-opted-in index changed: %q", index)
	}
	for _, body := range []string{"<!-- lore:index -->", "<!-- /lore:index --><!-- lore:index -->", "<!-- lore:index --><!-- lore:index --><!-- /lore:index -->"} {
		if _, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), writeCSBytes("/docs/index.md", body)); err == nil {
			t.Fatalf("accepted malformed markers %q", body)
		}
	}
	// Opting in materializes existing documents, not just future additions.
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	index, _ = s.merge.ReadFile("/docs/index.md")
	if !bytes.Contains(index, []byte("[a.md](a.md)")) {
		t.Fatalf("opt-in did not backfill: %q", index)
	}
}

func TestFolderIndexGeneratedLeafPassesAdmissionRules(t *testing.T) {
	auth := serverAuth(map[string]config.DocsetSpec{"docs": docset("/docs", nil)}, map[string]rules.RuleSpec{
		"index-size": {Match: []string{"**/index.md"}, Use: "size/lines", With: map[string]any{"max": 2}},
	})
	s, root := bootRulesServer(t, auth, nil)
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	_, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), writeCSBytes("/docs/a.md", "---\ntitle: A\n---\n"))
	if err == nil {
		t.Fatal("generated index bypassed size rule")
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "a.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected batch wrote content: %v", err)
	}
}

func TestFolderIndexDeferredBatchAndStaleReplay(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	if err := s.RegisterPlugin(deferMWProvider{gatedPrefix: "/docs/held.md", ref: "held"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), writeCSBytes("/docs/held.md", "---\ntitle: Held\n---\n"))
	var pending *vfs.PendingChangeError
	if !errors.As(err, &pending) || len(pending.ChangeSet.Leaves()) != 2 {
		t.Fatalf("held batch lost generated index: %v", err)
	}
	folderIndexWrite(t, s, "/docs/new.md", "---\ntitle: New\n---\n")
	if _, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "alice"}, pending.ChangeSet); err == nil {
		t.Fatal("stale held projection overwrote current index")
	}
	if _, err := s.merge.Stat("/docs/held.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale held batch partly committed: %v", err)
	}
	index, _ := s.merge.ReadFile("/docs/index.md")
	if !bytes.Contains(index, []byte("[New](new.md)")) || bytes.Contains(index, []byte("Held")) {
		t.Fatalf("stale replay damaged index: %q", index)
	}
}

func TestFolderIndexLateOptInRejectsUnexpandedWrite(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	// Simulate an old admitted write, or an internal producer that did not use
	// admission. It must not commit stale materialized state after opt-in.
	_, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "alice"}, writeCSBytes("/docs/late.md", "late"))
	if err == nil || !strings.Contains(err.Error(), "resubmit") {
		t.Fatalf("unexpanded write = %v", err)
	}
}

func TestFolderIndexRenderSafeAndDeterministic(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/z.md", "---\ntitle: Z\n---\n")
	folderIndexWrite(t, s, "/docs/a b.md", "---\ntitle: '[A] <script>'\ndescription: |\n  first\n  second\n---\n")
	folderIndexWrite(t, s, "/docs/bad.md", "---\n: [\n---\n")
	folderIndexWrite(t, s, "/docs/javascript:x.md", "body")
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	index, err := s.merge.ReadFile("/docs/index.md")
	if err != nil {
		t.Fatal(err)
	}
	got := string(index)
	if !strings.Contains(got, `- [\[A\] &lt;script&gt;](a%20b.md) — first second`) || strings.Index(got, "a%20b.md") > strings.Index(got, "z.md") || !strings.Contains(got, "## Unindexed\n\n- [bad.md](bad.md)") || !strings.Contains(got, "(./javascript:x.md)") {
		t.Fatalf("unsafe/unsorted rendering: %q", got)
	}
}

func TestFolderIndexExplicitWriteHasOneFinalLeafAndHash(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/a.md", "plain")
	cs := writeCSBytes("/docs/index.md", "<!-- lore:index -->stale<!-- /lore:index -->")
	result := folderIndexWrite(t, s, cs.Target, string(cs.Write.Bytes))
	materialized, _ := s.merge.ReadFile(cs.Target)
	if bytes.Contains(materialized, []byte("stale")) || !bytes.Contains(materialized, []byte("[a.md](a.md)")) || result.Hash != folderIndexHash(materialized) {
		t.Fatalf("returned hash %s != final materialized hash %s", result.Hash, folderIndexHash(materialized))
	}
}

func TestFolderIndexGeneratedLeafCanBeDeferred(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	if err := s.RegisterPlugin(deferMWProvider{gatedPrefix: "/docs/index.md", ref: "index-review"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), writeCSBytes("/docs/new.md", "plain"))
	var pending *vfs.PendingChangeError
	if !errors.As(err, &pending) || len(pending.ChangeSet.Leaves()) != 2 {
		t.Fatalf("generated index did not reach middleware: %v", err)
	}
	if _, err := s.merge.Stat("/docs/new.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deferred index committed source content: %v", err)
	}
	if _, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "alice"}, pending.ChangeSet); err != nil {
		t.Fatalf("fresh deferred batch could not replay: %v", err)
	}
	index, _ := s.merge.ReadFile("/docs/index.md")
	if !bytes.Contains(index, []byte("[new.md](new.md)")) {
		t.Fatalf("replayed batch did not materialize index: %q", index)
	}
}

func TestFolderIndexMovingOptedInIndexKeepsMoveAndFinalProjection(t *testing.T) {
	s, _ := folderIndexServer(t)
	if err := s.buildSessionFS(testIdentity("alice")).(vfs.WritableFS).Mkdir("/docs/other"); err != nil {
		t.Fatal(err)
	}
	folderIndexWrite(t, s, "/docs/a.md", "source document")
	folderIndexWrite(t, s, "/docs/other/b.md", "destination document")
	folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
	original, _ := s.merge.ReadFile("/docs/index.md")
	sh := s.buildSessionShell(testIdentity("alice"))
	var out, stderr bytes.Buffer
	sh.ExecPipeline("mv /docs/index.md /docs/other/index.md", &out, &stderr, nil)
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	index, _ := s.merge.ReadFile("/docs/other/index.md")
	if !bytes.Contains(index, []byte("[b.md](b.md)")) || bytes.Contains(index, []byte("a.md")) {
		t.Fatalf("moved index did not get destination projection: %q", index)
	}
	page, err := s.history.Query(context.Background(), HistoryQuery{FileKey: "/docs/other/index.md", Roots: []string{"/docs"}, FollowMoves: true})
	if err != nil || len(page.Records) != 3 {
		t.Fatalf("moved index provenance: %+v, %v", page, err)
	}
	if page.Records[0].Action != "write" || page.Records[0].ContentHash != hashOf(index) || page.Records[1].Action != "move" || page.Records[1].ContentHash != hashOf(original) || page.Records[1].From != "/docs/index.md" || page.Records[2].Action != "write" || page.Records[2].ContentHash != hashOf(original) {
		t.Fatalf("index refresh must be distinct from the hash-preserving move: %+v", page.Records)
	}
}

func TestFolderIndexUnchangedMoveRetainsDestinationCAS(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "overwrite"}[exists], func(t *testing.T) {
			s, root := folderIndexServer(t)
			if err := os.Mkdir(filepath.Join(root, "docs", "other"), 0o755); err != nil {
				t.Fatal(err)
			}
			folderIndexWrite(t, s, "/docs/index.md", "<!-- lore:index --><!-- /lore:index -->")
			original, _ := s.merge.ReadFile("/docs/index.md")
			if exists {
				folderIndexWrite(t, s, "/docs/other/index.md", string(original))
			}
			if err := s.RegisterPlugin(deferMWProvider{gatedPrefix: "/docs/other/index.md", ref: "move"}); err != nil {
				t.Fatal(err)
			}
			_, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), vfs.ChangeSet{
				Changes: []vfs.Change{
					{Target: "/docs/index.md", Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{Opts: vfs.RemoveOpts{Expected: &vfs.TreeSnapshot{Root: "/docs/index.md", Ops: []vfs.TreeOp{{RelPath: ".", Kind: "file", Hash: folderIndexHash(original), Size: int64(len(original))}}}}}},
					{Target: "/docs/other/index.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: original}},
				}, Moves: []vfs.Move{{From: 0, To: 1}},
			})
			var pending *vfs.PendingChangeError
			if !errors.As(err, &pending) {
				t.Fatalf("not deferred: %v", err)
			}
			// An external edit after admission must not be silently overwritten,
			// even when the moved section needs no regeneration at its new path.
			if err := os.WriteFile(filepath.Join(root, "docs", "other", "index.md"), []byte("concurrent"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "alice"}, pending.ChangeSet); err == nil {
				t.Fatal("stale move overwrote destination")
			}
			if source, err := s.merge.ReadFile("/docs/index.md"); err != nil || !bytes.Equal(source, original) {
				t.Fatalf("rejected move removed source: %q %v", source, err)
			}
		})
	}
}

func TestFolderIndexIgnoresPresentationTransforms(t *testing.T) {
	s, _ := folderIndexServer(t)
	folderIndexWrite(t, s, "/docs/index.md", folderIndexFixture)
	s.contentTransforms = append(s.contentTransforms, func(_ string, b []byte) []byte { return append(b, []byte("presentation banner")...) })
	_, err := s.AdmitChangeSet(context.Background(), testIdentity("alice"), writeCSBytes("/docs/a.md", "---\ntitle: A\n---\n"))
	if err != nil {
		t.Fatalf("presentation bytes broke raw index CAS: %v", err)
	}
	index, err := s.merge.ReadFile("/docs/index.md")
	want := strings.Replace(folderIndexFixture, "-->old<!--", "-->\n- [A](a.md)\n<!--", 1)
	if err != nil || string(index) != want {
		t.Fatalf("stored index contains presentation bytes: %q %v", index, err)
	}
}
