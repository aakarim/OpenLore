// Package agentskills validates the agentskills.io SKILL.md format.
package agentskills

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/okf"
	"gopkg.in/yaml.v3"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var repoComponentRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func ValidateName(name string) error {
	if len(name) < 1 || len(name) > 64 || !nameRE.MatchString(name) {
		return fmt.Errorf("name must be 1-64 characters matching [a-z0-9]+(?:-[a-z0-9]+)*")
	}
	return nil
}

// CanonicalRepoURL converts a legacy owner/repo value or validates an HTTPS
// repository URL. Repository URLs intentionally identify the repository root,
// not a web UI tree path.
func CanonicalRepoURL(repo string) (string, error) {
	if parts := strings.Split(repo, "/"); len(parts) == 2 && validRepoComponent(parts[0]) {
		parts[1] = strings.TrimSuffix(parts[1], ".git")
		if validRepoComponent(parts[1]) {
			return "https://github.com/" + strings.Join(parts, "/"), nil
		}
	}
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("repository must be an HTTPS owner/repo URL")
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("repository URL must identify owner/repo")
	}
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return "", fmt.Errorf("repository URL must identify owner/repo")
		}
		parts[i] = decoded
	}
	parts[1] = strings.TrimSuffix(parts[1], ".git")
	if !validRepoComponent(parts[0]) || !validRepoComponent(parts[1]) {
		return "", fmt.Errorf("repository URL must identify owner/repo")
	}
	hostName := strings.ToLower(u.Hostname())
	if hostName == "" {
		return "", fmt.Errorf("repository URL must have a hostname")
	}
	host := hostName
	if strings.Contains(hostName, ":") {
		host = "[" + hostName + "]"
	}
	if port := u.Port(); port != "" && port != "443" {
		host = net.JoinHostPort(hostName, port)
	}
	u.Host = host
	u.Path = "/" + strings.Join(parts, "/")
	u.RawPath = ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

func validRepoComponent(component string) bool {
	return component != "" && component != "." && component != ".." && repoComponentRE.MatchString(component)
}

// ValidateRemotePath accepts only canonical relative repository paths.
func ValidateRemotePath(remotePath string) error {
	if remotePath != "" && (path.IsAbs(remotePath) || remotePath == ".." || strings.HasPrefix(remotePath, "../") || strings.Contains(remotePath, "\\") || path.Clean(remotePath) != remotePath) {
		return fmt.Errorf("remote path must be a clean relative path")
	}
	return nil
}

// Normalize converts imported skills to OpenLore's strict agentskills.io
// representation. dirName fixes the identity during sync; when empty, the
// destination name is derived from the upstream name.
func Normalize(dirName string, content []byte) (string, []byte, error) {
	parts, err := splitDocumentParts(content)
	if err != nil {
		return "", nil, err
	}
	var fm map[string]any
	if err := yaml.Unmarshal(parts.frontmatter, &fm); err != nil || fm == nil {
		return "", nil, fmt.Errorf("SKILL.md requires YAML frontmatter mapping")
	}
	original, ok := fm["name"].(string)
	if !ok || strings.TrimSpace(original) == "" {
		return "", nil, fmt.Errorf("name must be a nonblank string")
	}
	name := dirName
	if name == "" {
		name = slugName(original)
	}
	if err := ValidateName(name); err != nil {
		return "", nil, err
	}
	fm["name"] = name

	metadata := map[string]any{}
	if existing, ok := fm["metadata"].(map[string]any); ok {
		for key, value := range existing {
			metadata[key] = stringifyMetadata(value)
		}
	}
	allowed := map[string]bool{"name": true, "description": true, "license": true, "compatibility": true, "metadata": true, "allowed-tools": true, "disable-model-invocation": true, "remote": true}
	for key, value := range fm {
		if allowed[key] {
			continue
		}
		metadata[key] = stringifyMetadata(value)
		delete(fm, key)
	}
	if original != name {
		metadata["display-name"] = original
	}
	if len(metadata) == 0 {
		delete(fm, "metadata")
	} else {
		fm["metadata"] = metadata
	}
	encoded, err := yaml.Marshal(fm)
	if err != nil {
		return "", nil, err
	}
	encoded = bytes.TrimSuffix(encoded, []byte("\n"))
	if bytes.Equal(parts.opening, []byte("---\r\n")) {
		encoded = bytes.ReplaceAll(encoded, []byte("\n"), []byte("\r\n"))
	}
	out := append([]byte(nil), parts.opening...)
	out = append(out, encoded...)
	out = append(out, parts.closing...)
	out = append(out, parts.body...)
	return name, out, nil
}

func slugName(name string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r <= unicode.MaxASCII && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			if separator && b.Len() > 0 && b.Len() < 63 {
				b.WriteByte('-')
			}
			if b.Len() == 64 {
				break
			}
			b.WriteRune(r)
			separator = false
		} else if b.Len() > 0 {
			separator = true
		}
	}
	return strings.Trim(strings.TrimSpace(b.String()), "-")
}

func stringifyMetadata(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	b, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return strings.TrimSpace(string(b))
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
	if !repoOK {
		return fmt.Errorf("remote.repo must be an HTTPS repository URL")
	}
	if _, err := CanonicalRepoURL(repo); err != nil {
		return fmt.Errorf("remote.repo: %w", err)
	}
	if !refOK || ref == "" || strings.ContainsAny(ref, "\x00\r\n") {
		return fmt.Errorf("remote.ref must be a nonempty string")
	}
	if p, ok := m["path"].(string); ok {
		if err := ValidateRemotePath(p); err != nil {
			return fmt.Errorf("remote.path must be a clean relative path")
		}
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
