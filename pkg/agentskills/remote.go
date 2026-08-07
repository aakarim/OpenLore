package agentskills

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

type Remote struct {
	Repo   string `yaml:"repo" json:"repo"`
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Ref    string `yaml:"ref" json:"ref"`
	Commit string `yaml:"commit,omitempty" json:"commit,omitempty"`
	Kind   string `yaml:"kind,omitempty" json:"kind,omitempty"`
}

type documentParts struct{ opening, frontmatter, closing, body []byte }

func splitDocumentParts(content []byte) (documentParts, error) {
	opening := []byte("---\n")
	closingPrefix := []byte("\n---")
	lineEnding := []byte("\n")
	if bytes.HasPrefix(content, []byte("---\r\n")) {
		opening = []byte("---\r\n")
		closingPrefix = []byte("\r\n---")
		lineEnding = []byte("\r\n")
	} else if !bytes.HasPrefix(content, opening) {
		return documentParts{}, fmt.Errorf("SKILL.md requires YAML frontmatter mapping")
	}
	rel := bytes.Index(content[len(opening):], closingPrefix)
	if rel < 0 {
		return documentParts{}, fmt.Errorf("unterminated YAML frontmatter")
	}
	fmEnd := len(opening) + rel
	closingEnd := fmEnd + len(closingPrefix)
	if bytes.HasPrefix(content[closingEnd:], lineEnding) {
		closingEnd += len(lineEnding)
	}
	return documentParts{
		opening: content[:len(opening)], frontmatter: content[len(opening):fmEnd],
		closing: content[fmEnd:closingEnd], body: content[closingEnd:],
	}, nil
}

func splitDocument(content []byte) ([]byte, []byte, error) {
	parts, err := splitDocumentParts(content)
	return parts.frontmatter, parts.body, err
}

func remoteEdit(content []byte, editRemote bool, remote *Remote, editStatus bool, status *string) ([]byte, error) {
	parts, err := splitDocumentParts(content)
	if err != nil {
		return nil, err
	}
	fm := append([]byte(nil), parts.frontmatter...)
	var doc yaml.Node
	if err := yaml.Unmarshal(fm, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("SKILL.md requires YAML frontmatter mapping")
	}
	if doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("remote edits require block-style YAML frontmatter")
	}
	type edit struct {
		start, end  int
		replacement []byte
	}
	lines := bytes.SplitAfter(fm, []byte("\n"))
	offsets := make([]int, len(lines)+1)
	for i := range lines {
		offsets[i+1] = offsets[i] + len(lines[i])
	}
	mapping := doc.Content[0]
	var edits []edit
	seenRemote := false
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Value != "remote" && key.Value != "remote-status" {
			continue
		}
		if key.Value == "remote" && !editRemote || key.Value == "remote-status" && !editStatus {
			continue
		}
		start := offsets[key.Line-1]
		end := len(fm)
		if i+2 < len(mapping.Content) {
			nextLine := mapping.Content[i+2].Line - 1
			endLine := nextLine
			for endLine > key.Line {
				trimmed := bytes.TrimSpace(lines[endLine-1])
				if len(trimmed) != 0 && !bytes.HasPrefix(trimmed, []byte("#")) {
					break
				}
				endLine--
			}
			end = offsets[endLine]
		}
		var replacement []byte
		if key.Value == "remote" && remote != nil {
			replacement, err = yaml.Marshal(struct {
				Remote Remote `yaml:"remote"`
			}{*remote})
			seenRemote = true
		} else if key.Value == "remote-status" && status != nil {
			replacement, err = yaml.Marshal(struct {
				Status string `yaml:"remote-status"`
			}{*status})
			status = nil
		}
		if err != nil {
			return nil, err
		}
		edits = append(edits, edit{start, end, replacement})
	}
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		fm = append(append(append([]byte(nil), fm[:e.start]...), e.replacement...), fm[e.end:]...)
	}
	if editRemote && remote != nil && !seenRemote {
		b, _ := yaml.Marshal(struct {
			Remote Remote `yaml:"remote"`
		}{*remote})
		if len(fm) > 0 && fm[len(fm)-1] != '\n' {
			fm = append(fm, '\n')
		}
		fm = append(fm, b...)
	}
	if editStatus && status != nil {
		b, _ := yaml.Marshal(struct {
			Status string `yaml:"remote-status"`
		}{*status})
		if len(fm) > 0 && fm[len(fm)-1] != '\n' {
			fm = append(fm, '\n')
		}
		fm = append(fm, b...)
	}
	out := append([]byte(nil), parts.opening...)
	out = append(out, fm...)
	out = append(out, parts.closing...)
	out = append(out, parts.body...)
	return out, nil
}

func ReadRemote(content []byte) (Remote, bool, error) {
	fm, _, err := splitDocument(content)
	if err != nil {
		return Remote{}, false, err
	}
	var values map[string]any
	if err := yaml.Unmarshal(fm, &values); err != nil {
		return Remote{}, false, err
	}
	v, ok := values["remote"]
	if !ok {
		return Remote{}, false, nil
	}
	b, _ := yaml.Marshal(v)
	var r Remote
	if err := yaml.Unmarshal(b, &r); err != nil {
		return Remote{}, false, err
	}
	return r, true, nil
}

func GraftRemote(content []byte, remote Remote) ([]byte, error) {
	return remoteEdit(content, true, &remote, false, nil)
}
func StripRemote(content []byte) ([]byte, error) {
	return remoteEdit(content, true, nil, true, nil)
}
func InjectRemoteStatus(content []byte, status string) ([]byte, error) {
	return remoteEdit(content, false, nil, true, &status)
}

// SurgicalRemoteEdit accepts a remote-only edit and clears commit when its target changes.
func SurgicalRemoteEdit(stored, incoming []byte, dirName ...string) ([]byte, bool, error) {
	old, linked, err := ReadRemote(stored)
	if err != nil || !linked {
		return nil, false, err
	}
	cleanIncoming, err := remoteEdit(incoming, false, nil, true, nil)
	if err != nil {
		return nil, false, err
	}
	if len(dirName) > 0 {
		if _, err := Validate(dirName[0], cleanIncoming); err != nil {
			return nil, false, err
		}
	}
	next, nextLinked, err := ReadRemote(cleanIncoming)
	if err != nil {
		return nil, false, err
	}
	a, _ := StripRemote(stored)
	b, _ := StripRemote(cleanIncoming)
	if !bytes.Equal(a, b) {
		return nil, false, nil
	}
	if nextLinked {
		canonicalNext, err := CanonicalRepoURL(next.Repo)
		if err != nil {
			return nil, false, err
		}
		next.Repo = canonicalNext
		canonicalOld, err := CanonicalRepoURL(old.Repo)
		if err != nil {
			return nil, false, err
		}
		if canonicalOld != next.Repo || old.Path != next.Path || old.Ref != next.Ref {
			next.Commit = ""
			next.Kind = ""
		} else {
			next.Commit = old.Commit
			next.Kind = old.Kind
		}
		cleanIncoming, err = GraftRemote(b, next)
	}
	return cleanIncoming, true, err
}
