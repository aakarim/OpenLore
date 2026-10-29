package config

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SourcesFile is committed project configuration, never machine credentials.
type SourcesFile struct {
	Version string       `yaml:"version"`
	Sources []SourceSpec `yaml:"sources"`
}

type SourceSpec struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Path    string   `yaml:"path,omitempty"`
	URL     string   `yaml:"url,omitempty"`
	Branch  string   `yaml:"branch,omitempty"`
	Group   string   `yaml:"group,omitempty"`
	Project string   `yaml:"project,omitempty"`
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

type ProjectionSpec struct {
	Store   string   `json:"store"`
	Subpath string   `json:"subpath,omitempty"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

func validProjectionPath(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.ContainsAny(p, "\\\x00")
}

// GitSourceStore validates a committed remote and derives its stable store key.
func GitSourceStore(raw string) (string, error) {
	parsed := raw
	if !strings.Contains(raw, "://") {
		at, colon := strings.Index(raw, "@"), strings.Index(raw, ":")
		if at < 0 || colon < at {
			return "", fmt.Errorf("git source requires an HTTPS or SSH remote URL")
		}
		parsed = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
	}
	u, err := url.Parse(parsed)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "ssh") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid git remote URL")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword || u.Scheme != "ssh" {
			return "", fmt.Errorf("do not commit credentials in git remote URLs; use a credential helper or SSH")
		}
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if len(strings.Split(repo, "/")) < 2 || !validProjectionPath(repo) || strings.Contains(repo, "..") {
		return "", fmt.Errorf("git remote must have an organization/repository path")
	}
	if path.Base(repo) == ReservedStoreSegment {
		return "", fmt.Errorf("git repository name %q is reserved", ReservedStoreSegment)
	}
	// Remotes on different ports are different servers, so the port is part
	// of the key; "_" cannot appear in a hostname, so no host can collide.
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		host += "_" + port
	}
	return "git/" + host + "/" + repo, nil
}

// ReservedStoreSegment is the docstore route suffix that lists a store's
// snapshots, so no store may end in it.
const ReservedStoreSegment = "snapshots"

// MatchSourceGlob supports ** as zero or more complete path segments.
func MatchSourceGlob(pattern, name string) bool {
	var match func([]string, []string) bool
	match = func(p, n []string) bool {
		if len(p) == 0 {
			return len(n) == 0
		}
		if p[0] == "**" {
			return match(p[1:], n) || (len(n) > 0 && match(p, n[1:]))
		}
		if len(n) == 0 {
			return false
		}
		ok, _ := path.Match(p[0], n[0])
		return ok && match(p[1:], n[1:])
	}
	return match(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func SourcePathSelected(include, exclude []string, p string) bool {
	selected := len(include) == 0
	for _, g := range include {
		if MatchSourceGlob(g, p) {
			selected = true
		}
	}
	for _, g := range exclude {
		if MatchSourceGlob(g, p) {
			return false
		}
	}
	return selected
}

// HomeSourcesFile is the path of an identity's committed source definitions
// inside the knowledge repository rooted at root.
func HomeSourcesFile(root, identity string) string {
	return filepath.Join(root, "home", identity, "sources.yaml")
}

// validateHomeSources parses every home/<identity>/sources.yaml beneath root so
// a broken committed definition fails at startup, naming the file. The server
// never fetches sources; it only checks that the files parse.
func validateHomeSources(root string) error {
	files, err := filepath.Glob(filepath.Join(root, "home", "*", "sources.yaml"))
	if err != nil {
		return err
	}
	for _, file := range files {
		if _, err := LoadSources(file); err != nil {
			return fmt.Errorf("loading sources %s: %w", file, err)
		}
	}
	return nil
}

func LoadSources(file string) (SourcesFile, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return SourcesFile{}, err
	}
	return ParseSources(b)
}

func ParseSources(b []byte) (SourcesFile, error) {
	var out SourcesFile
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return out, fmt.Errorf("sources.yaml must contain one document")
	}
	if out.Version != "1" {
		return out, fmt.Errorf("sources.yaml: version must be \"1\"")
	}
	seen := map[string]bool{}
	for _, s := range out.Sources {
		if s.Name == "" || s.Name == "." || s.Name == ".." || s.Name == ReservedStoreSegment || strings.ContainsAny(s.Name, "/\\\x00") || seen[s.Name] {
			return out, fmt.Errorf("invalid or duplicate source name %q", s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case "folder":
			if s.Path == "" || s.URL != "" || s.Group != "" || s.Project != "" || s.Branch != "" {
				return out, fmt.Errorf("source %s: folder requires only path", s.Name)
			}
		case "git":
			if s.URL == "" || s.Path != "" || s.Group != "" || s.Project != "" {
				return out, fmt.Errorf("source %s: git requires url", s.Name)
			}
			if _, err := GitSourceStore(s.URL); err != nil {
				return out, fmt.Errorf("source %s: %w", s.Name, err)
			}
			if strings.HasPrefix(s.Branch, "-") {
				return out, fmt.Errorf("source %s: invalid branch", s.Name)
			}
		case "claude":
			if (s.Group != "instructions" && s.Group != "memory" && s.Group != "transcripts" && s.Group != "personal") || (s.Group != "personal" && s.Project == "") || s.Path != "" || s.URL != "" || s.Branch != "" {
				return out, fmt.Errorf("source %s: invalid Claude group/project", s.Name)
			}
		default:
			return out, fmt.Errorf("source %s: unknown type %q", s.Name, s.Type)
		}
		for _, pattern := range append(append([]string{}, s.Include...), s.Exclude...) {
			if _, err := path.Match(pattern, ""); err != nil || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "..") {
				return out, fmt.Errorf("source %s: invalid glob %q", s.Name, pattern)
			}
		}
	}
	return out, nil
}
