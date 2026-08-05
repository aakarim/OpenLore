package cmds

import (
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// JobSpec describes one asynchronous external job: run Command via a shell, then
// commit its stdout to Target through WriteCtx (a frozen, session-detached write
// context). Append selects `>` vs `>>` semantics. Identity is for provenance.
type JobSpec struct {
	Command  string
	Target   string
	Append   bool
	Identity string
	WriteCtx CmdContext
}

// JobBackend schedules and runs JobSpecs. The server supplies the
// implementation (it owns the bounded worker pool + the in-memory registry).
// Submit returns the job id immediately; the work runs in the background.
type JobBackend interface {
	Submit(spec JobSpec) (id string, err error)
}

// Jobs is the active job backend, set by the server at startup. Nil when the
// server has no job manager (e.g. an embedded read-only server).
var Jobs JobBackend

// CmdSpawn runs an external command asynchronously and writes its stdout back
// into the lore once it finishes — without blocking the caller.
//
//	spawn --writes <path> [--append] -- <command...>
//
// The target must sit within the session's writable scope; spawn fails fast at
// submit time otherwise. The command runs as the OpenLore service user, so the
// verb is gated by ActionSpawn (operator-trusted identities only).
func CmdSpawn(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if Jobs == nil {
		fmt.Fprintln(errW, "spawn: async jobs are not enabled on this server")
		return 1
	}

	var target string
	appendMode := false
	var cmdParts []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--writes" || a == "-w":
			if i+1 >= len(args) {
				fmt.Fprintln(errW, "spawn: --writes requires a path")
				return 1
			}
			target = args[i+1]
			i++
		case a == "--append" || a == "-a":
			appendMode = true
		case a == "--":
			cmdParts = args[i+1:]
			i = len(args)
		default:
			fmt.Fprintf(errW, "spawn: unexpected argument %q (usage: spawn --writes <path> [--append] -- <command...>)\n", a)
			return 1
		}
	}

	if target == "" {
		fmt.Fprintln(errW, "spawn: --writes <path> is required")
		return 1
	}
	if len(cmdParts) == 0 {
		fmt.Fprintln(errW, "spawn: a command is required after --")
		return 1
	}

	resolved := ctx.Resolve(target)

	// Fail fast: the target must be in this session's writable scope, so we
	// never park a job that can only ever fail to commit.
	if sc, ok := ctx.FS().(vfs.WriteScopeFS); ok && !sc.CanWrite(resolved) {
		fmt.Fprintf(errW, "spawn: %s is not writable in this session\n", target)
		return 1
	}

	command := strings.Join(cmdParts, " ")
	id, err := Jobs.Submit(JobSpec{
		Command:  command,
		Target:   resolved,
		Append:   appendMode,
		Identity: ctx.GetEnv("OPENLORE_IDENTITY"),
		WriteCtx: newFrozenContext(ctx, resolved),
	})
	if err != nil {
		fmt.Fprintf(errW, "spawn: %s\n", err)
		return 1
	}

	fmt.Fprintf(w, "Spawned %s — writing to %s when done\n", id, resolved)
	fmt.Fprintf(w, "  track: /jobs/%s\n", id)
	return 0
}

// frozenContext is an immutable CmdContext snapshot safe to use from a
// background goroutine after the originating session has closed. It captures the
// session's scoped FS, cwd, a copy of the env, and the resolved write-conflict
// policy for the job's target. Exec/ExecPipeline are disabled — a background job
// commits a single write, it does not run further shell pipelines.
type frozenContext struct {
	fs     vfs.FileSystem
	cwd    string
	env    map[string]string
	policy vfs.WriteConflictPolicy
}

func newFrozenContext(src CmdContext, target string) *frozenContext {
	envCopy := make(map[string]string)
	for k, v := range src.AllEnv() {
		envCopy[k] = v
	}
	return &frozenContext{
		fs:     src.FS(),
		cwd:    src.Cwd(),
		env:    envCopy,
		policy: src.WriteConflictPolicy(target),
	}
}

func (c *frozenContext) FS() vfs.FileSystem { return c.fs }
func (c *frozenContext) Cwd() string        { return c.cwd }
func (c *frozenContext) SetCwd(string)      {}
func (c *frozenContext) Resolve(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return path.Clean(path.Join(c.cwd, p))
}
func (c *frozenContext) GetEnv(k string) string    { return c.env[k] }
func (c *frozenContext) SetEnv(k, v string)        { c.env[k] = v }
func (c *frozenContext) DeleteEnv(k string)        { delete(c.env, k) }
func (c *frozenContext) AllEnv() map[string]string { return c.env }
func (c *frozenContext) Exec(string, io.Writer, io.Writer, io.Reader) int {
	return 1
}
func (c *frozenContext) ExecPipeline(string, io.Writer, io.Writer, io.Reader) int {
	return 1
}
func (c *frozenContext) ActionAllowed(Action) bool { return true }
func (c *frozenContext) WriteConflictPolicy(string) vfs.WriteConflictPolicy {
	return c.policy
}
func (c *frozenContext) Docsets() []DocsetInfo              { return nil }
func (c *frozenContext) SkillsManagementEnabled() bool      { return false }
func (c *frozenContext) PublishTargets() []PublishTarget    { return nil }
func (c *frozenContext) MetaExtenders() []meta.Extender     { return nil }
func (c *frozenContext) MetaFilters() []meta.Filter         { return nil }
func (c *frozenContext) Validators() []validation.Validator { return nil }

var _ CmdContext = (*frozenContext)(nil)
