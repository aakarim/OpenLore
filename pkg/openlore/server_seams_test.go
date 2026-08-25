package openlore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// deferMWProvider is a test WriteMiddlewareProvider that defers (parks) any write
// whose target has gatedPrefix, and passes everything else through.
type deferMWProvider struct {
	gatedPrefix string
	ref         string
}

func (p deferMWProvider) WriteMiddleware() []WriteMiddleware {
	return []WriteMiddleware{func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			for _, leaf := range op.Leaves() {
				if strings.HasPrefix(leaf.Target, p.gatedPrefix) {
					return WriteResult{}, op.Pending(p.ref)
				}
			}
			return next(ctx, op)
		}
	}}
}

// recordPostCommit is a test PostCommitProvider that records every CommitInfo.
type recordPostCommit struct {
	mu    sync.Mutex
	infos []CommitInfo
}

func (r *recordPostCommit) PostCommitMiddleware() []PostCommitMiddleware {
	return []PostCommitMiddleware{func(next PostCommitHandler) PostCommitHandler {
		return func(ctx context.Context, info CommitInfo) error {
			r.mu.Lock()
			r.infos = append(r.infos, info)
			r.mu.Unlock()
			return next(ctx, info)
		}
	}}
}

func (r *recordPostCommit) snapshot() []CommitInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CommitInfo(nil), r.infos...)
}

func newSeamServer(t *testing.T, root vfs.WritableFS) *Server {
	t.Helper()
	s, err := NewServerWithRootFS(root, WithReadonly(false), config.WithDataDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServerWithRootFS: %v", err)
	}
	if s.writeLog == nil {
		t.Fatal("NewServerWithRootFS(WithReadonly(false)) must build a write log")
	}
	return s
}

func TestNewServerWithRootFS_WriteLogLive(t *testing.T) {
	fs := &wlRecordingFS{}
	s := newSeamServer(t, fs)

	res, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "a"}, writeCS("/x"))
	if err != nil {
		t.Fatalf("CommitChangeSet: %v", err)
	}
	if res.Hash != "h:/x" {
		t.Fatalf("hash = %q, want h:/x", res.Hash)
	}
	if got := fs.order(); len(got) != 1 || got[0] != "/x" {
		t.Fatalf("applied = %v, want [/x]", got)
	}
}

func TestUnsupportedShellUsageIsLoggedOnlyInDebugMode(t *testing.T) {
	for _, tt := range []struct {
		name  string
		debug bool
		want  bool
	}{
		{name: "debug", debug: true, want: true},
		{name: "non-debug", debug: false, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			s, err := NewServer("", WithLogger(logger), WithDebug(tt.debug))
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}

			sh := s.buildSessionShell(Identity{IdentityName: "agent-1"})
			sh.ExecPipeline("missing-command private-argument", &bytes.Buffer{}, &bytes.Buffer{}, nil)
			sh.ExecPipeline("| echo hello", &bytes.Buffer{}, &bytes.Buffer{}, nil)

			got := logs.String()
			if (got != "") != tt.want {
				t.Fatalf("logs = %q, want event: %t", got, tt.want)
			}
			if !tt.want {
				return
			}
			for _, want := range []string{
				`"level":"DEBUG"`,
				`"msg":"unsupported shell usage"`,
				`"kind":"unknown_command"`,
				`"command":"missing-command"`,
				`"kind":"unknown_syntax"`,
				`"syntax":"| echo hello"`,
				`"identity":"agent-1"`,
			} {
				if !strings.Contains(got, want) {
					t.Errorf("logs missing %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "private-argument") {
				t.Fatalf("unknown-command arguments leaked into logs:\n%s", got)
			}
		})
	}
}

func TestNewServerWarnsForInvalidConfigurationValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(path, []byte("passkeys: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	if _, err := NewServer("", WithConfigFile(path), WithLogger(logger)); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	got := logs.String()
	if !strings.Contains(got, `"level":"WARN"`) ||
		!strings.Contains(got, `"msg":"ignoring invalid configuration value"`) ||
		!strings.Contains(got, "passkeysYAML") {
		t.Fatalf("warning log = %q", got)
	}
}

func TestRegisterPlugin_WriteMiddlewareGatesLaterWrite(t *testing.T) {
	fs := &wlRecordingFS{}
	s := newSeamServer(t, fs)

	// Registered AFTER construction — must take effect on the composed chain.
	if err := s.RegisterPlugin(deferMWProvider{gatedPrefix: "/gated", ref: "held-1"}); err != nil {
		t.Fatal(err)
	}

	chain := s.writeChain()

	// A gated write is deferred (parked) and never reaches the substrate.
	_, err := chain(context.Background(), NewWriteOp(Attribution{Principal: "a"}, writeCS("/gated/a")))
	var pce *vfs.PendingChangeError
	if !errors.As(err, &pce) || pce.Ref != "held-1" {
		t.Fatalf("gated write: want PendingChangeError held-1, got %v", err)
	}
	if len(fs.order()) != 0 {
		t.Fatalf("gated write must not commit; applied = %v", fs.order())
	}

	// An ungated write commits.
	res, err := chain(context.Background(), NewWriteOp(Attribution{Principal: "a"}, writeCS("/ok")))
	if err != nil || res.Hash != "h:/ok" {
		t.Fatalf("ungated write: h=%q err=%v", res.Hash, err)
	}
	if got := fs.order(); len(got) != 1 || got[0] != "/ok" {
		t.Fatalf("applied = %v, want [/ok]", got)
	}
}

func TestRegisterPlugin_DefersBatchWhenLaterLeafMatches(t *testing.T) {
	fs := &wlRecordingFS{}
	s := newSeamServer(t, fs)
	if err := s.RegisterPlugin(deferMWProvider{gatedPrefix: "/gated", ref: "held-batch"}); err != nil {
		t.Fatal(err)
	}
	cs := vfs.ChangeSet{Changes: []vfs.Change{
		writeCS("/safe").Leaves()[0],
		writeCS("/gated/later").Leaves()[0],
	}}
	_, err := s.writeChain()(context.Background(), NewWriteOp(Attribution{Principal: "a"}, cs))
	var pending *vfs.PendingChangeError
	if !errors.As(err, &pending) || pending.Ref != "held-batch" || len(pending.ChangeSet.Changes) != 2 || len(fs.order()) != 0 {
		t.Fatalf("whole batch not deferred: pending=%+v applied=%v err=%v", pending, fs.order(), err)
	}
}

func TestRegisterPlugin_PostCommitFiresAfterConstruction(t *testing.T) {
	fs := &wlRecordingFS{}
	s := newSeamServer(t, fs)

	rec := &recordPostCommit{}
	if err := s.RegisterPlugin(rec); err != nil { // post-commit provider registered after the log was built
		t.Fatal(err)
	}

	_, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "alice", Extra: map[string]string{"approver": "bob"}}, writeCS("/x"))
	if err != nil {
		t.Fatalf("CommitChangeSet: %v", err)
	}
	infos := rec.snapshot()
	if len(infos) != 1 {
		t.Fatalf("post-commit fired %d times, want 1", len(infos))
	}
	if infos[0].Attribution.Principal != "alice" || infos[0].Attribution.Extra["approver"] != "bob" {
		t.Fatalf("attribution = %+v", infos[0].Attribution)
	}
	if infos[0].Hash != "h:/x" || infos[0].ChangeSet.Target != "/x" {
		t.Fatalf("commit info = %+v", infos[0])
	}
}

func TestCommitChangeSet_SkipsAdmission(t *testing.T) {
	fs := &wlRecordingFS{}
	s := newSeamServer(t, fs)

	// A middleware that defers EVERY write. Admission (writeChain) would park.
	if err := s.RegisterPlugin(deferMWProvider{gatedPrefix: "/", ref: "held"}); err != nil {
		t.Fatal(err)
	}

	// Sanity: admission defers.
	if _, err := s.writeChain()(context.Background(), NewWriteOp(Attribution{Principal: "a"}, writeCS("/x"))); err == nil {
		t.Fatal("admission chain should defer every write in this test")
	}

	// CommitChangeSet bypasses admission and commits directly.
	res, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "a"}, writeCS("/x"))
	if err != nil {
		t.Fatalf("CommitChangeSet must skip admission and commit: %v", err)
	}
	if res.Hash != "h:/x" {
		t.Fatalf("hash = %q, want h:/x", res.Hash)
	}
	if got := fs.order(); len(got) != 1 || got[0] != "/x" {
		t.Fatalf("applied = %v, want [/x]", got)
	}
}

func TestCommitChangeSet_CASErrorPropagates(t *testing.T) {
	base := "b0"
	fs := &wlRecordingFS{errFor: map[string]error{"/x": &vfs.PreconditionError{Path: "/x", Current: "now"}}}
	s := newSeamServer(t, fs)

	_, err := s.CommitChangeSet(context.Background(), Attribution{Principal: "a"}, vfs.ChangeSet{
		Target: "/x",
		Action: vfs.ChangeActionWrite,
		Write:  &vfs.WriteChange{Bytes: []byte("x"), Opts: vfs.WriteOpts{IfMatch: &base}},
	})
	var pe *vfs.PreconditionError
	if !errors.As(err, &pe) {
		t.Fatalf("want *vfs.PreconditionError, got %v", err)
	}
}

func TestCommitChangeSet_ReadonlyServer(t *testing.T) {
	// Default config is read-only → no write log → CommitChangeSet is read-only.
	s, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := s.CommitChangeSet(context.Background(), Attribution{}, writeCS("/x")); err == nil {
		t.Fatal("CommitChangeSet on a read-only server must return an error")
	}
}
