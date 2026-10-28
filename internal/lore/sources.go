package lore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"gopkg.in/yaml.v3"
)

// SourceDocument edits sources.yaml as a YAML node tree so comments and
// ordering survive a round trip.
type SourceDocument struct{ node yaml.Node }

// LoadSourceDocument parses file, or an empty definition when it is missing.
func LoadSourceDocument(file string) (*SourceDocument, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		b = []byte("version: \"1\"\nsources: []\n")
	} else if err != nil {
		return nil, err
	}
	if _, err := config.ParseSources(b); err != nil {
		return nil, err
	}
	var d SourceDocument
	err = yaml.Unmarshal(b, &d.node)
	return &d, err
}

func (d *SourceDocument) sequence() *yaml.Node {
	m := d.node.Content[0]
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == "sources" {
			return m.Content[i+1]
		}
	}
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "sources", Tag: "!!str"}, n)
	return n
}

// Append adds a definition, rendering the sources list in block style.
func (d *SourceDocument) Append(s config.SourceSpec) error {
	var n yaml.Node
	if err := n.Encode(s); err != nil {
		return err
	}
	seq := d.sequence()
	seq.Style = 0
	seq.Content = append(seq.Content, &n)
	return nil
}

// Remove drops the definition called name.
func (d *SourceDocument) Remove(name string) (config.SourceSpec, error) {
	seq := d.sequence()
	for i, n := range seq.Content {
		var s config.SourceSpec
		if err := n.Decode(&s); err != nil {
			return s, err
		}
		if s.Name == name {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			return s, nil
		}
	}
	return config.SourceSpec{}, fmt.Errorf("unknown source %q", name)
}

// Validate checks that the edited document still parses as sources.yaml.
func (d *SourceDocument) Validate() error {
	encoded, err := yaml.Marshal(&d.node)
	if err != nil {
		return err
	}
	_, err = config.ParseSources(encoded)
	return err
}

// Save validates and atomically writes the document to file.
func (d *SourceDocument) Save(file string) error {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(&d.node); err != nil {
		return err
	}
	e.Close()
	if _, err := config.ParseSources(b.Bytes()); err != nil {
		return err
	}
	return atomicFile(file, b.Bytes(), 0644)
}

// StorePath is the server store a source syncs to under identity; an empty
// identity renders as a placeholder.
func (r Repository) StorePath(s config.SourceSpec, identity string) (string, error) {
	switch s.Type {
	case "folder":
		if identity == "" {
			identity = "<identity>"
		}
		return "folder/" + identity + "/" + s.Name, nil
	case "git":
		return config.GitSourceStore(s.URL)
	case "claude":
		_, _, store, err := r.claudeLocation(s, identity)
		return store, err
	default:
		return "", fmt.Errorf("unsupported source type %s", s.Type)
	}
}

// Origin describes where a source's files come from.
func Origin(s config.SourceSpec) string {
	switch s.Type {
	case "git":
		return s.URL
	case "claude":
		return s.Project + " (" + s.Group + ")"
	}
	return s.Path
}

// FolderSource defines a folder source for path, taken relative to the
// working directory, and returns it with the resolved directory.
func (r Repository) FolderSource(path, name string, include, exclude []string) (config.SourceSpec, string, error) {
	spec := config.SourceSpec{Name: name, Type: "folder", Include: include, Exclude: exclude}
	cwd, err := os.Getwd()
	if err != nil {
		return spec, "", err
	}
	origin, err := expand(path, cwd)
	if err != nil {
		return spec, "", err
	}
	info, err := os.Stat(origin)
	if err != nil {
		return spec, "", err
	}
	if !info.IsDir() {
		return spec, "", fmt.Errorf("folder source must be a directory")
	}
	spec.Path = portablePath(origin, r.Root)
	if spec.Name == "" {
		spec.Name = filepath.Base(origin)
	}
	return spec, origin, nil
}

// GitSource defines a git source for a validated remote URL.
func GitSource(url, name, branch string, include, exclude []string) (config.SourceSpec, error) {
	store, err := config.GitSourceStore(url)
	if err != nil {
		return config.SourceSpec{}, err
	}
	spec := config.SourceSpec{Name: name, Type: "git", URL: url, Branch: branch, Include: include, Exclude: exclude}
	if spec.Name == "" {
		spec.Name = filepath.Base(store)
	}
	return spec, nil
}

// IsGitOrigin reports whether a source add argument names a git remote.
func IsGitOrigin(origin string) bool {
	return strings.Contains(origin, "://") || strings.HasPrefix(origin, "git@")
}

// portablePath renders p relative to the home directory or the repository
// so a committed definition works on other machines.
func portablePath(p, root string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return p
}
