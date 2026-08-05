// Package agentskills validates the agentskills.io SKILL.md format.
package agentskills

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/okf"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func ValidateName(name string) error {
	if len(name) < 1 || len(name) > 64 || !nameRE.MatchString(name) {
		return fmt.Errorf("name must be 1-64 characters matching [a-z0-9]+(?:-[a-z0-9]+)*")
	}
	return nil
}

// ExtractName parses and validates the name independently of disabled-skill
// handling. Remote import uses it before deriving a destination path.
func ExtractName(content []byte) (string, error) {
	fm, _, ok, err := okf.ParseFrontmatter(content)
	if err != nil {
		return "", err
	}
	if !ok || fm == nil {
		return "", fmt.Errorf("SKILL.md requires YAML frontmatter mapping")
	}
	name, ok := fm["name"].(string)
	if !ok {
		return "", fmt.Errorf("name must be a string")
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return name, nil
}

// Result describes a parseable skill. Disabled skills bypass ordinary skill
// fields, but reserved OpenLore fields are always validated.
type Result struct{ Disabled bool }

// Validate validates content for the skill directory dirName.
func Validate(dirName string, content []byte) (Result, error) {
	fm, _, ok, err := okf.ParseFrontmatter(content)
	if err != nil {
		return Result{}, err
	}
	if !ok || fm == nil {
		return Result{}, fmt.Errorf("SKILL.md requires YAML frontmatter mapping")
	}
	if _, exists := fm["remote-status"]; exists {
		return Result{}, fmt.Errorf("unknown frontmatter field %q", "remote-status")
	}
	if v, exists := fm["remote"]; exists {
		if err := validateRemote(v); err != nil {
			return Result{}, err
		}
	}
	if raw, ok := fm["metadata"]; ok {
		if m, ok := raw.(map[string]any); ok && m["agent_skill"] == "disable" {
			return Result{Disabled: true}, nil
		}
	}
	allowed := map[string]bool{"name": true, "description": true, "license": true, "compatibility": true, "metadata": true, "allowed-tools": true, "disable-model-invocation": true, "remote": true}
	for k := range fm {
		if !allowed[k] {
			return Result{}, fmt.Errorf("unknown frontmatter field %q", k)
		}
	}
	name, err := ExtractName(content)
	if err != nil {
		return Result{}, err
	}
	if name != path.Base(dirName) {
		return Result{}, fmt.Errorf("name %q must match parent directory %q", name, path.Base(dirName))
	}
	desc, ok := fm["description"].(string)
	if !ok || strings.TrimSpace(desc) == "" || utf8.RuneCountInString(desc) > 1024 {
		return Result{}, fmt.Errorf("description must be a nonblank string of at most 1024 characters")
	}
	for _, k := range []string{"license", "allowed-tools"} {
		if v, exists := fm[k]; exists {
			if _, ok := v.(string); !ok {
				return Result{}, fmt.Errorf("%s must be a string", k)
			}
		}
	}
	if v, exists := fm["disable-model-invocation"]; exists {
		if _, ok := v.(bool); !ok {
			return Result{}, fmt.Errorf("disable-model-invocation must be a boolean")
		}
	}
	if v, exists := fm["compatibility"]; exists {
		s, ok := v.(string)
		if !ok || s == "" || utf8.RuneCountInString(s) > 500 {
			return Result{}, fmt.Errorf("compatibility must be a nonempty string of at most 500 characters")
		}
	}
	if v, exists := fm["metadata"]; exists {
		m, ok := v.(map[string]any)
		if !ok {
			return Result{}, fmt.Errorf("metadata must be a string-to-string mapping")
		}
		for k, x := range m {
			if _, ok := x.(string); !ok {
				return Result{}, fmt.Errorf("metadata.%s must be a string", k)
			}
		}
	}
	return Result{}, nil
}

func validateRemote(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("remote must be a mapping")
	}
	for k, value := range m {
		if k != "repo" && k != "path" && k != "ref" && k != "commit" && k != "kind" {
			return fmt.Errorf("unknown remote field %q", k)
		}
		if _, ok := value.(string); !ok {
			return fmt.Errorf("remote.%s must be a string", k)
		}
	}
	repo, repoOK := m["repo"].(string)
	ref, refOK := m["ref"].(string)
	if !repoOK || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo) {
		return fmt.Errorf("remote.repo must be owner/repo")
	}
	if !refOK || ref == "" || strings.ContainsAny(ref, "\x00\r\n") {
		return fmt.Errorf("remote.ref must be a nonempty string")
	}
	if p, ok := m["path"].(string); ok && (path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "\\") || path.Clean(p) != p) {
		return fmt.Errorf("remote.path must be a clean relative path")
	}
	if commit, ok := m["commit"].(string); ok && !regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(commit) {
		return fmt.Errorf("remote.commit must be a 40-hex commit SHA")
	}
	kind, hasKind := m["kind"].(string)
	if hasKind && kind != "tracking" && kind != "pinned" {
		return fmt.Errorf("remote.kind must be tracking or pinned")
	}
	if _, hasCommit := m["commit"]; hasCommit && !hasKind {
		return fmt.Errorf("remote.kind is required when remote.commit is set")
	}
	return nil
}
