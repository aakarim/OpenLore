package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeExecRunner records each command + its parsed env and returns a programmed
// error per command line.
type fakeExecRunner struct {
	mu     sync.Mutex
	calls  []map[string]string
	cmds   []string
	errFor map[string]error
	ran    chan string // optional: signalled with the cmd after each run
}

func (f *fakeExecRunner) Run(_ context.Context, cmd string, env []string) ([]byte, error) {
	f.mu.Lock()
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	f.calls = append(f.calls, m)
	f.cmds = append(f.cmds, cmd)
	err := f.errFor[cmd]
	ran := f.ran
	f.mu.Unlock()
	if ran != nil {
		ran <- cmd
	}
	return nil, err
}

func (f *fakeExecRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cmds)
}

func (f *fakeExecRunner) lastEnv() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

func writeCSBytes(target, data string) vfs.ChangeSet {
	return vfs.ChangeSet{Target: target, Action: vfs.ChangeActionWrite,
		Write: &vfs.WriteChange{Bytes: []byte(data)}}
}

func TestShellexec_PreCommitPassesEnvAndAllows(t *testing.T) {
	fr := &fakeExecRunner{}
	p := &shellexecPlugin{
		preCommit: []execCmd{{cmd: "validate", timeout: time.Second, failOnError: true}},
		dataDir:   "/data",
		runner:    fr,
		logger:    silentLogger(),
	}
	terminalCalled := false
	terminal := func(_ context.Context, _ WriteOp) (WriteResult, error) {
		terminalCalled = true
		return WriteResult{Hash: "h"}, nil
	}
	h := chainWrite(terminal, p.WriteMiddleware()...)

	res, err := h(context.Background(), NewWriteOp(Attribution{Principal: "agent-1"}, writeCSBytes("/w/x", "hello")))
	if err != nil || res.Hash != "h" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !terminalCalled {
		t.Fatal("terminal must run when pre_commit passes")
	}
	env := fr.lastEnv()
	if env["OPENLORE_EVENT"] != "pre_commit" || env["OPENLORE_PATH"] != "/w/x" ||
		env["OPENLORE_AGENT"] != "agent-1" || env["OPENLORE_ACTION"] != "write" ||
		env["OPENLORE_BYTES"] != "5" || env["OPENLORE_DATA_DIR"] != "/data" {
		t.Fatalf("env = %v", env)
	}
}

func TestShellexecBatchChangesJSONOmitsWriteBytes(t *testing.T) {
	p := &shellexecPlugin{}
	large := bytes.Repeat([]byte("x"), 1024*1024)
	cs := vfs.ChangeSet{Changes: []vfs.Change{{Target: "/large", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: large}}}}
	env := map[string]string{}
	for _, entry := range p.writeEnv(cs, Attribution{}, "pre_commit") {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	if len(env["OPENLORE_CHANGES_JSON"]) > 200 || strings.Contains(env["OPENLORE_CHANGES_JSON"], strings.Repeat("x", 100)) {
		t.Fatalf("changes JSON embeds content: length=%d", len(env["OPENLORE_CHANGES_JSON"]))
	}
	var summaries []struct {
		Target, Action string
		Bytes          int
	}
	if err := json.Unmarshal([]byte(env["OPENLORE_CHANGES_JSON"]), &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Target != "/large" || summaries[0].Action != "write" || summaries[0].Bytes != len(large) || env["OPENLORE_BYTES"] != strconv.Itoa(len(large)) {
		t.Fatalf("summaries=%+v env=%v", summaries, env)
	}
}

func TestShellexec_PreCommitFailOnErrorRejects(t *testing.T) {
	fr := &fakeExecRunner{errFor: map[string]error{"validate": errors.New("nope")}}
	p := &shellexecPlugin{
		preCommit: []execCmd{{cmd: "validate", timeout: time.Second, failOnError: true}},
		runner:    fr, logger: silentLogger(),
	}
	terminalCalled := false
	terminal := func(_ context.Context, _ WriteOp) (WriteResult, error) {
		terminalCalled = true
		return WriteResult{}, nil
	}
	h := chainWrite(terminal, p.WriteMiddleware()...)

	if _, err := h(context.Background(), NewWriteOp(Attribution{}, writeCSBytes("/w", "x"))); err == nil {
		t.Fatal("want reject error from pre_commit")
	}
	if terminalCalled {
		t.Fatal("terminal must NOT run when pre_commit fails with fail_on_error")
	}
}

func TestShellexec_PreCommitNonFatalAllows(t *testing.T) {
	fr := &fakeExecRunner{errFor: map[string]error{"warn": errors.New("nope")}}
	p := &shellexecPlugin{
		preCommit: []execCmd{{cmd: "warn", timeout: time.Second, failOnError: false}},
		runner:    fr, logger: silentLogger(),
	}
	terminalCalled := false
	terminal := func(_ context.Context, _ WriteOp) (WriteResult, error) {
		terminalCalled = true
		return WriteResult{Hash: "h"}, nil
	}
	h := chainWrite(terminal, p.WriteMiddleware()...)

	if _, err := h(context.Background(), NewWriteOp(Attribution{}, writeCSBytes("/w", "x"))); err != nil {
		t.Fatalf("non-fatal pre_commit must not abort: %v", err)
	}
	if !terminalCalled {
		t.Fatal("terminal must run when pre_commit is non-fatal")
	}
}

func TestShellexec_PreReadAbortsAndDebounces(t *testing.T) {
	// Abort: failing pre_read with fail_on_error stops the read.
	fr := &fakeExecRunner{errFor: map[string]error{"pull": errors.New("no net")}}
	p := &shellexecPlugin{
		preRead: []execCmd{{cmd: "pull", timeout: time.Second, failOnError: true, debounce: 0}},
		runner:  fr, logger: silentLogger(),
	}
	terminalCalled := false
	terminal := func(_ context.Context, _ ReadOp) error { terminalCalled = true; return nil }
	h := chainRead(terminal, p.ReadMiddleware()...)
	if err := h(context.Background(), ReadOp{Path: "/a", Kind: ReadKindFile}); err == nil {
		t.Fatal("want abort from failing pre_read")
	}
	if terminalCalled {
		t.Fatal("read terminal must not run when pre_read aborts")
	}

	// Debounce: two rapid reads of the same path run the command once.
	fr2 := &fakeExecRunner{}
	p2 := &shellexecPlugin{
		preRead: []execCmd{{cmd: "pull", timeout: time.Second, failOnError: true, debounce: time.Hour}},
		runner:  fr2, logger: silentLogger(),
	}
	h2 := chainRead(func(_ context.Context, _ ReadOp) error { return nil }, p2.ReadMiddleware()...)
	for i := 0; i < 3; i++ {
		if err := h2(context.Background(), ReadOp{Path: "/same", Kind: ReadKindFile}); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if fr2.count() != 1 {
		t.Fatalf("debounced pre_read ran %d times, want 1", fr2.count())
	}
}

func TestShellexec_PostWriteNeverHaltsButRuns(t *testing.T) {
	fr := &fakeExecRunner{errFor: map[string]error{"version": errors.New("boom")}}
	p := &shellexecPlugin{
		postWrite: []execCmd{{cmd: "version", timeout: time.Second, failOnError: true}},
		runner:    fr, logger: silentLogger(),
	}
	nextCalled := false
	terminal := func(_ context.Context, _ CommitInfo) error { nextCalled = true; return nil }
	h := chainPostCommit(terminal, p.PostCommitMiddleware()...)

	info := CommitInfo{ChangeSet: writeCSBytes("/w", "x"), Hash: "abc", Attribution: Attribution{Principal: "a"}}
	if err := h(context.Background(), info); err != nil {
		t.Fatalf("post_write must never halt: %v", err)
	}
	if !nextCalled {
		t.Fatal("post-commit chain must fall through even when a post_write command fails")
	}
	if fr.count() != 1 {
		t.Fatalf("post_write ran %d times, want 1", fr.count())
	}
	if env := fr.lastEnv(); env["OPENLORE_EVENT"] != "post_write" || env["OPENLORE_HASH"] != "abc" {
		t.Fatalf("post_write env = %v", env)
	}
}

func TestShellexec_AsyncDoesNotAbort(t *testing.T) {
	ran := make(chan string, 1)
	fr := &fakeExecRunner{errFor: map[string]error{"bg": errors.New("boom")}, ran: ran}
	p := &shellexecPlugin{
		preCommit: []execCmd{{cmd: "bg", timeout: time.Second, failOnError: true, async: true}},
		runner:    fr, logger: silentLogger(),
	}
	terminalCalled := false
	terminal := func(_ context.Context, _ WriteOp) (WriteResult, error) {
		terminalCalled = true
		return WriteResult{Hash: "h"}, nil
	}
	h := chainWrite(terminal, p.WriteMiddleware()...)

	if _, err := h(context.Background(), NewWriteOp(Attribution{}, writeCSBytes("/w", "x"))); err != nil {
		t.Fatalf("async pre_commit must never abort: %v", err)
	}
	if !terminalCalled {
		t.Fatal("terminal must run for async pre_commit")
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("async command did not run")
	}
}

func TestNewShellexec_DefaultsAndValidation(t *testing.T) {
	cfg := config.ShellexecConfig{
		PreRead:   []config.ShellexecCmd{{Cmd: "pull"}}, // defaults: 30s timeout, 2s debounce, fail_on_error true
		PreCommit: []config.ShellexecCmd{{Cmd: "lint", Timeout: "5s"}},
	}
	p, err := newShellexec(cfg, "/d", &fakeExecRunner{}, silentLogger())
	if err != nil {
		t.Fatalf("newShellexec: %v", err)
	}
	if len(p.preRead) != 1 || p.preRead[0].timeout != defaultShellexecTimeout ||
		p.preRead[0].debounce != defaultShellexecDebounce || !p.preRead[0].failOnError {
		t.Fatalf("pre_read defaults = %+v", p.preRead)
	}
	if p.preCommit[0].timeout != 5*time.Second {
		t.Fatalf("pre_commit timeout = %v", p.preCommit[0].timeout)
	}

	// fail_on_error can be explicitly disabled.
	no := false
	p2, err := newShellexec(config.ShellexecConfig{PostWrite: []config.ShellexecCmd{{Cmd: "x", FailOnError: &no}}}, "", nil, silentLogger())
	if err != nil {
		t.Fatalf("newShellexec: %v", err)
	}
	if p2.postWrite[0].failOnError {
		t.Fatal("fail_on_error=false must disable failOnError")
	}

	// Empty cmd and bad duration are errors.
	if _, err := newShellexec(config.ShellexecConfig{PreRead: []config.ShellexecCmd{{Cmd: ""}}}, "", nil, silentLogger()); err == nil {
		t.Fatal("want error for empty cmd")
	}
	if _, err := newShellexec(config.ShellexecConfig{PreRead: []config.ShellexecCmd{{Cmd: "x", Timeout: "bogus"}}}, "", nil, silentLogger()); err == nil {
		t.Fatal("want error for bad timeout")
	}
}
