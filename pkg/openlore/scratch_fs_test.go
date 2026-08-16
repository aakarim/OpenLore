package openlore

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestSessionScratch_RedirectEditAndWriteBack(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(rootDir+"/docs", 0o755); err != nil {
		t.Fatal(err)
	}
	root := NewDirFS(rootDir, config.FilesConfig{}).WithDocsetRoots([]string{"/docs"})
	if err := root.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.WriteFileAtomic("/docs/index.md", []byte("old value\n"), vfs.WriteOpts{}); err != nil {
		t.Fatal(err)
	}

	server, err := NewServerWithRootFS(root, WithReadonly(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	sh := server.buildSessionShell(Identity{IdentityName: "writer"})
	sh.SetEnv("P", "/docs")

	for _, command := range []string{
		"cat $P/index.md > /tmp/index.md",
		"sed -i 's/old/new/g' /tmp/index.md",
		"cat /tmp/index.md > /docs/index.md",
	} {
		var stdout, stderr bytes.Buffer
		if code := sh.Exec(command, &stdout, &stderr, nil); code != 0 {
			t.Fatalf("%q failed: code=%d stdout=%q stderr=%q", command, code, stdout.String(), stderr.String())
		}
	}

	got, err := root.ReadFile("/docs/index.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new value\n" {
		t.Fatalf("write-back = %q, want %q", got, "new value\n")
	}
}

func TestSessionScratch_IsPrivateAndEphemeral(t *testing.T) {
	server, err := NewServerWithRootFS(NewDirFS(t.TempDir(), config.FilesConfig{}), WithReadonly(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	first := server.buildSessionFS(Identity{IdentityName: "one"})
	writable := first.(vfs.WritableFS)
	if _, err := writable.WriteFileAtomic("/tmp/private.md", []byte("secret"), vfs.WriteOpts{}); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	if got, err := first.ReadFile("/tmp/private.md"); err != nil || string(got) != "secret" {
		t.Fatalf("same-session read = %q, %v", got, err)
	}
	entries, err := first.ReadDir("/tmp")
	if err != nil || len(entries) != 1 || entries[0].Name() != "private.md" {
		t.Fatalf("scratch listing = %#v, %v", entries, err)
	}

	second := server.buildSessionFS(Identity{IdentityName: "two"})
	if _, err := second.ReadFile("/tmp/private.md"); err == nil {
		t.Fatal("scratch file leaked into another session")
	}
}
