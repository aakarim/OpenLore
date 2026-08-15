package openlore

import (
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestTransformedReadForwardsRawTrackedHash(t *testing.T) {
	d := NewDirFS(t.TempDir(), config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFileAtomic("/SKILL.md", []byte("raw"), vfs.WriteOpts{IfNoneMatch: true}); err != nil {
		t.Fatal(err)
	}
	tracked := newReadTrackingFS(d)
	f := &writableReadTransformFS{WritableFS: tracked, transforms: []ContentTransform{func(_ string, b []byte) []byte {
		return append(append([]byte(nil), b...), []byte("-status")...)
	}}}
	got, err := f.ReadFile("/SKILL.md")
	if err != nil || string(got) != "raw-status" {
		t.Fatalf("read=%q err=%v", got, err)
	}
	h, seen := f.LastReadHash("/SKILL.md")
	if !seen || h != hashBytes([]byte("raw")) {
		t.Fatalf("tracked hash=%q seen=%v", h, seen)
	}
	if _, err := f.WriteFileAtomic("/SKILL.md", []byte("updated"), vfs.WriteOpts{IfMatch: &h}); err != nil {
		t.Fatalf("surgical write with raw hash: %v", err)
	}
}
