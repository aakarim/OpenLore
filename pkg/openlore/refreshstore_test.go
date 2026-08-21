package openlore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testRefreshStore(t *testing.T) *fileRefreshStore {
	t.Helper()
	rs, err := newFileRefreshStore(filepath.Join(t.TempDir(), "refresh.json"))
	if err != nil {
		t.Fatalf("newFileRefreshStore: %v", err)
	}
	return rs
}

func TestRefreshStore_SaveLookup(t *testing.T) {
	rs := testRefreshStore(t)
	rt := RefreshToken{Token: "a", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := rs.Save(rt); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := rs.Lookup("a")
	if err != nil || !ok {
		t.Fatalf("Lookup: ok=%v err=%v", ok, err)
	}
	if got.Subject != "alice" {
		t.Errorf("subject = %q", got.Subject)
	}
}

func TestRefreshStore_RotateIssuesNewAndConsumesOld(t *testing.T) {
	rs := testRefreshStore(t)
	old := RefreshToken{Token: "old", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)}
	rs.Save(old)

	next := RefreshToken{Token: "new", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)}
	rotation, err := rs.Rotate("old", next)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotation.Token.Token != "new" || rotation.Retried {
		t.Fatalf("rotation = %+v", rotation)
	}
	// New token is present and unused.
	if got, ok, _ := rs.Lookup("new"); !ok || got.Used {
		t.Fatalf("new token missing or already used: ok=%v", ok)
	}
	// Old token is now marked used.
	if got, ok, _ := rs.Lookup("old"); !ok || !got.Used || got.ReplacedBy != "new" || got.RotatedAt.IsZero() {
		t.Fatalf("old token should point to its replacement: ok=%v token=%+v", ok, got)
	}
}

func TestRefreshStore_ImmediateRetryReturnsSameSuccessor(t *testing.T) {
	rs := testRefreshStore(t)
	rs.Save(RefreshToken{Token: "old", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := rs.Rotate("old", RefreshToken{Token: "new", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rotation, err := rs.Rotate("old", RefreshToken{Token: "discarded", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rotation.Token.Token != "new" || !rotation.Retried {
		t.Fatalf("retry rotation = %+v", rotation)
	}
	if _, ok, _ := rs.Lookup("discarded"); ok {
		t.Fatal("retry candidate must not be stored")
	}
}

func TestRefreshStore_ReuseAfterGraceRevokesChain(t *testing.T) {
	rs := testRefreshStore(t)
	rs.Save(RefreshToken{Token: "old", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := rs.Rotate("old", RefreshToken{Token: "new", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	old := rs.tokens["old"]
	old.RotatedAt = time.Now().Add(-refreshRetryGrace - time.Second)
	rs.tokens["old"] = old

	// Re-presenting the used token after the retry grace is theft → revoke.
	_, err := rs.Rotate("old", RefreshToken{Token: "attacker", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("expected ErrRefreshReuse, got %v", err)
	}
	// The entire chain is revoked: the previously-valid "new" is gone.
	if _, ok, _ := rs.Lookup("new"); ok {
		t.Fatalf("reuse must revoke the whole chain; 'new' still present")
	}
	if _, ok, _ := rs.Lookup("attacker"); ok {
		t.Fatalf("attacker token must not be stored")
	}
}

func TestRefreshStore_RotateUnknownInvalid(t *testing.T) {
	rs := testRefreshStore(t)
	_, err := rs.Rotate("nope", RefreshToken{Token: "x", ChainID: "c1"})
	if !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("expected ErrRefreshInvalid, got %v", err)
	}
}

func TestRefreshStore_RevokeDelegationRevokesMatchingDistinctChains(t *testing.T) {
	rs := testRefreshStore(t)
	expires := time.Now().Add(time.Hour)
	for _, rt := range []RefreshToken{
		{Token: "a1", Subject: "alice", Actor: "claude", ChainID: "c1", ExpiresAt: expires},
		{Token: "a2", Subject: "alice", Actor: "claude", ChainID: "c1", ExpiresAt: expires},
		{Token: "b", Subject: "alice", Actor: "claude", ChainID: "c2", ExpiresAt: expires},
		{Token: "other-principal", Subject: "bob", Actor: "claude", ChainID: "c3", ExpiresAt: expires},
		{Token: "other-actor", Subject: "alice", Actor: "chatgpt", ChainID: "c4", ExpiresAt: expires},
	} {
		if err := rs.Save(rt); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	revoked, err := rs.RevokeDelegation("alice", "claude")
	if err != nil || revoked != 2 {
		t.Fatalf("RevokeDelegation: revoked=%d err=%v", revoked, err)
	}
	for _, token := range []string{"a1", "a2", "b"} {
		if _, ok, _ := rs.Lookup(token); ok {
			t.Errorf("matching token %q was not revoked", token)
		}
	}
	for _, token := range []string{"other-principal", "other-actor"} {
		if _, ok, _ := rs.Lookup(token); !ok {
			t.Errorf("unrelated token %q was revoked", token)
		}
	}
}

func TestRefreshStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "refresh.json")
	rs, _ := newFileRefreshStore(path)
	rs.Save(RefreshToken{Token: "old", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := rs.Rotate("old", RefreshToken{Token: "new", Subject: "alice", ChainID: "c1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	// A fresh store reading the same file can safely satisfy an in-flight retry.
	rs2, err := newFileRefreshStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rotation, err := rs2.Rotate("old", RefreshToken{Token: "discarded", ChainID: "c1"})
	if err != nil || !rotation.Retried || rotation.Token.Token != "new" {
		t.Fatalf("persisted retry: rotation=%+v err=%v", rotation, err)
	}
}
