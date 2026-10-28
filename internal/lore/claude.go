package lore

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"gopkg.in/yaml.v3"
)

//go:embed claude.yaml
var claudeProfileYAML []byte

type agentGroup struct {
	Name   string   `yaml:"name"`
	Label  string   `yaml:"label"`
	Scope  string   `yaml:"scope"`
	OptIn  bool     `yaml:"opt_in"`
	Base   string   `yaml:"base"`
	Store  string   `yaml:"store"`
	Detect []string `yaml:"detect"`
}
type agentProfile struct {
	Name          string       `yaml:"name"`
	ProjectKey    string       `yaml:"project_key"`
	Groups        []agentGroup `yaml:"groups"`
	AlwaysExclude []string     `yaml:"always_exclude"`
}

func claudeProfile() agentProfile {
	var p agentProfile
	if err := yaml.Unmarshal(claudeProfileYAML, &p); err != nil {
		panic(err)
	}
	return p
}

func projectKey(project, rule string) (string, error) {
	if rule != "non_alphanumeric_to_dash" {
		return "", fmt.Errorf("unsupported project key rule %q", rule)
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, project), nil
}

// ClaudeProjectKey is the directory name Claude Code uses for a project.
func ClaudeProjectKey(project string) (string, error) {
	return projectKey(project, claudeProfile().ProjectKey)
}

// claudeLocation resolves a Claude source's group, the directory its files
// live in and the store it syncs to under identity.
func (r Repository) claudeLocation(s config.SourceSpec, identity string) (agentGroup, string, string, error) {
	profile := claudeProfile()
	var group agentGroup
	for _, g := range profile.Groups {
		if g.Name == s.Group {
			group = g
		}
	}
	if group.Name == "" {
		return group, "", "", fmt.Errorf("unknown Claude group %s", s.Group)
	}
	project := ""
	key := "personal"
	if group.Scope == "project" {
		var err error
		project, err = expand(s.Project, r.Root)
		if err != nil {
			return group, "", "", err
		}
		project, err = filepath.EvalSymlinks(project)
		if err != nil {
			return group, "", "", err
		}
		key, err = projectKey(project, profile.ProjectKey)
		if err != nil {
			return group, "", "", err
		}
	}
	if identity == "" {
		identity = "<identity>"
	}
	replace := strings.NewReplacer("{project}", project, "{project_key}", key, "{identity}", identity)
	base, err := expand(replace.Replace(group.Base), r.Root)
	return group, base, replace.Replace(group.Store), err
}

// FileCount renders "1 file" or "N files" for prompts and listings.
func FileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// ClaudeOffer is one profile group a person may choose to sync.
type ClaudeOffer struct {
	Spec        config.SourceSpec
	Label       string
	Preselected bool
}

// ClaudeOffers builds the groups whose scope is in scopes (all groups when
// scopes is empty) for the Claude project, which defaults to the working
// directory. Groups offered under a scope flag are preselected unless the
// profile marks them opt_in. identity names the stores the groups would sync
// to.
func (r Repository) ClaudeOffers(identity, project, name string, scopes, include, exclude []string) ([]ClaudeOffer, error) {
	// The project is where Claude Code runs, usually outside the knowledge
	// repository, so a relative path is taken from the working directory.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if project == "" {
		project = cwd
	}
	project, err = expand(project, cwd)
	if err != nil {
		return nil, err
	}
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	profile := claudeProfile()
	if name == "" {
		name = filepath.Base(project) + "-claude"
	}
	offers := []ClaudeOffer{}
	for _, g := range profile.Groups {
		if len(scopes) > 0 && !slices.Contains(scopes, g.Scope) {
			continue
		}
		s := config.SourceSpec{Name: name + "-" + g.Name, Type: "claude", Group: g.Name, Include: include, Exclude: exclude}
		if g.Scope == "project" {
			s.Project = portablePath(project, r.Root)
		}
		_, base, store, err := r.claudeLocation(s, identity)
		if err != nil {
			return nil, err
		}
		files, _, err := r.ScanSource(s, store, false)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		offers = append(offers, ClaudeOffer{
			Spec:        s,
			Label:       fmt.Sprintf("%s · %s · %s", g.Label, portablePath(base, r.Root), FileCount(len(files))),
			Preselected: len(scopes) > 0 && !g.OptIn,
		})
	}
	return offers, nil
}

// ClaudeSelection returns the specs of the chosen offers, rejecting indexes
// that are out of range or repeated.
func ClaudeSelection(offers []ClaudeOffer, chosen []int) ([]config.SourceSpec, error) {
	seen := map[int]bool{}
	specs := make([]config.SourceSpec, 0, len(chosen))
	for _, i := range chosen {
		if i < 0 || i >= len(offers) || seen[i] {
			return nil, fmt.Errorf("invalid group selection %d", i)
		}
		seen[i] = true
		specs = append(specs, offers[i].Spec)
	}
	return specs, nil
}

// SourcePreview is what a source would sync: its store and sorted paths.
type SourcePreview struct {
	Store string
	Files []string
}

// Preview scans a source as identity would sync it.
func (r Repository) Preview(s config.SourceSpec, identity string) (SourcePreview, error) {
	store, err := r.StorePath(s, identity)
	if err != nil {
		return SourcePreview{}, err
	}
	files, _, err := r.ScanSource(s, store, false)
	if err != nil {
		return SourcePreview{}, err
	}
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	slices.Sort(names)
	return SourcePreview{Store: store, Files: names}, nil
}
