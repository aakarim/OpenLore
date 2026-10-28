package lore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// MCPRegistration returns the agent CLI arguments that register the OpenLore
// MCP server and the instructions file the agent reads.
func MCPRegistration(agent, mcpURL string) ([]string, string, error) {
	switch agent {
	case "claude":
		return []string{"mcp", "add", "--transport", "http", "openlore", mcpURL}, "CLAUDE.md", nil
	case "codex":
		return []string{"mcp", "add", "openlore", "--url", mcpURL}, "AGENTS.md", nil
	}
	return nil, "", fmt.Errorf("unsupported agent %q", agent)
}

// ErrUnfinishedSection means an instructions file opens an OpenLore section
// it never closes.
var ErrUnfinishedSection = errors.New("unfinished OpenLore section")

// MergeInstructionBlock replaces the OpenLore section of an instructions
// file with snippet, or appends one.
func MergeInstructionBlock(existing, snippet string) (string, error) {
	const begin, end = "<!-- openlore:begin -->", "<!-- openlore:end -->"
	block := begin + "\n" + strings.TrimSpace(snippet) + "\n" + end + "\n"
	if start := strings.Index(existing, begin); start >= 0 {
		finish := strings.Index(existing[start:], end)
		if finish < 0 {
			return "", ErrUnfinishedSection
		}
		return existing[:start] + block + strings.TrimPrefix(existing[start+finish+len(end):], "\n"), nil
	}
	if existing == "" {
		return block, nil
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + block, nil
}

// UpdateInstructions merges snippet into file, keeping its mode.
func UpdateInstructions(file, snippet string) error {
	before, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text, err := MergeInstructionBlock(string(before), snippet)
	if errors.Is(err, ErrUnfinishedSection) {
		return fmt.Errorf("%s has an unfinished OpenLore section; repair it before reconnecting", file)
	}
	if err != nil {
		return err
	}
	mode := fs.FileMode(0644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode().Perm()
	}
	return atomicFile(file, []byte(text), mode)
}
