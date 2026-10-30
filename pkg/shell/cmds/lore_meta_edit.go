package cmds

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/aakarim/go-openlore/pkg/okf"
	"gopkg.in/yaml.v3"
)

// cmdLoreMetaEdit handles args beginning with set or unset, after `lore meta`.
// Keys name literal top-level YAML fields; set values are YAML values.
// The lore meta adapter must dispatch these verbs here before scan parsing.
func cmdLoreMetaEdit(ctx CmdContext, args []string, w, errW io.Writer, stdin io.Reader) int {
	if len(args) != 3 || (args[0] != "set" && args[0] != "unset") {
		fmt.Fprintln(errW, "usage: lore meta set <path> key=value | lore meta unset <path> key")
		return 1
	}
	verb, p, key := args[0], args[1], args[2]
	var value yaml.Node
	if verb == "set" {
		var raw string
		var found bool
		key, raw, found = strings.Cut(key, "=")
		if !found {
			fmt.Fprintln(errW, "lore meta set: expected key=value")
			return 1
		}
		if raw == "" {
			value = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""}
		} else {
			doc, err := metaEditYAML([]byte(raw))
			if err != nil {
				fmt.Fprintf(errW, "lore meta set: invalid YAML value: %s\n", err)
				return 1
			}
			value = *doc.Content[0]
		}
	}
	if strings.TrimSpace(key) == "" {
		fmt.Fprintln(errW, "lore meta: key must not be empty")
		return 1
	}
	base, err := ctx.FS().ReadFile(ctx.Resolve(p))
	if err != nil {
		fmt.Fprintf(errW, "lore meta: %s: %s\n", p, err)
		return 1
	}
	fm, body, hasFM := okf.SplitFrontmatter(base)
	if !hasFM {
		if bytes.HasPrefix(base, []byte("---\n")) || bytes.HasPrefix(base, []byte("---\r\n")) || bytes.Equal(base, []byte("---")) {
			fmt.Fprintln(errW, "lore meta: frontmatter has no closing delimiter")
			return 1
		}
		body = base
	}
	doc, err := metaEditYAML(fm)
	if err != nil {
		fmt.Fprintf(errW, "lore meta: invalid frontmatter: %s\n", err)
		return 1
	}
	mapping := doc.Content[0]
	if mapping.Kind == yaml.ScalarNode && mapping.Tag == "!!null" {
		mapping.Kind, mapping.Tag, mapping.Value = yaml.MappingNode, "!!map", ""
	}
	if mapping.Kind != yaml.MappingNode {
		fmt.Fprintln(errW, "lore meta: frontmatter must be a YAML mapping")
		return 1
	}
	index := -1
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			index = i
			break
		}
	}
	if verb == "unset" {
		if index < 0 {
			return 0 // Already absent: do not reserialize or trigger a write.
		}
		mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
	} else if index >= 0 {
		if value.Anchor == "" {
			value.Anchor = mapping.Content[index+1].Anchor
		}
		mapping.Content[index+1] = &value
	} else {
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &value)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		fmt.Fprintf(errW, "lore meta: %s\n", err)
		return 1
	}
	if err := enc.Close(); err != nil {
		fmt.Fprintf(errW, "lore meta: %s\n", err)
		return 1
	}
	// Removing an anchor still referenced elsewhere must not leave invalid YAML.
	if _, err := metaEditYAML(buf.Bytes()[len("---\n"):]); err != nil {
		fmt.Fprintf(errW, "lore meta: edit would produce invalid frontmatter: %s\n", err)
		return 1
	}
	buf.WriteString("---\n")
	buf.Write(body)
	return WriteFileCASMsg(ctx, errW, "lore meta "+verb, p, buf.Bytes(), base)
}

// metaEditYAML accepts exactly one YAML document and validates duplicate keys
// and aliases before any mutation is attempted.
func metaEditYAML(raw []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	err := decoder.Decode(&doc)
	if err == io.EOF {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
	}
	if err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected a single YAML document")
	}
	var validated any
	if err := doc.Decode(&validated); err != nil {
		return nil, err
	}
	return &doc, nil
}
