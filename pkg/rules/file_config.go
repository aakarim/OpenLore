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
