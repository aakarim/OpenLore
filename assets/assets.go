package assets

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed all:lore
var loreFS embed.FS

//go:embed all:config
var configFS embed.FS

//go:embed all:skills
var skillsFS embed.FS

//go:embed all:web
var webFS embed.FS

//go:embed all:legal
var legalFS embed.FS

//go:embed config/motd.txt
var defaultMOTD string

//go:embed config/VERSION
var versionString string

//go:embed oiya-icon.svg
var oiyaIcon []byte

// Lore returns the embedded docs filesystem (rooted inside lore/).
// Returns nil if the lore directory contains only the placeholder.
func Lore() fs.FS {
	sub, _ := fs.Sub(loreFS, "lore")
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Name() != "PUT_YOUR_DOCS_HERE" {
			return sub
		}
	}
	return nil
}

// Config returns the embedded config filesystem (rooted inside config/).
func Config() fs.FS {
	sub, _ := fs.Sub(configFS, "config")
	return sub
}

// DefaultMOTD returns the embedded MOTD string.
func DefaultMOTD() string {
	return defaultMOTD
}

// Version returns the embedded version string.
func Version() string {
	return strings.TrimSpace(versionString)
}

// OiyaIcon returns the Oiya brand icon as SVG.
func OiyaIcon() []byte {
	return oiyaIcon
}

// Skills returns the embedded skills filesystem (rooted inside skills/).
func Skills() fs.FS {
	sub, _ := fs.Sub(skillsFS, "skills")
	return sub
}

// Web returns the embedded web assets filesystem (rooted inside web/).
func Web() fs.FS {
	sub, _ := fs.Sub(webFS, "web")
	return sub
}

// Legal returns the embedded third-party legal notices filesystem (rooted
// inside legal/). It contains THIRD_PARTY_NOTICES.md and a licenses/ directory
// with the full license text of every bundled dependency.
func Legal() fs.FS {
	sub, _ := fs.Sub(legalFS, "legal")
	return sub
}

// HasEmbeddedConfig reports whether a real openlore.yml is embedded
// (not just the example file).
func HasEmbeddedConfig() bool {
	_, err := fs.Stat(configFS, "config/openlore.yml")
	return err == nil
}

// EmbeddedConfig returns the contents of the embedded openlore.yml, if any.
func EmbeddedConfig() ([]byte, bool) {
	data, err := fs.ReadFile(configFS, "config/openlore.yml")
	if err != nil {
		return nil, false
	}
	return data, true
}
