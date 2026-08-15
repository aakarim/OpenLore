package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func inboxCLIConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "openlore.yml")
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"identities":[{"name":"alice"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data_dir: "+filepath.Join(dir, "data")+"\nauth_file: "+authPath+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInboxTokenCreateRequiresAuthFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "openlore.yml")
	if err := os.WriteFile(configPath, []byte("data_dir: "+filepath.Join(dir, "data")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := inboxTokenCommand([]string{"create", "--identity", "alice", "--config", configPath}, &bytes.Buffer{}, &bytes.Buffer{}, time.Now)
	if err == nil || !strings.Contains(err.Error(), "auth_file") {
		t.Fatalf("error=%v", err)
	}
}

func TestInboxTokenCommandTTLAndDocumentedRevokeOrder(t *testing.T) {
	configPath := inboxCLIConfig(t)
	var stdout, stderr bytes.Buffer
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	if err := inboxTokenCommand([]string{"create", "--identity", "alice", "--ttl", "0", "--config", configPath}, &stdout, &stderr, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	credential := strings.TrimSpace(stdout.String())
	idAndSecret := strings.TrimPrefix(credential, "olin_")
	tokenID, _, ok := strings.Cut(idAndSecret, "_")
	if !ok || tokenID == "" {
		t.Fatalf("credential=%q", credential)
	}
	stdout.Reset()
	if err := inboxTokenCommand([]string{"list", "--config", configPath}, &stdout, &stderr, time.Now); err != nil || !strings.Contains(stdout.String(), "\tnever\n") {
		t.Fatalf("zero TTL list=%q err=%v", stdout.String(), err)
	}
	if err := inboxTokenCommand([]string{"revoke", tokenID, "--config", configPath}, &stdout, &stderr, time.Now); err != nil {
		t.Fatalf("documented revoke form: %v", err)
	}
	if err := inboxTokenCommand([]string{"create", "--identity", "alice", "--ttl", "-1s", "--config", configPath}, &stdout, &stderr, time.Now); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative TTL error=%v", err)
	}
}
