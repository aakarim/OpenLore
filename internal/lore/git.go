package lore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
)

// RefreshGit brings the local clone of a git source up to date with its
// configured branch and returns the checkout directory, kept in the store's
// local cache.
func (r Repository) RefreshGit(s config.SourceSpec, store string) (string, error) {
	if _, err := config.GitSourceStore(s.URL); err != nil {
		return "", err
	}
	repo := filepath.Join(r.sourceDir(store), "repo")
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		b, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git: %s: %w", strings.TrimSpace(string(b)), err)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(repo), 0700); err != nil {
			return "", err
		}
		if err := run("clone", "--no-checkout", "--", s.URL, repo); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	// The remote is configuration-owned, never a value left in the cached clone.
	if err := run("-C", repo, "remote", "set-url", "origin", s.URL); err != nil {
		return "", err
	}
	branch := s.Branch
	if branch == "" {
		branch = "HEAD"
	}
	if strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("invalid branch")
	}
	if err := run("-C", repo, "fetch", "--prune", "origin", branch); err != nil {
		return "", err
	}
	if err := run("-C", repo, "checkout", "--force", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	if err := run("-C", repo, "clean", "-fdx"); err != nil {
		return "", err
	}
	return repo, nil
}
