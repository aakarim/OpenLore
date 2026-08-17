package openlore

import (
	"context"
	"errors"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// rcRecordingFS records which read methods were reached (i.e. passed the gate).
type rcRecordingFS struct {
	statted, readDir, readFile bool
}

func (f *rcRecordingFS) Stat(string) (*vfs.FileInfo, error) {
	f.statted = true
	return &vfs.FileInfo{}, nil
}
func (f *rcRecordingFS) ReadDir(string) ([]vfs.FileInfo, error) {
	f.readDir = true
	return nil, nil
}
func (f *rcRecordingFS) ReadFile(string) ([]byte, error) {
	f.readFile = true
	return []byte("bytes"), nil
}

func TestReadChainFS_GateRunsBeforeEachReadWithKindAndActor(t *testing.T) {
	inner := &rcRecordingFS{}
	var kinds []ReadKind
	var actorID string
	gate := func(_ context.Context, op ReadOp) error {
		kinds = append(kinds, op.Kind)
		actorID = op.Attribution.Principal
		return nil
	}
	r := newReadChainFS(inner, Attribution{Principal: "agent-2"}, gate)

	if _, err := r.Stat("/a"); err != nil {
		t.Fatalf("stat: %v", err)
	}
	if _, err := r.ReadDir("/a"); err != nil {
		t.Fatalf("readdir: %v", err)
	}
	b, err := r.ReadFile("/a/f")
	if err != nil || string(b) != "bytes" {
		t.Fatalf("readfile: b=%q err=%v", b, err)
	}
	if !inner.statted || !inner.readDir || !inner.readFile {
		t.Fatalf("inner not reached: %+v", inner)
	}
	want := []ReadKind{ReadKindStat, ReadKindDir, ReadKindFile}
	if len(kinds) != 3 || kinds[0] != want[0] || kinds[1] != want[1] || kinds[2] != want[2] {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if actorID != "agent-2" {
		t.Fatalf("actor = %q", actorID)
	}
}

func TestReadChainFS_GateAbortStopsRead(t *testing.T) {
	inner := &rcRecordingFS{}
	boom := errors.New("pull failed")
	r := newReadChainFS(inner, Attribution{}, func(_ context.Context, _ ReadOp) error { return boom })

	if _, err := r.ReadFile("/a"); !errors.Is(err, boom) {
		t.Fatalf("want pull failed, got %v", err)
	}
	if inner.readFile {
		t.Fatal("inner ReadFile must not run when the gate aborts")
	}
}
