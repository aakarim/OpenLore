package openlore

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInboxTokenStorePersistencePermissionsAndRedaction(t *testing.T) {
	dir := t.TempDir()
	store, err := NewInboxTokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	created, err := store.Create("alice", "ci", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.Credential()[:5] != "olin_" {
		t.Fatal("create did not return credential secret")
	}
	info, err := os.Stat(filepath.Join(dir, "auth", "inbox_tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("store mode = %o", info.Mode().Perm())
	}
	parent, _ := os.Stat(filepath.Join(dir, "auth"))
	if parent.Mode().Perm() != 0700 {
		t.Fatalf("parent mode = %o", parent.Mode().Perm())
	}
	reopened, err := NewInboxTokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.Get(created.ID)
	if err != nil || !ok || got.Secret != created.Secret {
		t.Fatalf("persisted token = %#v, %v, %v", got, ok, err)
	}
	deleted, err := reopened.Delete(created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if _, ok, _ := reopened.Get(created.ID); ok {
		t.Fatal("revoked token still present")
	}
}

func TestInboxCredentialGeneratedRoundTrip(t *testing.T) {
	store, err := NewInboxTokenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		tok, err := store.Create("alice", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tok.ID, "_") {
			t.Fatalf("ID contains separator: %q", tok.ID)
		}
		id, secret, ok := parseInboxCredential(tok.Credential())
		if !ok || id != tok.ID || secret != tok.Secret {
			t.Fatalf("round trip failed for %q", tok.Credential())
		}
	}
}

func TestInboxTokenStoreIndependentConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewInboxTokenStore(dir)
	b, _ := NewInboxTokenStore(dir)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := a.Create("a", "", nil); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := b.Create("b", "", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	tokens, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 80 {
		t.Fatalf("lost creates: got %d", len(tokens))
	}
	for _, tok := range tokens {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			store := a
			if id[len(id)-1]%2 == 0 {
				store = b
			}
			if _, err := store.Delete(id); err != nil {
				t.Error(err)
			}
		}(tok.ID)
	}
	wg.Wait()
	tokens, err = b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("revocations resurrected: %d remain", len(tokens))
	}
}
