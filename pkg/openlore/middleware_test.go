package openlore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func admitOp(target string) WriteOp {
	return NewWriteOp(Attribution{Principal: "agent-1"}, vfs.ChangeSet{Target: target, Action: vfs.ChangeActionWrite,
		Write: &vfs.WriteChange{Bytes: []byte("x")}})
}

// recordMW appends label to *order when it runs, then calls next.
func recordMW(order *[]string, label string) WriteMiddleware {
	return func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			*order = append(*order, label)
			return next(ctx, op)
		}
	}
}

func TestChainWrite_AllowsThroughInRegistrationOrder(t *testing.T) {
	var order []string
	terminal := func(ctx context.Context, op WriteOp) (WriteResult, error) {
		order = append(order, "terminal")
		return WriteResult{Hash: "h"}, nil
	}
	h := chainWrite(terminal, recordMW(&order, "a"), recordMW(&order, "b"))

	res, err := h(context.Background(), admitOp("/x"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Hash != "h" {
		t.Fatalf("hash = %q", res.Hash)
	}
	if got := fmt.Sprint(order); got != "[a b terminal]" {
		t.Fatalf("order = %s, want [a b terminal]", got)
	}
}

func TestChainWrite_DeferShortCircuitsBeforeTerminal(t *testing.T) {
	var order []string
	terminalCalled := false
	terminal := func(ctx context.Context, op WriteOp) (WriteResult, error) {
		terminalCalled = true
		return WriteResult{}, nil
	}
	// "b" defers: parks a pending change and does NOT call next.
	deferMW := func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			order = append(order, "b-defer")
			return WriteResult{}, op.Pending("req-1")
		}
	}
	h := chainWrite(terminal, recordMW(&order, "a"), deferMW, recordMW(&order, "c"))

	_, err := h(context.Background(), admitOp("/x"))
	var pce *vfs.PendingChangeError
	if !errors.As(err, &pce) {
		t.Fatalf("want *PendingChangeError, got %v", err)
	}
	if pce.Ref != "req-1" {
		t.Fatalf("ref = %q", pce.Ref)
	}
	if terminalCalled {
		t.Fatal("terminal must not run when a middleware defers")
	}
	if got := fmt.Sprint(order); got != "[a b-defer]" {
		t.Fatalf("order = %s, want [a b-defer] (c must not run)", got)
	}
}

func TestChainWrite_RejectShortCircuits(t *testing.T) {
	terminalCalled := false
	terminal := func(ctx context.Context, op WriteOp) (WriteResult, error) {
		terminalCalled = true
		return WriteResult{}, nil
	}
	boom := errors.New("denied")
	rejectMW := func(next WriteHandler) WriteHandler {
		return func(ctx context.Context, op WriteOp) (WriteResult, error) {
			return WriteResult{}, boom
		}
	}
	h := chainWrite(terminal, rejectMW)
	if _, err := h(context.Background(), admitOp("/x")); !errors.Is(err, boom) {
		t.Fatalf("want denied, got %v", err)
	}
	if terminalCalled {
		t.Fatal("terminal must not run when a middleware rejects")
	}
}

func TestChainRead_AbortsOnError(t *testing.T) {
	var order []string
	terminalCalled := false
	terminal := func(ctx context.Context, op ReadOp) error {
		terminalCalled = true
		return nil
	}
	boom := errors.New("pull failed")
	gate := func(next ReadHandler) ReadHandler {
		return func(ctx context.Context, op ReadOp) error {
			order = append(order, "gate")
			return boom
		}
	}
	h := chainRead(terminal, gate)
	if err := h(context.Background(), ReadOp{Path: "/a", Kind: ReadKindFile}); !errors.Is(err, boom) {
		t.Fatalf("want pull failed, got %v", err)
	}
	if terminalCalled {
		t.Fatal("terminal read must not run when a read middleware aborts")
	}
}

func TestChainPostCommit_RunsAllInOrder(t *testing.T) {
	var order []string
	mk := func(label string) PostCommitMiddleware {
		return func(next PostCommitHandler) PostCommitHandler {
			return func(ctx context.Context, info CommitInfo) error {
				order = append(order, label)
				return next(ctx, info)
			}
		}
	}
	terminal := func(ctx context.Context, info CommitInfo) error {
		order = append(order, "terminal")
		return nil
	}
	h := chainPostCommit(terminal, mk("feed"), mk("hook"))
	if err := h(context.Background(), CommitInfo{Hash: "h"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := fmt.Sprint(order); got != "[feed hook terminal]" {
		t.Fatalf("order = %s, want [feed hook terminal]", got)
	}
}
