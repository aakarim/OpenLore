package openlore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// JobState is the lifecycle state of an async job.
type JobState string

const (
	// JobRunning: queued or executing.
	JobRunning JobState = "running"
	// JobDone: the command ran and its output committed (or was parked for
	// approval).
	JobDone JobState = "done"
	// JobFailed: the command failed, or its write-back could not commit.
	JobFailed JobState = "failed"
)

// DefaultJobTimeout caps a single job's command + write-back wall-clock time.
const DefaultJobTimeout = 10 * time.Minute

// jobDrainTimeout bounds how long Shutdown waits for in-flight jobs to commit.
const jobDrainTimeout = 5 * time.Second

// Job is the in-memory record of one async job (Part D). It is intentionally not
// persisted — a server restart loses in-flight jobs, which is acceptable for
// ad-hoc operational write-back.
type Job struct {
	ID          string
	Command     string
	Target      string
	Attribution Attribution
	State       JobState
	Note        string // terminal detail: bytes written, pending request id, or error
	StartedAt   time.Time
	EndedAt     time.Time
}

// JobManager runs JobSpecs on a bounded worker pool and keeps an in-memory
// registry surfaced read-only at /jobs. It implements cmds.JobBackend.
type JobManager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string // insertion order, newest last
	sem    chan struct{}
	wg     sync.WaitGroup
	runner Runner
	logger *slog.Logger
}

// NewJobManager creates a manager with at most maxConcurrent jobs running at
// once. runner defaults to a real `sh -c` runner.
func NewJobManager(maxConcurrent int, runner Runner, logger *slog.Logger) *JobManager {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if runner == nil {
		runner = ShellRunner{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &JobManager{
		jobs:   make(map[string]*Job),
		sem:    make(chan struct{}, maxConcurrent),
		runner: runner,
		logger: logger,
	}
}

// Submit registers a job and starts it in the background, returning its id
// immediately (cmds.JobBackend).
func (m *JobManager) Submit(spec cmds.JobSpec) (string, error) {
	id, err := newJobID()
	if err != nil {
		return "", err
	}
	job := &Job{
		ID:          id,
		Command:     spec.Command,
		Target:      spec.Target,
		Attribution: Attribution{Principal: spec.Attribution.Principal, Actor: spec.Attribution.Actor, ClientAuth: ClientAuthLevel(spec.Attribution.ClientAuth)},
		State:       JobRunning,
		StartedAt:   time.Now(),
	}
	m.mu.Lock()
	m.jobs[id] = job
	m.order = append(m.order, id)
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run(job, spec)
	return id, nil
}

func (m *JobManager) run(job *Job, spec cmds.JobSpec) {
	defer m.wg.Done()
	// Bound concurrency.
	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	ctx, cancel := context.WithTimeout(context.Background(), DefaultJobTimeout)
	defer cancel()

	out, err := m.runner.Run(ctx, spec.Command, m.jobEnv(spec))
	if err != nil {
		m.finish(job, JobFailed, fmt.Sprintf("command failed: %v (%s)", err, truncateOutput(out, 256)))
		return
	}

	// Commit stdout through the normal write seam on the frozen context, so
	// CAS / per-docset policy all apply uniformly.
	_, werr := cmds.WriteFile(spec.WriteCtx, spec.Target, out, spec.Append)
	if werr != nil {
		m.finish(job, JobFailed, fmt.Sprintf("write-back failed: %v", werr))
		return
	}
	m.finish(job, JobDone, fmt.Sprintf("wrote %d bytes to %s", len(out), spec.Target))
}

func (m *JobManager) finish(job *Job, state JobState, note string) {
	m.mu.Lock()
	job.State = state
	job.Note = note
	job.EndedAt = time.Now()
	m.mu.Unlock()
	m.logger.Info("job finished", "job", job.ID, "state", state, "target", job.Target, "identity", job.Attribution.String(), "note", note)
}

// jobEnv builds the environment for the spawned command.
func (m *JobManager) jobEnv(spec cmds.JobSpec) []string {
	return []string{
		"OPENLORE_IDENTITY=" + spec.Attribution.Principal,
		"OPENLORE_ACTOR=" + spec.Attribution.Actor,
		"OPENLORE_CLIENT_AUTH=" + spec.Attribution.ClientAuth,
		"OPENLORE_JOB_TARGET=" + spec.Target,
	}
}

// Drain waits for in-flight jobs to finish, up to timeout. Returns false if the
// timeout elapsed with jobs still running.
func (m *JobManager) Drain(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// snapshot returns a copy of one job (or false).
func (m *JobManager) snapshot(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// list returns copies of all jobs, newest first.
func (m *JobManager) list() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if j, ok := m.jobs[m.order[i]]; ok {
			out = append(out, *j)
		}
	}
	return out
}

func newJobID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(b[:]), nil
}

func truncateOutput(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// renderJob renders a job as the file body shown at /jobs/<id>.
func renderJob(j Job) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "job:      %s\n", j.ID)
	fmt.Fprintf(&b, "state:    %s\n", j.State)
	fmt.Fprintf(&b, "identity: %s\n", j.Attribution.String())
	fmt.Fprintf(&b, "target:   %s\n", j.Target)
	fmt.Fprintf(&b, "command:  %s\n", j.Command)
	fmt.Fprintf(&b, "started:  %s\n", j.StartedAt.UTC().Format(time.RFC3339))
	if !j.EndedAt.IsZero() {
		fmt.Fprintf(&b, "ended:    %s\n", j.EndedAt.UTC().Format(time.RFC3339))
	}
	if j.Note != "" {
		fmt.Fprintf(&b, "detail:   %s\n", j.Note)
	}
	return []byte(b.String())
}

// JobsFS is the read-only computed filesystem mounted at /jobs. Each job renders
// as a file named by its id; the directory lists all jobs. It is not a
// vfs.WritableFS, so writes to /jobs are denied by MergeFS.
type JobsFS struct {
	mgr *JobManager
}

// NewJobsFS wraps a manager as a read-only computed FS.
func NewJobsFS(mgr *JobManager) *JobsFS { return &JobsFS{mgr: mgr} }

func (f *JobsFS) Stat(p string) (*vfs.FileInfo, error) {
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return &vfs.FileInfo{FileName: "jobs", FilePath: "/", Dir: true}, nil
	}
	id := strings.TrimPrefix(clean, "/")
	j, ok := f.mgr.snapshot(id)
	if !ok {
		return nil, fmt.Errorf("no such job: %s", id)
	}
	body := renderJob(j)
	mod := j.StartedAt
	if !j.EndedAt.IsZero() {
		mod = j.EndedAt
	}
	return &vfs.FileInfo{FileName: id, FilePath: clean, FileSize: int64(len(body)), FileModTime: mod}, nil
}

func (f *JobsFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	if vfs.CleanPath(p) != "/" {
		return nil, fmt.Errorf("not a directory: %s", p)
	}
	jobs := f.mgr.list()
	entries := make([]vfs.FileInfo, 0, len(jobs))
	for _, j := range jobs {
		mod := j.StartedAt
		if !j.EndedAt.IsZero() {
			mod = j.EndedAt
		}
		entries = append(entries, vfs.FileInfo{FileName: j.ID, FilePath: "/" + j.ID, FileModTime: mod})
	}
	return entries, nil
}

func (f *JobsFS) ReadFile(p string) ([]byte, error) {
	clean := vfs.CleanPath(p)
	if clean == "/" {
		return nil, fmt.Errorf("cannot read directory")
	}
	id := strings.TrimPrefix(clean, "/")
	j, ok := f.mgr.snapshot(id)
	if !ok {
		return nil, fmt.Errorf("no such job: %s", id)
	}
	return renderJob(j), nil
}

var _ cmds.JobBackend = (*JobManager)(nil)
