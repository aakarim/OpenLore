package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAuthConfigRulesAndLegacyOKF(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lore.json")
	data := `{"rules":{"size":{"match":["**/*.md"],"use":"size/lines","with":{"max":3}}},"docsets":{"docs":{"paths":["/docs"],"okf":{}}},"identities":[]}`
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := LoadAuthConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if auth.Rules["size"].Use != "size/lines" || auth.Docsets["docs"].Rules["okf"].Use != "okf" || auth.Docsets["docs"].Rules["okf/bundle"].Use != "okf/bundle" {
		t.Fatalf("rules not loaded/desugared: %#v", auth)
	}
}

func TestLoadAuthConfigRejectsConflictingLegacyOKFRule(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lore.json")
	data := `{"docsets":{"docs":{"paths":["/docs"],"okf":{},"rules":{"okf":{"match":["**/*.txt"],"use":"okf"}}}},"identities":[]}`
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAuthConfig(file)
	if err == nil || !strings.Contains(err.Error(), "okf") || !strings.Contains(err.Error(), "rules.okf") {
		t.Fatalf("error=%v", err)
	}
}

func TestRulesDeploymentConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openlore.yml")
	if err := os.WriteFile(file, []byte("rules:\n  growth: 1.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := New(WithConfigFile(file))
	if err != nil || cfg.Rules.Growth != 1.5 {
		t.Fatalf("growth=%v err=%v", cfg.Rules.Growth, err)
	}
	if err := os.WriteFile(file, []byte("rules:\n  tokenizer: o200k_base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(WithConfigFile(file)); err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("error=%v", err)
	}
}
