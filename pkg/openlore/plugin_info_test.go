package openlore

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
)

func TestPluginInfo_BuiltinsReportNameAndVersion(t *testing.T) {
	cases := []struct {
		plugin       PluginInfoProvider
		name, semver string
	}{
		{pluginWith(docsWithOKF()), "okf", "0.2.0"},
		{NewInboxPlugin(), "inbox", "1.0.0"},
		{&shellexecPlugin{}, "shellexec", "1.0.0"},
	}
	for _, c := range cases {
		got := c.plugin.Info()
		if got.Name != c.name || got.Version != c.semver {
			t.Errorf("Info() = %+v, want {Name:%q Version:%q}", got, c.name, c.semver)
		}
	}
}

// registerPlugin must emit a boot log recording each plugin's name and version.
func TestRegisterPlugin_LogsNameAndVersion(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := &Server{grants: newGrantRegistry(), logger: logger}

	if err := s.registerPlugin(newOKF(map[string]config.DocsetSpec{}, logger)); err != nil {
		t.Fatal(err)
	}
	if err := s.registerPlugin(NewInboxPlugin()); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{`name=okf`, `version=0.2.0`, `name=inbox`, `version=1.0.0`} {
		if !strings.Contains(out, want) {
			t.Errorf("boot log missing %q; got:\n%s", want, out)
		}
	}
}

// A nil logger (as when a Server is constructed directly in tests) must not
// panic during registration.
func TestRegisterPlugin_NilLoggerDoesNotPanic(t *testing.T) {
	s := &Server{grants: newGrantRegistry()}
	if err := s.registerPlugin(NewInboxPlugin()); err != nil { // must not panic
		t.Fatal(err)
	}
}
