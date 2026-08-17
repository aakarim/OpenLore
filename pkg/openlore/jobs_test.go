package openlore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

// fakeRunner is a deterministic Runner for tests: it returns out/err
// without execing anything.
type fakeRunner struct {
	out []byte
	err error
}

func (r fakeRunner) Run(_ context.Context, _ string, _ []string) ([]byte, error) {
	return r.out, r.err
}

// spawnFixture wires the Part D stack: a writable substrate, a scoped session
// (writable only under /ops), a JobManager with the given runner installed as
// cmds.Jobs, and a shell that is allowed to spawn. It returns the proposer
// shell, the manager, and the raw substrate.
func spawnFixture(t *testing.T, runner fakeRunner) (*shell.Shell, *JobManager, *DirFS) {
	t.Helper()
	base := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	if err := base.Mkdir("/ops"); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}

	mgr := NewJobManager(4, runner, nil)
	saved := cmds.Jobs
	cmds.Jobs = mgr
	t.Cleanup(func() { cmds.Jobs = saved })

	scoped := newScopedWriteFS(base, rootsAuthz("/ops"))
	sh := shell.NewShell(scoped)
	sh.SetAllowedActions([]cmds.Action{cmds.ActionWrite, cmds.ActionSpawn})
	sh.SetEnv("OPENLORE_IDENTITY", "jared")
	return sh, mgr, base
}

// waitJob polls until the single job reaches a terminal state (or fails the
// test on timeout).
func waitJob(t *testing.T, mgr *JobManager) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list := mgr.list()
		if len(list) == 1 && list[0].State != JobRunning {
			return list[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not reach terminal state in time")
	return Job{}
}

func TestSpawn_AsyncWriteBack(t *testing.T) {
	sh, mgr, base := spawnFixture(t, fakeRunner{out: []byte("temporal-ns: v42\n")})

	out, errs, code := run(sh, "spawn --writes /ops/live.md -- kubectl get ns")
	if code != 0 {
		t.Fatalf("spawn failed: code=%d err=%q", code, errs)
	}
	if !strings.Contains(out, "job_") {
		t.Fatalf("spawn output should announce a job id, got %q", out)
	}

	// spawn returns before the write lands; drain to let the goroutine commit.
	if !mgr.Drain(2 * time.Second) {
		t.Fatal("jobs did not drain")
	}
	job := waitJob(t, mgr)
	if job.State != JobDone {
		t.Fatalf("job state = %s, want done (note=%q)", job.State, job.Note)
	}
	if b, err := base.ReadFile("/ops/live.md"); err != nil || string(b) != "temporal-ns: v42\n" {
		t.Fatalf("write-back not committed: %q err=%v", b, err)
	}
	if job.Attribution.Principal != "jared" {
		t.Fatalf("provenance = %q, want jared", job.Attribution.Principal)
	}
}

func TestSpawn_OutOfScopeFailsFast(t *testing.T) {
	sh, mgr, base := spawnFixture(t, fakeRunner{out: []byte("x")})

	_, errs, code := run(sh, "spawn --writes /elsewhere/live.md -- echo hi")
	if code == 0 {
		t.Fatal("spawn to an out-of-scope target should fail fast")
	}
	if !strings.Contains(errs, "not writable") {
		t.Fatalf("error should explain scope, got %q", errs)
	}
	if len(mgr.list()) != 0 {
		t.Fatal("no job should be registered for a doomed target")
	}
	if _, err := base.ReadFile("/elsewhere/live.md"); err == nil {
		t.Fatal("nothing should be written out of scope")
	}
}

func TestSpawn_CommandFailureMarksFailed(t *testing.T) {
	sh, mgr, base := spawnFixture(t, fakeRunner{out: []byte("boom"), err: fmt.Errorf("exit status 1")})

	if _, errs, code := run(sh, "spawn --writes /ops/live.md -- false"); code != 0 {
		t.Fatalf("spawn submit should succeed: code=%d err=%q", code, errs)
	}
	mgr.Drain(2 * time.Second)
	job := waitJob(t, mgr)
	if job.State != JobFailed {
		t.Fatalf("job state = %s, want failed", job.State)
	}
	if !strings.Contains(job.Note, "command failed") {
		t.Fatalf("failure note = %q", job.Note)
	}
	if _, err := base.ReadFile("/ops/live.md"); err == nil {
		t.Fatal("a failed command must not commit anything")
	}
}

func TestSpawn_NotEnabled(t *testing.T) {
	base := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := base.SetWriteable(); err != nil {
		t.Fatalf("SetWriteable: %v", err)
	}
	saved := cmds.Jobs
	cmds.Jobs = nil
	t.Cleanup(func() { cmds.Jobs = saved })

	sh := shell.NewShell(newScopedWriteFS(base, nil))
	sh.SetAllowedActions([]cmds.Action{cmds.ActionSpawn})
	if _, errs, code := run(sh, "spawn --writes /a.md -- echo hi"); code == 0 || !strings.Contains(errs, "not enabled") {
		t.Fatalf("spawn should report disabled, code=%d err=%q", code, errs)
	}
}

func TestSpawn_GatedOffForOrdinaryWriters(t *testing.T) {
	sh, _, _ := spawnFixture(t, fakeRunner{out: []byte("x")})
	// A session that may write but lacks ActionSpawn cannot even see spawn.
	sh.SetAllowedActions([]cmds.Action{cmds.ActionWrite})
	_, _, code := run(sh, "spawn --writes /ops/live.md -- echo hi")
	if code == 0 {
		t.Fatal("spawn must be unavailable without ActionSpawn")
	}
}
