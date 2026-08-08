package okf

import (
	"path"
	"strconv"
	"strings"
)

// Version identifies an OKF spec revision this package knows how to lint.
type Version string

const (
	// V01 is OKF v0.1: the original spec with a legacy scalar `timestamp`
	// field and body `# Citations` convention.
	V01 Version = "0.1"
	// V02 is OKF v0.2: adds the optional provenance (`sources`,
	// `usage_window`), trust (`generated`, `verified`), lifecycle (`status`,
	// `stale_after`), and Attested Computation frontmatter families.
	V02 Version = "0.2"
	// Latest is the newest spec revision this package targets. Versionless
	// and unrecognized bundles are linted against it, per §12: consumers
	// attempt best-effort consumption rather than rejecting.
	Latest = V02
)

// DeclaredVersion returns the raw okf_version declaration from the
// bundle-root index.md frontmatter — the one place the spec permits it
// (§12). ok is false when the bundle has no root index, no frontmatter, or no
// usable okf_version value.
func DeclaredVersion(files []File) (string, bool) {
	for _, file := range files {
		name := strings.TrimPrefix(path.Clean("/"+file.Path), "/")
		if name != IndexFile {
			continue
		}
		meta, _, ok, err := ParseFrontmatter(file.Content)
		if !ok || err != nil {
			return "", false
		}
		return versionString(meta["okf_version"])
	}
	return "", false
}

// ResolveVersion maps a declaration to a supported Version. Unknown
// declarations resolve to Latest with known=false so callers can surface a
// diagnostic without rejecting the bundle.
func ResolveVersion(declared string) (v Version, known bool) {
	switch Version(strings.TrimSpace(declared)) {
	case V01:
		return V01, true
	case V02:
		return V02, true
	}
	return Latest, false
}

// DetectVersion resolves a bundle's effective spec version: the declared
// version when present and recognized, Latest otherwise.
func DetectVersion(files []File) Version {
	declared, ok := DeclaredVersion(files)
	if !ok {
		return Latest
	}
	v, _ := ResolveVersion(declared)
	return v
}

// versionString normalizes an okf_version frontmatter value. The spec shows a
// quoted string ("0.2"), but an unquoted declaration decodes as a YAML float,
// so both are accepted.
func versionString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		return s, s != ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	}
	return "", false
}
