package cmds_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

func newAnalyticsService(t *testing.T, fs *mapFS) *analytics.Service {
	t.Helper()
	disabled := false
	service, err := analytics.New(config.AnalyticsConfig{
		Dir:      t.TempDir(),
		Pipeline: config.AnalyticsPipelineConfig{Enabled: &disabled},
	}, analytics.Deps{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(t.Context()) })
	return service
}

func TestAnalyticsFactsUseShellFileSystem(t *testing.T) {
	globalFS := newMapFS()
	globalFS.AddFile("/doc.md", "global")
	sessionFS := newMapFS()
	sessionFS.AddFile("/doc.md", "one\ntwo\nthree\n")

	sh := shell.NewShell(sessionFS)
	sh.SetAnalytics(newAnalyticsService(t, globalFS))
	for _, command := range []string{"stat --json /doc.md", "ls --json /doc.md"} {
		var out, errOut bytes.Buffer
		if code := sh.Exec(command, &out, &errOut, nil); code != 0 {
			t.Fatalf("%q exited %d: %s", command, code, errOut.String())
		}
		var facts analytics.DocScalars
		if err := json.Unmarshal(out.Bytes(), &facts); err != nil {
			t.Fatalf("%q returned invalid JSON %q: %v", command, out.String(), err)
		}
		if got := facts.Scalars["lines"]; got != 3 {
			t.Fatalf("%q lines = %v, want session-scoped value 3", command, got)
		}
	}

	var out, errOut bytes.Buffer
	if code := sh.Exec("ls -l --stats /doc.md", &out, &errOut, nil); code != 0 {
		t.Fatalf("ls --stats exited %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "       3") {
		t.Fatalf("ls --stats did not report session-scoped line count: %q", out.String())
	}
}

func TestContentFactsDoNotGrantAnalyticsAccess(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/doc.md", "one\ntwo\n")
	sh := shell.NewShell(fs)
	sh.SetFacts(analytics.NewContentFacts(fs))

	var out, errOut bytes.Buffer
	if code := sh.Exec("stat --json /doc.md", &out, &errOut, nil); code != 0 || !strings.Contains(out.String(), `"lines":2`) {
		t.Fatalf("facts command: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := sh.Exec("analytics list", &out, &errOut, nil); code == 0 || !strings.Contains(errOut.String(), "not enabled") {
		t.Fatalf("analytics command: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestAnalyticsReplayReportsParseError(t *testing.T) {
	fs := newMapFS()
	sh := shell.NewShell(fs)
	sh.SetAnalytics(newAnalyticsService(t, fs))
	sh.SetAnalyticsAuthorizer(func() error { return nil })

	var out, errOut bytes.Buffer
	if code := sh.Exec("analytics replay --since definitely-not-a-duration", &out, &errOut, nil); code == 0 {
		t.Fatalf("analytics replay unexpectedly succeeded: %s", out.String())
	}
	if got := errOut.String(); !strings.Contains(got, "analytics replay:") || !strings.Contains(got, "invalid duration") {
		t.Fatalf("analytics replay hid parse error: %q", got)
	}
}

// Embedding only the stable interface deliberately hides optional extensions.
type legacyContext struct{ cmds.CmdContext }

func TestLegacyContextWithoutAnalyticsExtensions(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/doc.md", "hello\n")
	ctx := legacyContext{shell.NewShell(fs)}
	var out, errOut bytes.Buffer
	if code := cmds.CmdCat(ctx, []string{"/doc.md"}, &out, &errOut, nil); code != 0 || out.String() != "hello\n" {
		t.Fatalf("legacy cat: %d %q %q", code, out.String(), errOut.String())
	}
}

func TestAnalyticsGlobalOperationsFailClosed(t *testing.T) {
	sh := shell.NewShell(newMapFS())
	sh.SetAnalytics(newAnalyticsService(t, newMapFS()))
	for _, command := range []string{"list", "show tree-size", "export", "status", "refresh", "replay", "ship", "rebuild --from-remote"} {
		var out, errOut bytes.Buffer
		if code := sh.Exec("analytics "+command, &out, &errOut, nil); code == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "lore:analytics:admin") {
			t.Fatalf("unprivileged %s: %d %q %q", command, code, out.String(), errOut.String())
		}
	}
}

func TestAnalyticsHelpAndDiscovery(t *testing.T) {
	sh := shell.NewShell(newMapFS())
	sh.SetAnalytics(newAnalyticsService(t, newMapFS()))
	sh.SetAnalyticsAuthorizer(func() error { return errors.New("role(s) reader lack capability lore:analytics:admin") })
	run := func(command string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := sh.Exec(command, &out, &errOut, nil)
		return code, out.String(), errOut.String()
	}

	// Usage is available to an identity that cannot run any subcommand.
	for _, command := range []string{"analytics --help", "analytics help", "analytics -h"} {
		if code, out, errOut := run(command); code != 0 || !strings.Contains(out, "show <name>") || !strings.Contains(out, "lore:analytics:admin") || errOut != "" {
			t.Fatalf("%s: %d %q %q", command, code, out, errOut)
		}
	}
	if code, out, errOut := run("analytics list"); code == 0 || out != "" || errOut != "analytics: denied: role(s) reader lack capability lore:analytics:admin\n" {
		t.Fatalf("denial did not carry the authorizer's reason: %d %q %q", code, out, errOut)
	}
	if _, out, _ := run("help"); !strings.Contains(out, "analytics") || !strings.Contains(out, "operator-only") {
		t.Fatalf("help does not list analytics: %q", out)
	}

	sh.SetAnalyticsAuthorizer(func() error { return nil })
	if code, _, errOut := run("analytics tree-size"); code == 0 || !strings.Contains(errOut, "run: analytics show tree-size") {
		t.Fatalf("aggregation used as subcommand: %d %q", code, errOut)
	}
	if code, _, errOut := run("analytics bogus"); code == 0 || strings.Contains(errOut, "analytics show") || !strings.Contains(errOut, "usage: analytics") {
		t.Fatalf("unknown subcommand: %d %q", code, errOut)
	}
}
