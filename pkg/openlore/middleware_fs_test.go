package openlore

import (
	"context"
	"errors"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// mfsReadView is a no-op read view; middlewareFS only delegates reads to it.
type mfsReadView struct{ vfs.FileSystem }

func TestMiddlewareFS_MutationsMapToChangeSets(t *testing.T) {
	var got WriteOp
	admit := func(_ context.Context, op WriteOp) (WriteResult, error) {
		got = op
		return WriteResult{Hash: "committed"}, nil
	}
	m := newMiddlewareFS(mfsReadView{}, Attribution{Principal: "agent-1"}, admit)

	base := "b0"
	h, err := m.WriteFileAtomic("/w", []byte("hi"), vfs.WriteOpts{IfMatch: &base})
	if err != nil || h != "committed" {
		t.Fatalf("write: h=%q err=%v", h, err)
	}
	if got.Attribution.Principal != "agent-1" {
		t.Fatalf("attribution = %+v", got.Attribution)
	}
	if got.Leaves()[0].Action != vfs.ChangeActionWrite || got.Leaves()[0].Target != "/w" ||
		got.Leaves()[0].Write == nil || string(got.Leaves()[0].Write.Bytes) != "hi" ||
		got.Leaves()[0].Write.Opts.IfMatch == nil || *got.Leaves()[0].Write.Opts.IfMatch != "b0" {
		t.Fatalf("write changeset = %+v", got.Leaves())
	}

	if err := m.Mkdir("/d"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got.Leaves()[0].Action != vfs.ChangeActionMkdir || got.Leaves()[0].Target != "/d" {
		t.Fatalf("mkdir changeset = %+v", got.Leaves())
	}

	if err := m.MkdirAll("/d/e/f"); err != nil {
		t.Fatalf("mkdir_all: %v", err)
	}
	if got.Leaves()[0].Action != vfs.ChangeActionMkdirAll || got.Leaves()[0].Target != "/d/e/f" {
		t.Fatalf("mkdir_all changeset = %+v", got.Leaves())
	}

	if err := m.Remove("/f"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got.Leaves()[0].Action != vfs.ChangeActionRemove || got.Leaves()[0].Target != "/f" {
		t.Fatalf("remove changeset = %+v", got.Leaves())
	}

	snap := vfs.TreeSnapshot{Root: "/t"}
	if err := m.RemoveAll("/t", vfs.RemoveOpts{Expected: &snap}); err != nil {
		t.Fatalf("remove_all: %v", err)
	}
	if got.Leaves()[0].Action != vfs.ChangeActionRemoveAll || got.Leaves()[0].RemoveAll == nil ||
		got.Leaves()[0].RemoveAll.Opts.Expected == nil || got.Leaves()[0].RemoveAll.Opts.Expected.Root != "/t" {
		t.Fatalf("remove_all changeset = %+v", got.Leaves())
	}
}

func TestMiddlewareFS_PendingAndRejectPropagate(t *testing.T) {
	pending := &vfs.PendingChangeError{Ref: "req-7"}
	m := newMiddlewareFS(mfsReadView{}, Attribution{}, func(_ context.Context, _ WriteOp) (WriteResult, error) {
		return WriteResult{}, pending
	})
	_, err := m.WriteFileAtomic("/w", []byte("x"), vfs.WriteOpts{})
	var pce *vfs.PendingChangeError
	if !errors.As(err, &pce) || pce.Ref != "req-7" {
		t.Fatalf("want pending req-7, got %v", err)
	}

	boom := errors.New("denied")
	m2 := newMiddlewareFS(mfsReadView{}, Attribution{}, func(_ context.Context, _ WriteOp) (WriteResult, error) {
		return WriteResult{}, boom
	})
	if err := m2.Remove("/x"); !errors.Is(err, boom) {
		t.Fatalf("want denied, got %v", err)
	}
}
