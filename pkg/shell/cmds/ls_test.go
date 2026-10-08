package cmds_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/shell"
)

func TestLs(t *testing.T) {
	fs := testFS()
	out, _, code := execCmd(t, fs, "ls /docs")
	if code != 0 {
		t.Fatalf("ls failed with code %d", code)
	}
	if !strings.Contains(out, "readme.md") {
		t.Error("ls /docs should list readme.md")
	}
}

// TestLsHelpFlags runs every flag that help lists for ls against a fixture
// folder. None may be mistaken for a path name.
func TestLsHelpFlags(t *testing.T) {
	fs := testFS()
	help, _, _ := execCmd(t, fs, "help")
	m := regexp.MustCompile(`(?m)^\s*ls \[([^\]]+)\]`).FindStringSubmatch(help)
	if m == nil {
		t.Fatalf("help has no ls line:\n%s", help)
	}
	for _, flag := range strings.Split(m[1], "|") {
		out, errOut, code := execCmd(t, fs, "ls "+flag+" /docs")
		if code != 0 || errOut != "" {
			t.Errorf("ls %s /docs: exit %d, stderr %q", flag, code, errOut)
		}
		if out == "" {
			t.Errorf("ls %s /docs printed nothing", flag)
		}
	}
}

func TestLsUnsupportedFlag(t *testing.T) {
	fs := testFS()
	for _, cmd := range []string{"ls -z /docs", "ls -lz /docs", "ls --bogus /docs"} {
		out, errOut, code := execCmd(t, fs, cmd)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2", cmd, code)
		}
		if out != "" {
			t.Errorf("%s: printed %q on stdout, want nothing", cmd, out)
		}
		if !strings.Contains(errOut, "unsupported flag") {
			t.Errorf("%s: stderr %q should name the unsupported flag", cmd, errOut)
		}
	}
	if _, errOut, _ := execCmd(t, fs, "ls -lz /docs"); !strings.Contains(errOut, "-z") {
		t.Errorf("stderr %q should name -z", errOut)
	}
}

func TestLsRecursive(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddFile("/p/a.md", "a\n")
	fs.AddFile("/p/sub/b.md", "b\n")
	fs.AddFile("/p/sub/deep/c.md", "c\n")
	assertOutput(t, fs, "ls -R /p", "/p:\na.md\nsub/\n\n/p/sub:\nb.md\ndeep/\n\n/p/sub/deep:\nc.md")
	assertOutput(t, fs, "cd /p && ls -R", "/p:\na.md\nsub/\n\n/p/sub:\nb.md\ndeep/\n\n/p/sub/deep:\nc.md")
	assertOutput(t, fs, "cd /p && ls -R sub", "sub:\nb.md\ndeep/\n\nsub/deep:\nc.md")
}

func TestLsSortAndFormat(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddFile("/s/small.md", "x")
	fs.AddFile("/s/big.md", strings.Repeat("x", 2048))
	fs.AddFile("/s/mid.md", strings.Repeat("x", 100))
	fs.Files["/s/mid.md"].FileModTime = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	fs.Files["/s/small.md"].FileModTime = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)

	assertOutput(t, fs, "ls -S /s", "big.md\nmid.md\nsmall.md")
	assertOutput(t, fs, "ls -t /s", "mid.md\nbig.md\nsmall.md")
	assertOutput(t, fs, "ls -1 /s", "small.md\nbig.md\nmid.md")
	assertOutput(t, fs, "ls -d /s", "/s/")

	out, _, _ := execCmd(t, fs, "ls -lhS /s")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 ||
		!strings.Contains(lines[0], "2.0K") || !strings.HasSuffix(lines[0], " big.md") {
		t.Errorf("ls -lhS /s: got %q", out)
	}
	out, _, _ = execCmd(t, fs, "ls -ldF /s")
	if !strings.HasPrefix(out, "dr-xr-xr-x") || !strings.HasSuffix(strings.TrimSpace(out), " /s/") {
		t.Errorf("ls -ldF /s: got %q", out)
	}
}

func TestLsSortsOperands(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddFile("/s/small.md", "x")
	fs.AddFile("/s/big.md", strings.Repeat("x", 2048))
	fs.AddDir("/older")
	fs.AddDir("/newer")
	fs.Files["/older"].FileModTime = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	fs.Files["/newer"].FileModTime = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	assertOutput(t, fs, "ls -S /s/small.md /s/big.md", "big.md\nsmall.md")
	assertOutput(t, fs, "ls -td /older /newer", "/newer/\n/older/")
}

func TestLsDirOperandTrailingSlash(t *testing.T) {
	fs := testFS()
	assertOutput(t, fs, "ls -d /", "/")
	assertOutput(t, fs, "ls -d /docs/", "/docs/")
	out, _, _ := execCmd(t, fs, "ls -ldF /docs/")
	if !strings.HasSuffix(strings.TrimSpace(out), " /docs/") {
		t.Errorf("ls -ldF /docs/: got %q", out)
	}
}

func TestLsRecursiveJSONIsAStream(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddFile("/p/a.md", "a\n")
	fs.AddFile("/p/sub/b.md", "b\nb\n")
	sh := shell.NewShell(fs)
	sh.SetFacts(analytics.NewContentFacts(fs))

	var out, errOut bytes.Buffer
	if code := sh.Exec("ls -R --json /p", &out, &errOut, nil); code != 0 {
		t.Fatalf("ls -R --json exited %d: %s", code, errOut.String())
	}
	dec := json.NewDecoder(&out)
	var paths []string
	for dec.More() {
		var facts analytics.DocScalars
		if err := dec.Decode(&facts); err != nil {
			t.Fatalf("ls -R --json output is not a JSON stream: %v", err)
		}
		paths = append(paths, facts.Path)
	}
	if !slices.Contains(paths, "/p/a.md") || !slices.Contains(paths, "/p/sub/b.md") {
		t.Errorf("ls -R --json paths = %v, want /p/a.md and /p/sub/b.md", paths)
	}
}
