package cmds_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

// A skill document is a template over the calling session, so the examples an
// agent copies name mounts, files, and publish targets that exist for it.
func runSkillDoc(t *testing.T, name, doc string, configure func(*shell.Shell)) string {
	t.Helper()
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddDir("/backend")
	fs.AddFile("/backend/.hidden.md", "secret\n")
	fs.AddFile("/backend/data.csv", "a,b\n")
	fs.AddDir("/backend/research")
	fs.AddFile("/backend/research/notes.md", "# notes\n")
	fs.AddDir("/notes")
	fs.AddFile("/notes/api.md", "# api\n")
	cmds.RegisterTemplatedSkill(name, "test skill", doc)
	sh := shell.NewShell(fs)
	if configure != nil {
		configure(sh)
	}
	var out, errOut bytes.Buffer
	if code := sh.ExecPipeline(name, &out, &errOut, nil); code != 0 {
		t.Fatalf("%s exit=%d stderr=%q", name, code, errOut.String())
	}
	return out.String()
}

func TestSkillTemplateRendersSessionMountsAndPublishSyntax(t *testing.T) {
	doc := "port={{.Port}} mount={{.Mount}} file={{.File}} mounts={{join .Mounts \",\"}} " +
		"publish={{.PublishDocset}} writable={{join .Writable \",\"}} sub={{sub .Mount \"x.md\"}}\n"
	out := runSkillDoc(t, "tpl-session", doc, func(sh *shell.Shell) {
		sh.SetAdvertisedSSHPort(2299)
		sh.SetDocsets([]cmds.DocsetInfo{
			{Name: "notes", Paths: []string{"/notes"}, Grants: []string{"ro"}},
			{Name: "backend", Paths: []string{"/backend"}, Grants: []string{"rw"}, Writable: true},
			// Alias rows must not become mounts.
			{Name: "backend", Paths: []string{"/be"}, AliasTarget: "/backend", Grants: []string{"rw"}, Writable: true},
		})
		sh.SetPublishTargets([]cmds.PublishTarget{{Name: "backend", InboxPath: "/backend/inbox"}})
	})
	// The example file skips the hidden entry and the non-Markdown file at the
	// mount root in favour of the nested Markdown file.
	want := "port=2299 mount=/backend file=/backend/research/notes.md mounts=/backend,/notes " +
		"publish=backend writable=/backend sub=/backend/x.md\n"
	if out != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", out, want)
	}
}

func TestSkillTemplatePrefersMountContainingCwd(t *testing.T) {
	out := runSkillDoc(t, "tpl-cwd", "{{.Mount}} {{.File}}\n", func(sh *shell.Shell) {
		sh.SetDocsets([]cmds.DocsetInfo{
			{Name: "backend", Paths: []string{"/backend"}},
			{Name: "notes", Paths: []string{"/notes"}},
		})
		sh.SetCwd("/notes")
	})
	if out != "/notes /notes/api.md\n" {
		t.Fatalf("rendered %q", out)
	}
}

func TestSkillTemplateResolvesAliasCwdToCanonicalMount(t *testing.T) {
	// A session that has cd'd into an alias belongs to the canonical mount
	// behind it, not to the first mount in sorted order.
	out := runSkillDoc(t, "tpl-alias-cwd", "{{.Mount}} {{join .Mounts \",\"}} {{writable \"/notes/x\"}} {{writable \"/backend\"}}\n", func(sh *shell.Shell) {
		sh.SetDocsets([]cmds.DocsetInfo{
			{Name: "backend", Paths: []string{"/backend"}},
			{Name: "notes", Paths: []string{"/notes"}, Writable: true},
			{Name: "notes", Paths: []string{"/n"}, AliasTarget: "/notes", Writable: true},
		})
		sh.SetCwd("/n/sub")
	})
	if out != "/notes /backend,/notes true false\n" {
		t.Fatalf("rendered %q", out)
	}
}

func TestSkillTemplateStandaloneShellUsesPlaceholders(t *testing.T) {
	// No host: no port, no docsets, no publish targets. The document must still
	// render with visible placeholders rather than invented paths. The walk is
	// breadth-first, so the shallowest Markdown file wins.
	out := runSkillDoc(t, "tpl-standalone", "{{.Port}} {{.Mount}} {{.File}} [{{.PublishDocset}}]\n", nil)
	if out != "<port> / /notes/api.md []\n" {
		t.Fatalf("rendered %q", out)
	}
}

func TestSkillTemplateEmptyMountFallsBackToPlaceholderFile(t *testing.T) {
	out := runSkillDoc(t, "tpl-empty", "{{.File}}\n", func(sh *shell.Shell) {
		sh.SetDocsets([]cmds.DocsetInfo{{Name: "void", Paths: []string{"/void"}}})
	})
	if out != "/void/<file>\n" {
		t.Fatalf("rendered %q", out)
	}
}

func TestSkillDocumentThatIsNotATemplateIsServedVerbatim(t *testing.T) {
	// An embedded document with a template mistake must be emitted unchanged
	// instead of disappearing.
	doc := "literal {{ not a template\n"
	if out := runSkillDoc(t, "tpl-raw", doc, nil); out != doc {
		t.Fatalf("rendered %q, want verbatim %q", out, doc)
	}
	doc = "{{.NoSuchField}}\n"
	if out := runSkillDoc(t, "tpl-badfield", doc, nil); out != doc {
		t.Fatalf("rendered %q, want verbatim %q", out, doc)
	}
}

func TestRuntimeSkillIsNeverTemplated(t *testing.T) {
	// A runtime skill from --skills-dir may contain valid template actions
	// that are meant literally; RegisterSkill must not interpret them.
	doc := "port={{.Port}} {{if true}}kept{{end}}\n"
	cmds.RegisterSkill("raw-runtime", "runtime skill", doc)
	var out bytes.Buffer
	sh := shell.NewShell(testFS())
	sh.SetAdvertisedSSHPort(2299)
	if code := sh.ExecPipeline("raw-runtime", &out, &bytes.Buffer{}, nil); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if out.String() != doc {
		t.Fatalf("rendered %q, want verbatim %q", out.String(), doc)
	}
}

func TestPublishUsageExampleNamesARealTarget(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetPublishTargets([]cmds.PublishTarget{{Name: "backend", InboxPath: "/backend/inbox"}})
	var out bytes.Buffer
	if code := sh.ExecPipeline("publish", &out, &bytes.Buffer{}, nil); code != 0 {
		t.Fatalf("publish exit=%d", code)
	}
	if !strings.Contains(out.String(), "publish /backend/topic.md") || strings.Contains(out.String(), "/knowledge/") {
		t.Fatalf("usage = %q", out.String())
	}
}
