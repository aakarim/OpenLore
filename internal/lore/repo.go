// Package lore holds the lore client's logic: knowledge-repository layout,
// source definitions, scanning and syncing. It takes data and returns data;
// the command line, prompts and process execution live in cmd/lore.
package lore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
)

// Repository is a checked-out knowledge repository and its server config.
type Repository struct {
	Root   string
	Config config.Config
}

// IsKnowledgeRepository reports whether dir holds an openlore.yml.
func IsKnowledgeRepository(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "openlore.yml"))
	return err == nil && !info.IsDir()
}

// FindRoot walks up from cwd to the enclosing knowledge repository, falling
// back to the one the last lore login recorded so lore also works from a
// Claude project directory.
func FindRoot(cwd string) (string, error) {
	p := cwd
	for {
		if IsKnowledgeRepository(p) {
			return filepath.EvalSymlinks(p)
		}
		parent := filepath.Dir(p)
		if p == parent {
			break
		}
		p = parent
	}
	if root, err := DefaultRoot(); err == nil && root != "" && IsKnowledgeRepository(root) {
		return root, nil
	}
	return "", fmt.Errorf("not inside a knowledge repository (openlore.yml not found); run lore login from one to make it the default")
}

// SourcesFile is the committed source definitions of a login identity; the
// caller must have resolved the identity first.
func (r Repository) SourcesFile(identity string) string {
	return config.HomeSourcesFile(r.Root, identity)
}

func (r Repository) stateFile() string { return filepath.Join(r.Root, ".local", "sync.json") }

// sourceDir is the local cache of one store. Keying it by the store rather
// than the source name keeps two identities' same-named sources apart while
// letting them share a clone of the same git remote.
func (r Repository) sourceDir(store string) string {
	return filepath.Join(r.Root, ".local", "sources", filepath.FromSlash(store))
}

// Projections maps each store named by a lore.json docset projection to the
// sorted docset names projecting it.
func (r Repository) Projections() (map[string][]string, error) {
	var lore struct {
		Docsets map[string]struct {
			Projection *struct {
				Store string `json:"store"`
			} `json:"projection"`
		} `json:"docsets"`
	}
	b, err := os.ReadFile(filepath.Join(r.Root, "lore.json"))
	if err == nil {
		if err = json.Unmarshal(b, &lore); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stores := map[string][]string{}
	for name, d := range lore.Docsets {
		if d.Projection != nil {
			stores[d.Projection.Store] = append(stores[d.Projection.Store], name)
		}
	}
	for _, names := range stores {
		slices.Sort(names)
	}
	return stores, nil
}

// expand resolves ~ and makes p absolute relative to root.
func expand(p, root string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, strings.TrimPrefix(p, "~/"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return filepath.Clean(p), nil
}
