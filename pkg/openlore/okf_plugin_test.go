package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// runOKF composes the plugin's single admission middleware around a terminal
// handler that records whether it was reached, and drives one ChangeSet through
// it. It returns the terminal-reached flag and the chain error.
func runOKF(p *okfPlugin, cs vfs.ChangeSet) (reachedTerminal bool, err error) {
	mws := p.WriteMiddleware()
	if len(mws) != 1 {
		panic("expected exactly one OKF middleware")
	}
	terminal := func(ctx context.Context, op WriteOp) (WriteResult, error) {
		reachedTerminal = true
		return WriteResult{Hash: "ok"}, nil
	}
	h := mws[0](terminal)
	_, err = h(context.Background(), NewWriteOp(Actor{}, cs))
	return reachedTerminal, err
}

const validDoc = "---\ntype: Note\n---\nbody\n"
const invalidDoc = "no frontmatter\n"

// docset builds a single-root docset spec with the given OKF config.
func docset(root string, okf *config.OKFDocsetConfig) config.DocsetSpec {
	return config.DocsetSpec{
		Paths: []config.PathMapping{{Source: root, Display: root}},
		OKF:   okf,
	}
}

// pluginWith constructs an OKF plugin over the given docsets.
func pluginWith(docsets map[string]config.DocsetSpec) *okfPlugin {
	return newOKF(docsets, nil)
}

// docsWithOKF is a docset at /docs with default OKF (enforce, *.md).
func docsWithOKF() map[string]config.DocsetSpec {
	return map[string]config.DocsetSpec{"docs": docset("/docs", &config.OKFDocsetConfig{})}
}

func TestOKFPlugin_RejectsNonConformantMarkdown(t *testing.T) {
	p := pluginWith(docsWithOKF()) // defaults: *.md, enforce=true
	reached, err := runOKF(p, writeCSBytes("/docs/bad.md", invalidDoc))
	if err == nil {
		t.Fatal("expected rejection error for non-conformant .md write")
	}
	if reached {
		t.Fatal("terminal handler should not be reached when a write is rejected")
	}
}

func TestOKFPlugin_RejectsForbiddenLaterBatchLeaf(t *testing.T) {
	cs := vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/docs/good.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte(validDoc)}},
		{Target: "/docs/bad.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte(invalidDoc)}},
	}}
	reached, err := runOKF(pluginWith(docsWithOKF()), cs)
	if err == nil || reached {
		t.Fatalf("later invalid leaf was not rejected: reached=%v err=%v", reached, err)
	}
}

func TestOKFPlugin_AllowsConformantMarkdown(t *testing.T) {
	p := pluginWith(docsWithOKF())
	reached, err := runOKF(p, writeCSBytes("/docs/good.md", validDoc))
	if err != nil {
		t.Fatalf("unexpected error for conformant .md write: %v", err)
	}
	if !reached {
		t.Fatal("terminal handler should be reached for a conformant write")
	}
}

func TestOKFPlugin_IgnoresNonMatchingFiles(t *testing.T) {
	p := pluginWith(docsWithOKF())
	// A .txt file with garbage frontmatter is not an OKF concern by default.
	reached, err := runOKF(p, writeCSBytes("/docs/notes.txt", invalidDoc))
	if err != nil {
		t.Fatalf("non-matching file should pass through, got %v", err)
	}
	if !reached {
		t.Fatal("terminal handler should be reached for a non-matching file")
	}
}

func TestOKFPlugin_NonEnforcingAllowsButPasses(t *testing.T) {
	p := pluginWith(map[string]config.DocsetSpec{
		"docs": docset("/docs", &config.OKFDocsetConfig{Enforce: boolPtr(false)}),
	})
	reached, err := runOKF(p, writeCSBytes("/docs/bad.md", invalidDoc))
	if err != nil {
		t.Fatalf("non-enforcing plugin must not reject, got %v", err)
	}
	if !reached {
		t.Fatal("terminal handler should be reached in non-enforcing mode")
	}
}

func TestOKFPlugin_CustomPatterns(t *testing.T) {
	p := pluginWith(map[string]config.DocsetSpec{
		"docs": docset("/docs", &config.OKFDocsetConfig{Patterns: []string{"*.markdown"}}),
	})

	// .md is no longer matched → passes through unchecked.
	reached, err := runOKF(p, writeCSBytes("/docs/bad.md", invalidDoc))
	if err != nil || !reached {
		t.Fatalf(".md should be ignored with custom patterns (err=%v reached=%v)", err, reached)
	}

	// .markdown is now matched → rejected.
	reached, err = runOKF(p, writeCSBytes("/docs/bad.markdown", invalidDoc))
	if err == nil {
		t.Fatal("expected rejection for non-conformant .markdown write")
	}
	if reached {
		t.Fatal("terminal should not be reached for rejected .markdown write")
	}
}

func TestOKFPlugin_NonWriteActionsPassThrough(t *testing.T) {
	p := pluginWith(docsWithOKF())
	for _, action := range []vfs.ChangeAction{
		vfs.ChangeActionMkdir,
		vfs.ChangeActionMkdirAll,
		vfs.ChangeActionRemove,
		vfs.ChangeActionRemoveAll,
	} {
		reached, err := runOKF(p, vfs.ChangeSet{Target: "/docs/x.md", Action: action})
		if err != nil {
			t.Fatalf("action %q should pass through, got %v", action, err)
		}
		if !reached {
			t.Fatalf("terminal should be reached for action %q", action)
		}
	}
}

func TestOKFPlugin_ReservedFilesLenient(t *testing.T) {
	p := pluginWith(docsWithOKF())
	// index.md without frontmatter is conformant (reserved file).
	reached, err := runOKF(p, writeCSBytes("/docs/index.md", "# Section\n* [x](x.md)\n"))
	if err != nil || !reached {
		t.Fatalf("reserved index.md should pass (err=%v reached=%v)", err, reached)
	}
}

// A write outside any OKF-carrying docset is never validated.
func TestOKFPlugin_PathOutsideDocsetNotValidated(t *testing.T) {
	p := pluginWith(docsWithOKF())
	reached, err := runOKF(p, writeCSBytes("/other/bad.md", invalidDoc))
	if err != nil || !reached {
		t.Fatalf("write outside the OKF docset must pass (err=%v reached=%v)", err, reached)
	}
}

// A docset without OKF config never validates its writes.
func TestOKFPlugin_DocsetWithoutOKFNotValidated(t *testing.T) {
	p := pluginWith(map[string]config.DocsetSpec{
		"adil": docset("/adil", nil),
	})
	reached, err := runOKF(p, writeCSBytes("/adil/bad.md", invalidDoc))
	if err != nil || !reached {
		t.Fatalf("docset without OKF must not validate (err=%v reached=%v)", err, reached)
	}
}

// A nested docset carrying OKF validates only its subtree; the parent docset
// (no OKF) is untouched. This is the include-via-nesting case: /adil is exempt,
// /adil/wiki is enforced.
func TestOKFPlugin_NestedDocsetIncludesSubtree(t *testing.T) {
	p := pluginWith(map[string]config.DocsetSpec{
		"adil":      docset("/adil", nil),
		"adil-wiki": docset("/adil/wiki", &config.OKFDocsetConfig{}),
	})

	// Parent subtree: not validated.
	reached, err := runOKF(p, writeCSBytes("/adil/notes.md", invalidDoc))
	if err != nil || !reached {
		t.Fatalf("/adil (no OKF) must not validate (err=%v reached=%v)", err, reached)
	}

	// Nested subtree: validated (longest matching root wins).
	reached, err = runOKF(p, writeCSBytes("/adil/wiki/page.md", invalidDoc))
	if err == nil {
		t.Fatal("/adil/wiki (OKF) must reject non-conformant write")
	}
	if reached {
		t.Fatal("terminal should not be reached for rejected nested write")
	}
}

// A nested docset without OKF shadows a parent's OKF: the exclude-via-nesting
// case. /adil is enforced, /adil/drafts is exempt.
func TestOKFPlugin_NestedDocsetExcludesSubtree(t *testing.T) {
	p := pluginWith(map[string]config.DocsetSpec{
		"adil":        docset("/adil", &config.OKFDocsetConfig{}),
		"adil-drafts": docset("/adil/drafts", nil),
	})

	// Parent subtree: validated.
	reached, err := runOKF(p, writeCSBytes("/adil/notes.md", invalidDoc))
	if err == nil {
		t.Fatal("/adil (OKF) must reject non-conformant write")
	}
	if reached {
		t.Fatal("terminal should not be reached for rejected parent write")
	}

	// Nested subtree without OKF: exempt (longest matching root has no OKF).
	reached, err = runOKF(p, writeCSBytes("/adil/drafts/page.md", invalidDoc))
	if err != nil || !reached {
		t.Fatalf("/adil/drafts (no OKF) must be exempt (err=%v reached=%v)", err, reached)
	}
}

func TestOKFPlugin_ImplementsProvider(t *testing.T) {
	var _ WriteMiddlewareProvider = pluginWith(docsWithOKF())
}

// Guard against an unexpected error type leaking (should be a plain wrapped
// error, not a PendingChangeError which would be mis-read as "pending").
func TestOKFPlugin_RejectionIsHardError(t *testing.T) {
	p := pluginWith(docsWithOKF())
	_, err := runOKF(p, writeCSBytes("/docs/bad.md", invalidDoc))
	var pce *vfs.PendingChangeError
	if errors.As(err, &pce) {
		t.Fatal("OKF rejection must be a hard error, not a PendingChangeError")
	}
}

// okfExtend runs the plugin's single meta extender against one document.
func okfExtend(p *okfPlugin, absPath, content string) map[string]any {
	exts := p.MetaExtenders()
	if len(exts) != 1 {
		panic("expected exactly one OKF meta extender")
	}
	return exts[0](absPath, []byte(content), nil)
}

func TestOKFMetaExtender_AnnotatesConformant(t *testing.T) {
	p := pluginWith(docsWithOKF())
	got := okfExtend(p, "/docs/good.md", validDoc)
	okfField, ok := got["okf"].(map[string]any)
	if !ok {
		t.Fatalf("expected okf field, got %v", got)
	}
	if okfField["valid"] != true {
		t.Fatalf("expected valid=true, got %v", okfField)
	}
	if _, has := okfField["error"]; has {
		t.Fatalf("conformant doc should carry no error: %v", okfField)
	}
}

func TestOKFMetaExtender_AnnotatesNonConformant(t *testing.T) {
	p := pluginWith(docsWithOKF())
	got := okfExtend(p, "/docs/bad.md", invalidDoc)
	okfField := got["okf"].(map[string]any)
	if okfField["valid"] != false {
		t.Fatalf("expected valid=false, got %v", okfField)
	}
	if okfField["error"] == nil || okfField["error"] == "" {
		t.Fatalf("non-conformant doc should carry an error: %v", okfField)
	}
}

func TestOKFMetaExtender_SilentWhereOKFDoesNotApply(t *testing.T) {
	p := pluginWith(docsWithOKF())
	// Outside the OKF docset: no annotation.
	if got := okfExtend(p, "/other/x.md", invalidDoc); got != nil {
		t.Fatalf("path outside OKF docset must not be annotated, got %v", got)
	}
	// Inside the docset but not matching patterns: no annotation.
	if got := okfExtend(p, "/docs/x.txt", invalidDoc); got != nil {
		t.Fatalf("non-matching file must not be annotated, got %v", got)
	}
}

func TestOKFPlugin_ImplementsMetaExtenderProvider(t *testing.T) {
	var _ MetaExtenderProvider = pluginWith(docsWithOKF())
}

// End-to-end: registerPlugin must collect the okf meta extender onto the server
// so a session shell (via SetMetaExtenders) annotates docs with OKF conformance
// where OKF applies. Runs a real shell over a DirFS.
func TestRegisterPlugin_WiresOKFIntoLoreMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("wiki/good.md", validDoc)
	// Has a parseable frontmatter block (so `lore meta` emits it) but is not
	// OKF-conformant (no `type`), so the extender flags it invalid.
	writeFile("wiki/bad.md", "---\ntitle: No Type\n---\nbody\n")

	docsets := map[string]config.DocsetSpec{
		"wiki": docset("/wiki", &config.OKFDocsetConfig{}),
	}
	s := &Server{grants: newGrantRegistry()}
	if err := s.registerPlugin(newOKF(docsets, nil)); err != nil { // must collect the meta extender
		t.Fatal(err)
	}

	sh := shell.NewShell(NewDirFS(dir, config.FilesConfig{}))
	sh.SetMetaExtenders(s.metaExtenders)
	sh.SetCwd("/wiki")
	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline("lore meta", &out, &errOut, nil); code != 0 {
		t.Fatalf("lore meta exit=%d stderr=%s", code, errOut.String())
	}

	byPath := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad NDJSON %q: %v", line, err)
		}
		byPath[m["path"].(string)] = m
	}

	good, ok := byPath["good.md"]
	if !ok {
		t.Fatalf("good.md missing from output: %s", out.String())
	}
	if okfField, _ := good["okf"].(map[string]any); okfField == nil || okfField["valid"] != true {
		t.Fatalf("good.md should be annotated valid: %v", good["okf"])
	}
	bad := byPath["bad.md"]
	if okfField, _ := bad["okf"].(map[string]any); okfField == nil || okfField["valid"] != false {
		t.Fatalf("bad.md should be annotated invalid: %v", bad["okf"])
	}
}

func TestAnyDocsetHasOKF(t *testing.T) {
	if anyDocsetHasOKF(map[string]config.DocsetSpec{"a": docset("/a", nil)}) {
		t.Fatal("no docset carries OKF; expected false")
	}
	if !anyDocsetHasOKF(docsWithOKF()) {
		t.Fatal("a docset carries OKF; expected true")
	}
}

func TestOKFPlugin_ValidateCommandListsAllErrorsAndAliasWarnings(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, "docs", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good.md", "---\ntype: Note\n---\n[target](target.md) [missing](missing.md)\n")
	write("target.md", validDoc)
	write("invalid.md", invalidDoc)

	docsets := docsWithOKF()
	docset := docsets["docs"]
	docset.Paths = []config.PathMapping{{Source: "/wiki", Display: "/wiki"}}
	docset.Aliases = []string{"/docs"}
	docsets["docs"] = docset
	plugin := pluginWith(docsets)
	sh := shell.NewShell(NewDirFS(dir, config.FilesConfig{}))
	sh.SetCwd("/docs")
	sh.SetValidators(plugin.Validators())

	var out, errOut bytes.Buffer
	code := sh.ExecPipeline("lore validate", &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"invalid.md:1:1: error [okf/concept]",
		"error [openlore/broken-link]",
		"warning [openlore/alias-referrer]",
		"warning [openlore/alias-target]",
		"2 errors, 4 warnings",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestOKFPlugin_ValidateCommandSucceedsForPortableBundle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte(validDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := shell.NewShell(NewDirFS(dir, config.FilesConfig{}))
	sh.SetCwd("/")
	sh.SetValidators(pluginWith(docsWithOKF()).Validators())

	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline("lore validate", &out, &errOut, nil); code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if out.String() != "0 errors, 0 warnings\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRegisterPlugin_ValidateCommandIsCoreAndValidatorsAreSessionLocal(t *testing.T) {
	withOKF := &Server{}
	if err := withOKF.registerPlugin(pluginWith(docsWithOKF())); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.md"), []byte(invalidDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := NewDirFS(dir, config.FilesConfig{})
	withShell := shell.NewShell(fsys)
	withShell.SetValidators(withOKF.validators)
	withoutShell := shell.NewShell(fsys)

	var withOut, withoutOut, errOut bytes.Buffer
	if code := withShell.ExecPipeline("lore", &withOut, &errOut, nil); code != 0 {
		t.Fatalf("lore with plugin exit=%d: %s", code, errOut.String())
	}
	if code := withoutShell.ExecPipeline("lore", &withoutOut, &errOut, nil); code != 0 {
		t.Fatalf("lore without plugin exit=%d: %s", code, errOut.String())
	}
	if !strings.Contains(withOut.String(), "validate") || !strings.Contains(withoutOut.String(), "validate") {
		t.Fatalf("core validate command missing: with plugin:\n%s\nwithout plugin:\n%s", withOut.String(), withoutOut.String())
	}
	withOut.Reset()
	withoutOut.Reset()
	if code := withShell.ExecPipeline("lore validate", &withOut, &errOut, nil); code != 1 {
		t.Fatalf("with plugin exit=%d, want 1: %s", code, withOut.String())
	}
	if code := withoutShell.ExecPipeline("lore validate", &withoutOut, &errOut, nil); code != 0 {
		t.Fatalf("without plugin exit=%d, want 0: %s", code, withoutOut.String())
	}
	if !strings.Contains(withOut.String(), "okf/concept") || withoutOut.String() != "0 errors, 0 warnings\n" {
		t.Fatalf("validators not session-local: with=%q without=%q", withOut.String(), withoutOut.String())
	}
}
