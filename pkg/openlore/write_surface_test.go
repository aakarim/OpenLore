package openlore

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// run executes a single shell line and returns (stdout, stderr, exitCode).
func run(sh *shell.Shell, line string) (string, string, int) {
	var out, errb bytes.Buffer
	code := sh.Exec(line, &out, &errb, nil)
	return out.String(), errb.String(), code
}

func newWritableShell(t *testing.T) (*shell.Shell, *DirFS, string) {
	t.Helper()
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	return shell.NewShell(d), d, dir
}

func TestRedirect_OverwriteAndReadBack(t *testing.T) {
	sh, _, _ := newWritableShell(t)

	if _, errs, code := run(sh, "echo hello > /a.md"); code != 0 {
		t.Fatalf("redirect failed: code=%d err=%q", code, errs)
	}
	out, _, _ := run(sh, "cat /a.md")
	if out != "hello\n" {
		t.Fatalf("readback = %q, want %q", out, "hello\n")
	}

	// Overwrite replaces, not appends.
	run(sh, "echo world > /a.md")
	out, _, _ = run(sh, "cat /a.md")
	if out != "world\n" {
		t.Fatalf("after overwrite = %q, want %q", out, "world\n")
	}
}

func TestRedirect_ReadOnly_HardError(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{}) // not SetWriteable → read-only
	sh := shell.NewShell(d)

	out, errs, code := run(sh, "echo nope > /a.md")
	if code == 0 {
		t.Fatalf("redirect on read-only FS should fail; out=%q err=%q", out, errs)
	}
	if !strings.Contains(errs, "read-only") {
		t.Fatalf("expected read-only error, got %q", errs)
	}
	if _, err := os.Stat(d.resolve("/a.md")); !os.IsNotExist(err) {
		t.Fatalf("read-only redirect must not create the file")
	}
}

func TestRedirect_OutsideWriteSurfaceIsStillRedirection(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	if err := os.Mkdir(d.resolve("/docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(d.resolve("/docs/index.md"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	sh := shell.NewShell(newScopedWriteFS(d, rootsAuthz("/docs")))
	sh.SetEnv("P", "/docs")

	for _, command := range []string{
		"cat $P/index.md > /tmp/pi.md",
		"cat $P/index.md>/tmp/pi.md",
	} {
		out, errs, code := run(sh, command)
		if code == 0 {
			t.Fatalf("%q: redirect outside write surface should fail; out=%q err=%q", command, out, errs)
		}
		if !strings.Contains(errs, "redirect: /tmp/pi.md: read-only filesystem") {
			t.Fatalf("%q: expected redirect error, got %q", command, errs)
		}
		if strings.Contains(errs, "command not found") {
			t.Fatalf("%q: redirect target was treated as a command: %q", command, errs)
		}
	}
}

func TestRedirect_DevNullDiscardsWithoutWriteCapability(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{}) // read-only substrate
	sh := shell.NewShell(d)
	sh.SetAllowedActions(nil) // read-only session

	for _, line := range []string{
		"echo discarded > /dev/null",
		"echo discarded >> /dev/null",
		"cat /missing 2>/dev/null",
		"cat /missing 2>>/dev/null",
	} {
		out, errs, code := run(sh, line)
		if strings.HasPrefix(line, "echo") && code != 0 {
			t.Errorf("%q: got code=%d", line, code)
		}
		if strings.HasPrefix(line, "cat") && code == 0 {
			t.Errorf("%q: command failure was suppressed", line)
		}
		if out != "" || errs != "" {
			t.Errorf("%q: got code=%d out=%q err=%q", line, code, out, errs)
		}
	}
	if _, err := os.Stat(d.resolve("/dev/null")); !os.IsNotExist(err) {
		t.Fatal("redirect to /dev/null must not create a virtual file")
	}
}

func TestRedirect_StderrToFile(t *testing.T) {
	sh, d, _ := newWritableShell(t)

	out, errs, code := run(sh, "cat /first-missing 2>/errors.log")
	if code == 0 || out != "" || errs != "" {
		t.Fatalf("stderr redirect: got code=%d out=%q err=%q", code, out, errs)
	}
	first, err := d.ReadFile("/errors.log")
	if err != nil || !strings.Contains(string(first), "first-missing") {
		t.Fatalf("redirected stderr = %q, err=%v", first, err)
	}

	run(sh, "cat /second-missing 2>>/errors.log")
	got, err := d.ReadFile("/errors.log")
	if err != nil || !strings.Contains(string(got), "first-missing") || !strings.Contains(string(got), "second-missing") {
		t.Fatalf("appended stderr = %q, err=%v", got, err)
	}
}

func TestRedirect_MidStreamError_CommitsNothing(t *testing.T) {
	sh, d, _ := newWritableShell(t)

	// cat of a missing file exits non-zero and produces no full output.
	_, _, code := run(sh, "cat /does-not-exist > /out.md")
	if code == 0 {
		t.Fatal("expected non-zero exit from failed producer")
	}
	if _, err := os.Stat(d.resolve("/out.md")); !os.IsNotExist(err) {
		t.Fatalf("failed producer must not create the redirect target")
	}
}

func TestRedirect_OversizeRejected(t *testing.T) {
	dir := t.TempDir()
	d := NewDirFS(dir, config.FilesConfig{})
	d.maxWriteBytes = 4
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	sh := shell.NewShell(d)

	_, errs, code := run(sh, "echo hello > /big.md") // "hello\n" = 6 bytes > 4
	if code == 0 {
		t.Fatalf("oversize redirect should fail; err=%q", errs)
	}
	if _, err := os.Stat(d.resolve("/big.md")); !os.IsNotExist(err) {
		t.Fatal("oversize redirect must not create the file")
	}
}

// Under the default "hash" policy concurrent overwrites are compare-and-swap:
// the file is never torn, and a writer either wins (exit 0) or is rejected as a
// concurrent change (exit 1, precondition) — never a silent clobber.
func TestRedirect_ConcurrentOverwrite_NeverTorn(t *testing.T) {
	_, d, _ := newWritableShell(t)

	const n = 40
	valid := map[string]bool{}
	var mu sync.Mutex
	wins := 0
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		val := fmt.Sprintf("writer-%02d-%s", i, strings.Repeat("x", 4000))
		valid[val+"\n"] = true
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			sh := shell.NewShell(d) // own shell, shared FS (default: hash policy)
			_, errs, code := run(sh, "echo "+v+" > /race.txt")
			if code == 0 {
				mu.Lock()
				wins++
				mu.Unlock()
				return
			}
			// The only acceptable failure is a CAS rejection.
			if !strings.Contains(errs, "changed concurrently") {
				t.Errorf("unexpected redirect failure: code=%d err=%q", code, errs)
			}
		}(val)
	}
	wg.Wait()

	if wins == 0 {
		t.Fatal("at least one concurrent overwrite must win")
	}
	got, err := d.ReadFile("/race.txt")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !valid[string(got)] {
		t.Fatalf("final content is torn / not a single writer's value (len=%d)", len(got))
	}
}

// With last_write_wins the same race lets every writer succeed (no CAS); the
// file is still never torn.
func TestRedirect_ConcurrentOverwrite_LastWriteWins(t *testing.T) {
	_, d, _ := newWritableShell(t)

	const n = 40
	valid := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		val := fmt.Sprintf("writer-%02d-%s", i, strings.Repeat("x", 4000))
		valid[val+"\n"] = true
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			sh := shell.NewShell(d)
			sh.SetConflictPolicyFn(func(string) vfs.WriteConflictPolicy { return vfs.PolicyLastWriteWins })
			if _, errs, code := run(sh, "echo "+v+" > /race.txt"); code != 0 {
				t.Errorf("last_write_wins redirect should always succeed: code=%d err=%q", code, errs)
			}
		}(val)
	}
	wg.Wait()

	got, err := d.ReadFile("/race.txt")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !valid[string(got)] {
		t.Fatalf("final content is torn / not a single writer's value (len=%d)", len(got))
	}
}

func TestRedirect_ConcurrentAppend_AllLand(t *testing.T) {
	_, d, _ := newWritableShell(t)

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sh := shell.NewShell(d)
			line := fmt.Sprintf("printf 'line-%02d\\n' >> /app.log", i)
			if _, errs, code := run(sh, line); code != 0 {
				t.Errorf("concurrent append: code=%d err=%q", code, errs)
			}
		}(i)
	}
	wg.Wait()

	got, err := d.ReadFile("/app.log")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("append landed %d lines, want %d (lost a concurrent append)", len(lines), n)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct lines, got %d", n, len(seen))
	}
}

func TestTee_BufferAndCommit(t *testing.T) {
	sh, d, _ := newWritableShell(t)

	out, _, code := run(sh, "echo hi | tee /t1.md /t2.md")
	if code != 0 {
		t.Fatalf("tee failed: %d", code)
	}
	if out != "hi\n" {
		t.Fatalf("tee passthrough = %q, want %q", out, "hi\n")
	}
	for _, f := range []string{"/t1.md", "/t2.md"} {
		b, err := d.ReadFile(f)
		if err != nil || string(b) != "hi\n" {
			t.Fatalf("tee %s = %q err=%v", f, b, err)
		}
	}

	// tee -a appends.
	run(sh, "echo more | tee -a /t1.md")
	b, _ := d.ReadFile("/t1.md")
	if string(b) != "hi\nmore\n" {
		t.Fatalf("tee -a = %q, want %q", b, "hi\nmore\n")
	}
}

func TestSed_InPlace_AtomicWriteBack(t *testing.T) {
	sh, d, _ := newWritableShell(t)

	run(sh, "printf 'foo\\nbar\\nfoo\\n' > /s.md")
	if _, errs, code := run(sh, "sed -i s/foo/baz/ /s.md"); code != 0 {
		t.Fatalf("sed -i failed: code=%d err=%q", code, errs)
	}
	b, _ := d.ReadFile("/s.md")
	if string(b) != "baz\nbar\nbaz\n" {
		t.Fatalf("sed -i result = %q, want %q", b, "baz\nbar\nbaz\n")
	}
}

func TestPatch_AppliesAndConflictsOnStaleContext(t *testing.T) {
	sh, d, _ := newWritableShell(t)

	run(sh, "printf 'alpha\\nbeta\\ngamma\\n' > /doc.md")

	diff := "--- a/doc.md\n+++ b/doc.md\n@@ -1,3 +1,3 @@\n alpha\n-beta\n+BETA\n gamma\n"

	// Apply via stdin heredoc-style: use a pipe through cat is awkward; call
	// the shell with the diff on stdin directly.
	var out, errb bytes.Buffer
	code := sh.Exec("patch /doc.md", &out, &errb, strings.NewReader(diff))
	if code != 0 {
		t.Fatalf("patch apply failed: code=%d err=%q", code, errb.String())
	}
	b, _ := d.ReadFile("/doc.md")
	if string(b) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("patched = %q, want %q", b, "alpha\nBETA\ngamma\n")
	}

	// Now drift the file so the same diff's context no longer matches.
	run(sh, "printf 'alpha\\nDRIFT\\ngamma\\n' > /doc.md")
	out.Reset()
	errb.Reset()
	code = sh.Exec("patch /doc.md", &out, &errb, strings.NewReader(diff))
	if code == 0 {
		t.Fatalf("patch on stale base should conflict; out=%q", out.String())
	}
	if !strings.Contains(errb.String(), "drifted") && !strings.Contains(errb.String(), "mismatch") {
		t.Fatalf("expected conflict message, got %q", errb.String())
	}
	// File must be unchanged by the failed patch.
	b, _ = d.ReadFile("/doc.md")
	if string(b) != "alpha\nDRIFT\ngamma\n" {
		t.Fatalf("conflicted patch must not modify the file; got %q", b)
	}
}
