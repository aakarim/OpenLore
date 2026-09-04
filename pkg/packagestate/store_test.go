package packagestate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type testMapper map[string]string

func (m testMapper) HostDir(dir string) (string, bool) { host, ok := m[dir]; return host, ok }

func collect(t *testing.T, d DirStore, key string) [][]byte {
	t.Helper()
	var out [][]byte
	for record, err := range d.Records(key) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	return out
}

func TestStoreRoundTripMoveRemoveReplaceAndEscaping(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	store := Open(testMapper{"/a": a, "/b": b}, "Github.com/X")
	da, _ := store.Dir(context.Background(), "/a")
	db, _ := store.Dir(context.Background(), "/b")
	if err := da.Append("x.jsonl", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := da.Append("x.jsonl", []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, da, "x.jsonl"); !reflect.DeepEqual(got, [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`)}) {
		t.Fatalf("records=%q", got)
	}
	if err := da.Replace("x.jsonl", [][]byte{[]byte(`{"n":3}`)}); err != nil {
		t.Fatal(err)
	}
	if err := da.Move("x.jsonl", db, "y.jsonl"); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, db, "y.jsonl"); len(got) != 1 || string(got[0]) != `{"n":3}` {
		t.Fatalf("moved=%q", got)
	}
	if err := db.Remove("y.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a, ".lore", "!github.com", "!x")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRefusesSymlinkEscape(t *testing.T) {
	host, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(host, ".lore"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(host, ".lore", "size")); err != nil {
		t.Fatal(err)
	}
	d, _ := Open(testMapper{"/": host}, "size").Dir(context.Background(), "/")
	if err := d.Append("x.jsonl", []byte("x")); err == nil {
		t.Fatal("symlink escape succeeded")
	}
}

func TestStoreRejectsUnsafePackagePaths(t *testing.T) {
	for _, pkg := range []string{"", ".", "..", "../other", "other/..", "/root", "a//b", `a\b`, "a:b"} {
		if _, err := Open(testMapper{"/": t.TempDir()}, pkg).Dir(context.Background(), "/"); err == nil {
			t.Errorf("package %q accepted", pkg)
		}
	}
}
