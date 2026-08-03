package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// The shellexec plugin runs external commands as middleware on the read and
// write paths — the reborn, middleware-shaped replacement for the legacy
// event-bus hooks. It contributes:
//
//   - pre_read   → read middleware:  runs before a read; may abort it.
//   - pre_commit → write middleware: runs before a write commits; may reject it.
//   - post_write → post-commit:      runs after a durable commit; fire-and-forget,
//     never halts the log (a failure is logged only).
//
// Defaults (differ from the legacy hooks): synchronous (async opt-in),
// fail_on_error=true, 30s timeout, 2s pre_read debounce. Commands are run via
// `sh -c` with the OPENLORE_* env protocol.
//
// It implements WriteMiddlewareProvider, ReadMiddlewareProvider, and
// PostCommitProvider, so registerPlugin wires it into the chains.

const (
	defaultShellexecTimeout  = 30 * time.Second
	defaultShellexecDebounce = 2 * time.Second
)

// execCmd is a parsed, defaulted shellexec command.
type execCmd struct {
	cmd         string
	timeout     time.Duration
	failOnError bool
	debounce    time.Duration // pre_read only
	async       bool
}

type shellexecPlugin struct {
	preRead   []execCmd
	preCommit []execCmd
	postWrite []execCmd

	dataDir string
	runner  Runner
	logger  *slog.Logger
}

// newShellexec parses cfg (validating durations and requiring a cmd on each
// entry) and returns the plugin. runner (nil → sh -c) and logger (nil →
// slog.Default) are injectable for tests.
func newShellexec(cfg config.ShellexecConfig, dataDir string, runner Runner, logger *slog.Logger) (*shellexecPlugin, error) {
	if runner == nil {
		runner = ShellRunner{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	preRead, err := parseExecCmds(cfg.PreRead, true)
	if err != nil {
		return nil, err
	}
	preCommit, err := parseExecCmds(cfg.PreCommit, false)
	if err != nil {
		return nil, err
	}
	postWrite, err := parseExecCmds(cfg.PostWrite, false)
	if err != nil {
		return nil, err
	}
	return &shellexecPlugin{
		preRead:   preRead,
		preCommit: preCommit,
		postWrite: postWrite,
		dataDir:   dataDir,
		runner:    runner,
		logger:    logger,
	}, nil
}

func parseExecCmds(cmds []config.ShellexecCmd, allowDebounce bool) ([]execCmd, error) {
	out := make([]execCmd, 0, len(cmds))
	for _, c := range cmds {
		if c.Cmd == "" {
			return nil, fmt.Errorf("shellexec: cmd is required")
		}
		ec := execCmd{
			cmd:         c.Cmd,
			timeout:     defaultShellexecTimeout,
			failOnError: c.FailOnError == nil || *c.FailOnError,
			debounce:    defaultShellexecDebounce,
			async:       c.Async,
		}
		if c.Timeout != "" {
			d, err := time.ParseDuration(c.Timeout)
			if err != nil || d < 0 {
				return nil, fmt.Errorf("shellexec: invalid timeout %q for %q", c.Timeout, c.Cmd)
			}
			ec.timeout = d
		}
		if allowDebounce && c.Debounce != "" {
			d, err := time.ParseDuration(c.Debounce)
			if err != nil || d < 0 {
				return nil, fmt.Errorf("shellexec: invalid debounce %q for %q", c.Debounce, c.Cmd)
			}
			ec.debounce = d
		}
		out = append(out, ec)
	}
	return out, nil
}

// ReadMiddleware contributes the pre_read commands (before-read gate).
func (p *shellexecPlugin) ReadMiddleware() []ReadMiddleware {
	mws := make([]ReadMiddleware, 0, len(p.preRead))
	for i, c := range p.preRead {
		c := c
		name := fmt.Sprintf("pre_read[%d]", i)
		deb := newDebouncer(c.debounce)
		mws = append(mws, func(next ReadHandler) ReadHandler {
			return func(ctx context.Context, op ReadOp) error {
				if !deb.allow(op.Path) {
					return next(ctx, op)
				}
				if err := p.exec(ctx, name, c, p.readEnv(op), true); err != nil {
					return err
				}
				return next(ctx, op)
			}
		})
	}
	return mws
}

// WriteMiddleware contributes the pre_commit commands (before-commit gate).
func (p *shellexecPlugin) WriteMiddleware() []WriteMiddleware {
	mws := make([]WriteMiddleware, 0, len(p.preCommit))
	for i, c := range p.preCommit {
		c := c
		name := fmt.Sprintf("pre_commit[%d]", i)
		mws = append(mws, func(next WriteHandler) WriteHandler {
			return func(ctx context.Context, op WriteOp) (WriteResult, error) {
				if err := p.exec(ctx, name, c, p.writeLeavesEnv(op.Leaves(), op.Actor, "pre_commit"), true); err != nil {
					return WriteResult{}, err
				}
				return next(ctx, op)
			}
		})
	}
	return mws
}

// PostCommitMiddleware contributes the post_write commands. These are
// fire-and-forget: a failure is logged but never halts the log (decision #11),
// so the command always falls through to next.
func (p *shellexecPlugin) PostCommitMiddleware() []PostCommitMiddleware {
	mws := make([]PostCommitMiddleware, 0, len(p.postWrite))
	for i, c := range p.postWrite {
		c := c
		name := fmt.Sprintf("post_write[%d]", i)
		mws = append(mws, func(next PostCommitHandler) PostCommitHandler {
			return func(ctx context.Context, info CommitInfo) error {
				_ = p.exec(ctx, name, c, p.postEnv(info), false)
				return next(ctx, info)
			}
		})
	}
	return mws
}

// Info implements PluginInfoProvider.
func (p *shellexecPlugin) Info() PluginInfo { return PluginInfo{Name: "shellexec", Version: "1.0.0"} }

var (
	_ WriteMiddlewareProvider = (*shellexecPlugin)(nil)
	_ ReadMiddlewareProvider  = (*shellexecPlugin)(nil)
	_ PostCommitProvider      = (*shellexecPlugin)(nil)
	_ PluginInfoProvider      = (*shellexecPlugin)(nil)
)

// exec runs c and returns a non-nil error only when the caller should abort the
// operation: synchronous, failed, fail_on_error set, and abortOnFail true
// (pre_read / pre_commit). Async runs in the background and never aborts;
// post_write passes abortOnFail=false so a failure only logs.
func (p *shellexecPlugin) exec(ctx context.Context, name string, c execCmd, env []string, abortOnFail bool) error {
	if c.async {
		go p.runOnce(context.Background(), name, c, env)
		return nil
	}
	err := p.runOnce(ctx, name, c, env)
	if err != nil && abortOnFail && c.failOnError {
		return fmt.Errorf("shellexec %s: %w", name, err)
	}
	return nil
}

func (p *shellexecPlugin) runOnce(ctx context.Context, name string, c execCmd, env []string) error {
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	out, err := p.runner.Run(runCtx, c.cmd, env)
	if err != nil {
		p.logger.Warn("shellexec command failed",
			"hook", name, "cmd", c.cmd, "err", err, "output", truncateOutput(out, 512))
		return err
	}
	if len(out) > 0 {
		p.logger.Debug("shellexec command output",
			"hook", name, "cmd", c.cmd, "output", truncateOutput(out, 512))
	}
	return nil
}

// baseEnv builds the OPENLORE_* env shared by every event kind.
func (p *shellexecPlugin) baseEnv(event, path string, actor Actor) []string {
	env := []string{
		"OPENLORE_DATA_DIR=" + p.dataDir,
		"OPENLORE_EVENT=" + event,
		"OPENLORE_PATH=" + path,
		"OPENLORE_AGENT=" + actor.ID,
	}
	for k, v := range actor.Extra {
		env = append(env, "OPENLORE_EXTRA_"+k+"="+v)
	}
	return env
}

func (p *shellexecPlugin) readEnv(op ReadOp) []string {
	return append(p.baseEnv("pre_read", op.Path, op.Actor), "OPENLORE_READ_KIND="+string(op.Kind))
}

func (p *shellexecPlugin) writeEnv(cs vfs.ChangeSet, actor Actor, event string) []string {
	return p.writeLeavesEnv(cs.Leaves(), actor, event)
}

func (p *shellexecPlugin) writeLeavesEnv(leaves []vfs.Change, actor Actor, event string) []string {
	if len(leaves) == 0 {
		return nil
	}
	path, action := leaves[0].Target, string(leaves[0].Action)
	if len(leaves) > 1 {
		path, action = "", "batch"
	}
	env := append(p.baseEnv(event, path, actor), "OPENLORE_ACTION="+action)
	// OPENLORE_CHANGES_JSON is always present and is the stable, inspectable
	// leaf list. Batch hooks run once, preserving one admission/log operation.
	type leafSummary struct {
		Target string           `json:"target"`
		Action vfs.ChangeAction `json:"action"`
		Bytes  int              `json:"bytes"`
	}
	summaries := make([]leafSummary, 0, len(leaves))
	for _, leaf := range leaves {
		n := 0
		if leaf.Write != nil {
			n = len(leaf.Write.Bytes)
		}
		summaries = append(summaries, leafSummary{Target: leaf.Target, Action: leaf.Action, Bytes: n})
	}
	b, _ := json.Marshal(summaries)
	env = append(env, "OPENLORE_CHANGES_JSON="+string(b), "OPENLORE_CHANGE_COUNT="+strconv.Itoa(len(leaves)))
	if len(leaves) == 1 && leaves[0].Write != nil {
		env = append(env, "OPENLORE_BYTES="+strconv.Itoa(len(leaves[0].Write.Bytes)))
	} else if len(leaves) > 1 {
		total := 0
		for _, change := range leaves {
			if change.Write != nil {
				total += len(change.Write.Bytes)
			}
		}
		env = append(env, "OPENLORE_BYTES="+strconv.Itoa(total))
	}
	return env
}

func (p *shellexecPlugin) postEnv(info CommitInfo) []string {
	return append(p.writeEnv(info.ChangeSet, info.Actor, "post_write"), "OPENLORE_HASH="+info.Hash)
}

// debouncer coalesces repeated calls for the same path within a window.
type debouncer struct {
	window time.Duration
	mu     sync.Mutex
	last   map[string]time.Time
}

func newDebouncer(window time.Duration) *debouncer {
	return &debouncer{window: window, last: map[string]time.Time{}}
}

// allow reports whether a call for path should proceed (records the fire time
// as a side effect). A non-positive window disables debouncing.
func (d *debouncer) allow(path string) bool {
	if d.window <= 0 {
		return true
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.last[path]; ok && now.Sub(t) < d.window {
		return false
	}
	d.last[path] = now
	return true
}

var (
	_ WriteMiddlewareProvider = (*shellexecPlugin)(nil)
	_ ReadMiddlewareProvider  = (*shellexecPlugin)(nil)
	_ PostCommitProvider      = (*shellexecPlugin)(nil)
)
