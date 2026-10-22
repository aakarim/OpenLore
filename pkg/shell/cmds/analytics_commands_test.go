package cmds_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func newAnalyticsService(t *testing.T, fs vfs.FileSystem) *analytics.Service {
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

func TestAnalyticsShowFormatsTextWithoutChangingJSON(t *testing.T) {
	fs := newMapFS()
	service := newAnalyticsService(t, fs)
	service.Registry().SetPaused(false)
	stamp := time.Date(2026, 10, 10, 12, 12, 22, 421936657, time.UTC)
	var never *time.Time
	if err := service.RegisterAggregations([]analytics.Aggregation{{
		Name: "formatting",
		Compute: func(context.Context, analytics.EventSource, analytics.ContentFacts, analytics.Params) (analytics.Table, error) {
			return analytics.Table{
				Columns: []string{"path", "bytes", "p50_ms", "error_rate", "reads_per_ktok", "last_read_at", "last_seen", "count"},
				Rows: [][]any{
					{"/large.md", float64(1923962), 1529975.6, 0.49544548051528053, 1.23456, &stamp, stamp, 18553},
					{"2026-10-10T12:12:22.421936657Z", float64(-1923962), 0.4, 8.312766402664672e-05, nil, never, nil, 0},
				},
			}, nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	sh := shell.NewShell(fs)
	sh.SetAnalytics(service)
	sh.SetAnalyticsAuthorizer(func() error { return nil })
	for _, flags := range []string{"--fresh", "", "--fresh --json", "--json"} {
		t.Run(flags, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := sh.Exec("analytics show formatting "+flags, &out, &errOut, nil); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, errOut.String())
			}
			if strings.Contains(flags, "--json") {
				var result analytics.Materialized
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				want := [][]any{
					{"/large.md", float64(1923962), 1529975.6, 0.49544548051528053, 1.23456, "2026-10-10T12:12:22.421936657Z", "2026-10-10T12:12:22.421936657Z", float64(18553)},
					{"2026-10-10T12:12:22.421936657Z", float64(-1923962), 0.4, 8.312766402664672e-05, nil, nil, nil, float64(0)},
				}
				if !reflect.DeepEqual(result.Table.Rows, want) {
					t.Fatalf("JSON rows=%v, want %v", result.Table.Rows, want)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			want := []string{
				"/large.md 1923962 1529976 0.495 1.235 2026-10-10T12:12:22Z 2026-10-10T12:12:22Z 18553",
				"2026-10-10T12:12:22.421936657Z -1923962 0 0 - - - 0",
			}
			if len(lines) != 4 {
				t.Fatalf("unexpected text table: %q", out.String())
			}
			for i, row := range want {
				if got := strings.Join(strings.Fields(lines[i+2]), " "); got != row {
					t.Errorf("row=%q, want %q", got, row)
				}
			}
		})
	}
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

// denyReadFS lists and stats a file but refuses to read it, as a file-policy
// layer can.
type denyReadFS struct {
	*mapFS
	denied string
}

func (f denyReadFS) ReadFile(p string) ([]byte, error) {
	if p == f.denied {
		return nil, errors.New("access denied: " + p)
	}
	return f.mapFS.ReadFile(p)
}

func TestAnalyticsSizeViewsSkipUnreadableFiles(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/docs/big.md", strings.Repeat("x", 400))
	fs.AddFile("/docs/small.md", "one\n")
	fs.AddFile("/generate.go", "package docs\n")
	sh := shell.NewShell(fs)
	sh.SetAnalytics(newAnalyticsService(t, denyReadFS{fs, "/generate.go"}))
	sh.SetAnalyticsAuthorizer(func() error { return nil })

	for _, command := range []string{"analytics show largest-docs --fresh", "analytics show tree-size --fresh --param depth=0"} {
		var out, errOut bytes.Buffer
		if code := sh.Exec(command, &out, &errOut, nil); code != 0 {
			t.Fatalf("%s exited %d: %q", command, code, errOut.String())
		}
		got := out.String()
		if !strings.Contains(got, "\n/docs/big.md ") || !strings.Contains(got, "\n/docs/small.md ") || strings.Contains(got, "\n/generate.go ") {
			t.Fatalf("%s rows: %q", command, got)
		}
		if !strings.Contains(got, "Skipped 1 unreadable path: /generate.go") {
			t.Fatalf("%s did not report the skipped path: %q", command, got)
		}
	}

	// A root that cannot be read is still an error, not an empty result.
	var out, errOut bytes.Buffer
	if code := sh.Exec("analytics show tree-size --fresh --param path=/generate.go", &out, &errOut, nil); code == 0 || !strings.Contains(errOut.String(), "access denied: /generate.go") {
		t.Fatalf("unreadable root: %d %q %q", code, out.String(), errOut.String())
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
