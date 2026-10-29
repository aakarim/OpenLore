package lore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aakarim/go-openlore/internal/lore/api"
)

// Credentials is what lore login saves for one knowledge repository.
type Credentials = api.Credentials

// ConfigDir is the private per-user directory for credentials and defaults.
func ConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "lore"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "lore"), nil
}

type defaults struct {
	Root string `json:"root"`
}

// DefaultFile records the knowledge repository of the last lore login.
func DefaultFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "default.json"), nil
}

// DefaultRoot is the root the last lore login recorded.
func DefaultRoot() (string, error) {
	file, err := DefaultFile()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var d defaults
	if err := json.Unmarshal(b, &d); err != nil {
		return "", err
	}
	return d.Root, nil
}

// SaveDefaultRoot records root as the repository to use outside any tree.
func SaveDefaultRoot(root string) error {
	file, err := DefaultFile()
	if err != nil {
		return err
	}
	return privateJSON(file, defaults{Root: root})
}

// CredentialFile is the credentials file for the repository at root.
func CredentialFile(root string) (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

// LoadCredentials reads the credentials lore login saved for root.
func LoadCredentials(root string) (Credentials, error) {
	var cred Credentials
	file, err := CredentialFile(root)
	if err != nil {
		return cred, err
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return cred, fmt.Errorf("run lore login first: %w", err)
	}
	err = json.Unmarshal(b, &cred)
	return cred, err
}

// SaveCredentials writes cred privately for the repository at root.
func SaveCredentials(root string, cred Credentials) error {
	file, err := CredentialFile(root)
	if err != nil {
		return err
	}
	return privateJSON(file, cred)
}

func privateJSON(file string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(file, b, 0600)
}

func atomicFile(file string, b []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".lore-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
