package rules

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// FileConfig is the strictly decoded contents of a folder's
// .lore/config.yaml file.
type FileConfig struct {
	Version    int                 `yaml:"version"`
	Rules      map[string]RuleSpec `yaml:"rules,omitempty"`
	Packages   map[string]any      `yaml:"packages,omitempty"`
	Hooks      map[string]any      `yaml:"hooks,omitempty"`
	Operations map[string]any      `yaml:"operations,omitempty"`
}

// DecodeFile decodes a folder rule configuration. Reserved sections are
// accepted while empty so configurations remain forward-compatible.
func DecodeFile(content []byte) (FileConfig, error) {
	versionPresent, err := inspectFileConfig(content)
	if err != nil {
		return FileConfig{}, err
	}
	var config FileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return FileConfig{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return FileConfig{}, fmt.Errorf("multiple YAML documents are not supported")
		}
		return FileConfig{}, err
	}
	if !versionPresent {
		return FileConfig{}, fmt.Errorf("version: required (expected 1)")
	}
	if config.Version != 1 {
		return FileConfig{}, fmt.Errorf("version: expected 1, got %d", config.Version)
	}
	for _, reserved := range []struct {
		name    string
		section map[string]any
	}{{"packages", config.Packages}, {"hooks", config.Hooks}, {"operations", config.Operations}} {
		if len(reserved.section) != 0 {
			return FileConfig{}, fmt.Errorf("%s: not supported yet", reserved.name)
		}
	}
	return config, nil
}

func inspectFileConfig(content []byte) (bool, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return false, err
	}
	if len(document.Content) == 0 || len(document.Content[0].Content) == 0 {
		return false, nil
	}
	root := document.Content[0]
	versionPresent := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		switch key {
		case "version":
			versionPresent = true
		case "rules":
			if err := inspectRuleSpecs(value); err != nil {
				return versionPresent, err
			}
		}
	}
	return versionPresent, nil
}

func inspectRuleSpecs(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	valid := []string{"match", "exclude", "use", "with", "enforce", "default"}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, spec := node.Content[i].Value, node.Content[i+1]
		if spec.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(spec.Content); j += 2 {
			key := spec.Content[j].Value
			known := false
			for _, candidate := range valid {
				known = known || key == candidate
			}
			if known {
				continue
			}
			suffix := ""
			if suggestion := nearest(key, valid); suggestion != "" {
				suffix = fmt.Sprintf(" (did you mean %q?)", suggestion)
			}
			return fmt.Errorf("rules.%s: unknown key %q%s", name, key, suffix)
		}
	}
	return nil
}
