// Command configdocs generates docs/openlore-yml.md, the reference for every
// key openlore.yml accepts. Key names and types come from the yaml struct tags
// in internal/config via config.YAMLKeys(); defaults, descriptions and
// examples are maintained in the entries map below. The build fails if the two
// drift: every key the loader reads must have an entry, and every entry must
// name a key the loader reads.
package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
)

// entry is the hand-maintained half of a reference entry.
type entry struct {
	// Default is the built-in value, rendered in backticks; empty means none.
	Default string
	// Description is one sentence, present tense. A second sentence is allowed
	// only for a consequence the reader cannot infer.
	Description string
	// Example is the YAML value shown in the snippet. Sections have none.
	Example string
}

var entries = map[string]entry{
	"version": {
		Default:     "",
		Description: "Config file format version, reserved for migrations between formats.",
		Example:     `"1"`,
	},
	"debug": {
		Default:     "false",
		Description: "Turns on verbose server logs, including unknown-command and syntax telemetry.",
		Example:     "true",
	},
	"experimental": {
		Default:     "",
		Description: "Names of experimental features to switch on; the `OPENLORE_EXPERIMENTAL` environment variable appends to this list.",
		Example:     `["analytics"]`,
	},
	"analytics": {
		Description: "Usage analytics: how reads and writes are logged, aggregated, indexed and exported.",
	},
	"analytics.enabled": {
		Default:     "true",
		Description: "Records agent reads and writes for the dashboard and the `analytics` command.",
		Example:     "false",
	},
	"analytics.dir": {
		Default:     "analytics",
		Description: "Directory for analytics logs and indexes; a relative path is resolved under `data_dir`.",
		Example:     "/var/lib/openlore/analytics",
	},
	"analytics.log": {
		Description: "Rotation and retention of the raw analytics log.",
	},
	"analytics.log.rotate": {
		Default:     "24h",
		Description: "Interval at which the active log segment is closed and a new one started.",
		Example:     "1h",
	},
	"analytics.log.compress": {
		Default:     "zstd",
		Description: "Compression applied to rotated log segments.",
		Example:     "zstd",
	},
	"analytics.log.retention": {
		Default:     "",
		Description: "How long rotated segments are kept before deletion; accepts a `d` suffix for days. Unset keeps them indefinitely.",
		Example:     "90d",
	},
	"analytics.ship": {
		Description: "Shipping of analytics segments to a remote store.",
	},
	"analytics.ship.interval": {
		Default:     "30s",
		Description: "How often the shipper looks for completed segments to send.",
		Example:     "1m",
	},
	"analytics.ship.remote": {
		Default:     "none",
		Description: "Destination for shipped segments; `none` is the only supported value.",
		Example:     "none",
	},
	"analytics.pipeline": {
		Description: "The in-process event pipeline that feeds the analytics log.",
	},
	"analytics.pipeline.enabled": {
		Default:     "false",
		Description: "Runs the event pipeline; leave unset to let `analytics.enabled` decide.",
		Example:     "true",
	},
	"analytics.pipeline.buffer": {
		Default:     "1024",
		Description: "Number of events the pipeline buffers before back-pressure applies.",
		Example:     "4096",
	},
	"analytics.shutdown_timeout": {
		Default:     "10s",
		Description: "How long the server waits for buffered analytics events to flush on shutdown.",
		Example:     "30s",
	},
	"analytics.aggregations": {
		Description: "Pre-computed aggregates read by the dashboard.",
	},
	"analytics.aggregations.refresh_interval": {
		Default:     "5m",
		Description: "How often aggregates are recomputed from the log.",
		Example:     "1m",
	},
	"analytics.aggregations.store": {
		Default:     "sqlite",
		Description: "Backend for aggregates: `sqlite` or `file`.",
		Example:     "file",
	},
	"analytics.index": {
		Description: "Indexing of analytics segments for queries.",
	},
	"analytics.index.workers": {
		Default:     "2",
		Description: "Number of concurrent indexing workers.",
		Example:     "4",
	},
	"analytics.history": {
		Description: "The commit journal that lets you inspect what agents wrote and when.",
	},
	"analytics.history.blobs": {
		Default:     "true",
		Description: "Stores the content of each committed write so `history` can show diffs.",
		Example:     "false",
	},
	"analytics.history.retention": {
		Default:     "",
		Description: "How long journal entries are kept; accepts a `d` suffix for days. Unset keeps them indefinitely.",
		Example:     "365d",
	},
	"analytics.export": {
		Description: "Export of analytics to monitoring systems.",
	},
	"analytics.export.prometheus": {
		Default:     "true",
		Description: "Serves analytics counters on the metrics port alongside server metrics.",
		Example:     "false",
	},
	"port": {
		Default:     "2222",
		Description: "TCP port for the SSH server that agents connect to.",
		Example:     "22",
	},
	"metrics_port": {
		Default:     "3000",
		Description: "TCP port for the Prometheus metrics endpoint; `0` disables it.",
		Example:     "0",
	},
	"host_key_path": {
		Default:     ".ssh/openlore_ed25519",
		Description: "Path to the SSH host key. Generated on first start if the file does not exist.",
		Example:     "/etc/openlore/host_key",
	},
	"motd": {
		Default:     "",
		Description: "Message shown to every client when it connects.",
		Example:     `"Welcome to Acme docs. Type 'tree -L 1 /' to start."`,
	},
	"motd_file": {
		Default:     "",
		Description: "Path to a file whose contents replace `motd`; read once when the config loads.",
		Example:     "./motd.txt",
	},
	"auth_file": {
		Default:     "",
		Description: "Path to the `lore.json` file that maps keys to identities, roles and docsets.",
		Example:     "./lore.json",
	},
	"skills_dir": {
		Default:     "",
		Description: "Directory of additional skills (`skills.json` plus Markdown files) loaded alongside the built-in ones.",
		Example:     "./skills",
	},
	"writable_dir": {
		Default:     "",
		Description: "Disk-backed content root overlaid on the embedded docs at the virtual root.",
		Example:     "./published",
	},
	"data_dir": {
		Default:     ".openlore",
		Description: "Root for server state such as signing keys, analytics and the write journal; distinct from content.",
		Example:     "/var/lib/openlore",
	},
	"http_port": {
		Default:     "8080",
		Description: "TCP port for the HTTP server that hosts the dashboard, MCP endpoint and JSON API; `0` disables it.",
		Example:     "80",
	},
	"external_ssh_port": {
		Default:     "",
		Description: "SSH port advertised to clients through the `X-SSH-Port` header when a load balancer remaps `port`.",
		Example:     "22",
	},
	"mcp": {
		Description: "The MCP-over-HTTP endpoint for clients such as Claude Code and Codex.",
	},
	"mcp.enabled": {
		Default:     "true",
		Description: "Serves the Streamable HTTP MCP endpoint on the HTTP server.",
		Example:     "false",
	},
	"mcp.path": {
		Default:     "/mcp",
		Description: "URL path the MCP endpoint is mounted at.",
		Example:     "/mcp",
	},
	"mcp.require_auth": {
		Default:     "",
		Description: "Forces OAuth for the MCP endpoint and JSON API even when `lore.json` allows keyless access; requires `tokens`. Unset inherits the SSH posture.",
		Example:     "true",
	},
	"api": {
		Description: "The plain JSON HTTP API backed by the same MCP server.",
	},
	"api.enabled": {
		Default:     "true",
		Description: "Serves `POST {path}/shell` and `GET {path}/commands` on the HTTP server.",
		Example:     "false",
	},
	"api.path": {
		Default:     "/api",
		Description: "URL path the JSON API is mounted at.",
		Example:     "/api",
	},
	"tls_cert": {
		Default:     "",
		Description: "PEM certificate for the HTTP server; SSH is unaffected. Set with `tls_key`.",
		Example:     "./cert.pem",
	},
	"tls_key": {
		Default:     "",
		Description: "PEM private key matching `tls_cert`.",
		Example:     "./key.pem",
	},
	"auth": {
		Description: "Transport-level authentication for the HTTP server.",
	},
	"auth.mtls": {
		Description: "Optional client-certificate corroboration for authenticated OAuth clients.",
	},
	"auth.mtls.ca_bundle": {
		Default:     "",
		Description: "PEM CA bundle a presented client certificate must chain to; clients without a certificate can still connect. OpenLore must terminate TLS itself.",
		Example:     "/etc/openlore/connectors-ca.pem",
	},
	"ca_keys_file": {
		Default:     "",
		Description: "File of CA public keys trusted to sign user SSH certificates, like OpenSSH's `TrustedUserCAKeys`.",
		Example:     "./ca_user_key.pub",
	},
	"host_cert_file": {
		Default:     "",
		Description: "CA-signed certificate for the key at `host_key_path`, presented to clients instead of a bare host key.",
		Example:     ".ssh/openlore_ed25519-cert.pub",
	},
	"default_cwd": {
		Default:     "/openlore",
		Description: "Directory a session starts in.",
		Example:     "/docs",
	},
	"files": {
		Description: "Which files in the content root are served.",
	},
	"files.allowed": {
		Default:     `["*.md", "*.markdown", "*.txt", "*.html", "*.htm", "*.css", "*.js", "*.json", "*.jsonl", "*.yaml", "*.yml", "*.csv", "*.tsv", "*.xml", "*.toml", "*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg", "*.webp"]`,
		Description: "Glob patterns a file name must match to be served; an empty list serves every file.",
		Example:     `["*.md", "*.txt"]`,
	},
	"files.denied": {
		Default:     "",
		Description: "Glob patterns that hide a file even when it matches `files.allowed`.",
		Example:     `["secret-*.md"]`,
	},
	"files.ignore": {
		Default:     `[".git/**", "node_modules/**", ".env*", "**/.DS_Store"]`,
		Description: "Glob patterns matched against every path segment; a match hides the file or directory from listings.",
		Example:     `[".git", "node_modules", "*.key"]`,
	},
	"passkeys": {
		Description: "Browser passkey (WebAuthn) login for the dashboard.",
	},
	"passkeys.enabled": {
		Default:     "true",
		Description: "Lets people register a passkey from SSH and sign in to the dashboard with it.",
		Example:     "false",
	},
	"passkeys.rp_id": {
		Default:     "localhost",
		Description: "WebAuthn relying-party ID, normally the public domain of the dashboard.",
		Example:     "docs.example.com",
	},
	"passkeys.rp_name": {
		Default:     "OpenLore",
		Description: "Display name the browser shows in its passkey prompt.",
		Example:     `"Acme Docs"`,
	},
	"passkeys.rp_origins": {
		Default:     `["http://localhost:8080"]`,
		Description: "Origins allowed to start WebAuthn ceremonies.",
		Example:     `["https://docs.example.com"]`,
	},
	"passkeys.lore_path": {
		Default:     "/lore",
		Description: "URL path prefix for browsing content in the dashboard.",
		Example:     "/lore",
	},
	"passkeys.passkeys_file": {
		Default:     "./config/passkeys.json",
		Description: "File that stores registered passkey credentials.",
		Example:     "/var/lib/openlore/passkeys.json",
	},
	"passkeys.session_ttl": {
		Default:     "24h",
		Description: "How long a browser session lasts after passkey sign-in.",
		Example:     "8h",
	},
	"shellexec": {
		Description: "External commands run around reads and writes by the built-in shellexec plugin. Each command receives the `OPENLORE_*` environment variables.",
	},
	"shellexec.pre_read": {
		Description: "Commands run before a path is read, debounced per path.",
	},
	"shellexec.pre_read[].cmd": {
		Default:     "",
		Description: "Command line run with `sh -c`.",
		Example:     "./scripts/pull.sh",
	},
	"shellexec.pre_read[].timeout": {
		Default:     "30s",
		Description: "Wall-clock limit for the command; a timeout counts as a failure.",
		Example:     "10s",
	},
	"shellexec.pre_read[].fail_on_error": {
		Default:     "true",
		Description: "Aborts the read when the command exits non-zero.",
		Example:     "false",
	},
	"shellexec.pre_read[].debounce": {
		Default:     "2s",
		Description: "Window in which repeated reads of the same path run the command once.",
		Example:     "5s",
	},
	"shellexec.pre_read[].async": {
		Default:     "false",
		Description: "Runs the command in the background; an async command cannot abort the read.",
		Example:     "true",
	},
	"shellexec.pre_commit": {
		Description: "Commands run before a write is committed.",
	},
	"shellexec.pre_commit[].cmd": {
		Default:     "",
		Description: "Command line run with `sh -c`.",
		Example:     "./scripts/lint.sh",
	},
	"shellexec.pre_commit[].timeout": {
		Default:     "30s",
		Description: "Wall-clock limit for the command; a timeout counts as a failure.",
		Example:     "10s",
	},
	"shellexec.pre_commit[].fail_on_error": {
		Default:     "true",
		Description: "Rejects the write when the command exits non-zero.",
		Example:     "false",
	},
	"shellexec.pre_commit[].debounce": {
		Default:     "",
		Description: "Accepted for symmetry with `pre_read` but ignored for pre-commit commands.",
		Example:     "2s",
	},
	"shellexec.pre_commit[].async": {
		Default:     "false",
		Description: "Runs the command in the background; an async command cannot reject the write.",
		Example:     "true",
	},
	"shellexec.post_write": {
		Description: "Commands run after bytes reach disk; they never halt the write.",
	},
	"shellexec.post_write[].cmd": {
		Default:     "",
		Description: "Command line run with `sh -c`.",
		Example:     "./scripts/push.sh",
	},
	"shellexec.post_write[].timeout": {
		Default:     "30s",
		Description: "Wall-clock limit for the command.",
		Example:     "60s",
	},
	"shellexec.post_write[].fail_on_error": {
		Default:     "true",
		Description: "Accepted for symmetry with `pre_read` but ignored, because a post-write command cannot undo the write.",
		Example:     "false",
	},
	"shellexec.post_write[].debounce": {
		Default:     "",
		Description: "Accepted for symmetry with `pre_read` but ignored for post-write commands.",
		Example:     "2s",
	},
	"shellexec.post_write[].async": {
		Default:     "false",
		Description: "Runs the command in the background so the write returns before it finishes.",
		Example:     "true",
	},
	"readonly": {
		Default:     "true",
		Description: "Rejects every write; set to `false` to let identities with write grants publish.",
		Example:     "false",
	},
	"write_conflict_policy": {
		Default:     "hash",
		Description: "How overlapping writes to one file resolve: `hash` makes overwrites compare-and-swap, `last_write_wins` accepts the newest.",
		Example:     "last_write_wins",
	},
	"max_jobs": {
		Default:     "8",
		Description: "Maximum number of concurrent background jobs started with `spawn`.",
		Example:     "16",
	},
	"rules": {
		Description: "Server-wide defaults for folder rules; the rules themselves live in `lore.json` and `.lore/config.yaml`.",
	},
	"rules.growth": {
		Default:     "1.25",
		Description: "Multiplier applied to a file's initial size for `max: initial` size rules; must be at least `1`.",
		Example:     "1.5",
	},
	"rules.tokenizer": {
		Default:     "",
		Description: "Reserved for token-based size rules; any value is rejected until a tokenizer ships.",
		Example:     "",
	},
	"tokens": {
		Description: "The bearer-token issuer for the MCP endpoint and JSON API. The ES256 signing key is generated under `data_dir/auth/` on first start.",
	},
	"tokens.issuer": {
		Default:     "",
		Description: "Value of the `iss` claim and base URL for `/.well-known/jwks.json`.",
		Example:     "https://docs.example.com",
	},
	"tokens.audience": {
		Default:     "",
		Description: "Required `aud` claim; one value per server.",
		Example:     "https://docs.example.com",
	},
	"tokens.access_ttl": {
		Default:     "1h",
		Description: "Lifetime of an access token.",
		Example:     "15m",
	},
	"tokens.refresh_ttl": {
		Default:     "720h",
		Description: "Lifetime of a refresh token.",
		Example:     "168h",
	},
	"oidc_issuers": {
		Description: "External identity providers whose JWTs can be exchanged for OpenLore tokens (workload identity federation).",
	},
	"oidc_issuers[].issuer_url": {
		Default:     "",
		Description: "Value the `iss` claim of an exchanged JWT must equal.",
		Example:     "https://token.actions.githubusercontent.com",
	},
	"oidc_issuers[].jwks": {
		Description: "How the issuer's public keys are obtained.",
	},
	"oidc_issuers[].jwks.mode": {
		Default:     "discovery",
		Description: "`discovery` reads keys from the issuer's `.well-known/openid-configuration`; `url` fetches a JWKS document from `url`.",
		Example:     "url",
	},
	"oidc_issuers[].jwks.url": {
		Default:     "",
		Description: "URL of the JWKS document; required when `mode` is `url`.",
		Example:     "https://spire.example/keys",
	},
	"inbox": {
		Description: "HTTP uploads into a docset inbox with a revocable credential.",
	},
	"inbox.max_upload_size": {
		Default:     "10MB",
		Description: "Largest upload accepted, as a byte size.",
		Example:     "25MB",
	},
	"inbox.allowed_types": {
		Description: "File types the inbox accepts. Each entry maps extensions to the MIME type the upload must declare.",
	},
	"inbox.allowed_types[].extensions": {
		Default:     `[".md", ".markdown"]`,
		Description: "File extensions, including the leading dot, that this entry covers.",
		Example:     `[".m4a"]`,
	},
	"inbox.allowed_types[].mime": {
		Default:     "text/markdown",
		Description: "MIME type an upload with one of these extensions must declare.",
		Example:     "audio/mp4",
	},
	"plugins": {
		Description: "Settings for built-in plugins.",
	},
	"plugins.skills": {
		Description: "The skills plugin, which serves routines to agents.",
	},
	"plugins.skills.enabled": {
		Default:     "false",
		Description: "Serves skills from `skills_dir` and remote skill references.",
		Example:     "true",
	},
	"plugins.skills.remote_check_ttl": {
		Default:     "60s",
		Description: "How long a fetched remote skill is cached before it is checked again.",
		Example:     "5m",
	},
	"plugins.skills.remote_timeout": {
		Default:     "3s",
		Description: "Time limit for fetching one remote skill.",
		Example:     "10s",
	},
	"plugins.skills.remote_max_bytes": {
		Default:     "10MB",
		Description: "Largest remote skill document accepted, as a byte size.",
		Example:     "1MB",
	},
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: configdocs <output>")
		os.Exit(2)
	}
	content, err := Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], content, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Generate renders the reference page, or reports keys that are documented
// but not read by the loader, or read by the loader but not documented.
func Generate() ([]byte, error) {
	keys := config.YAMLKeys()
	if err := checkCoverage(keys); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.WriteString("<!-- Code generated by go generate; DO NOT EDIT. -->\n\n")
	out.WriteString("# openlore.yml reference\n\n")
	out.WriteString("Every key `openlore.yml` accepts, in the order the server reads them. Durations are Go duration strings such as `30s` or `24h`; byte sizes accept `KB`, `MB` and `GB` suffixes.\n\n")
	out.WriteString("`openlore.yml` holds deployment settings. Identities, roles and docsets belong in `lore.json`; see [Configuration and identity](configuration-and-identity.md).\n\n")

	out.WriteString("---\n\n## Top-level keys\n\n")
	for _, key := range keys {
		if key.Section || strings.Contains(key.Path, ".") {
			continue
		}
		writeEntry(&out, key)
	}
	for _, key := range keys {
		if !key.Section || strings.Contains(key.Path, ".") {
			continue
		}
		fmt.Fprintf(&out, "---\n\n## `%s`\n\n%s\n\n", key.Path, entries[key.Path].Description)
		for _, child := range keys {
			if !strings.HasPrefix(child.Path, key.Path+".") && !strings.HasPrefix(child.Path, key.Path+"[].") {
				continue
			}
			writeEntry(&out, child)
		}
	}
	return append(bytes.TrimSpace(out.Bytes()), '\n'), nil
}

func checkCoverage(keys []config.YAMLKey) error {
	known := make(map[string]bool, len(keys))
	var missing []string
	for _, key := range keys {
		known[key.Path] = true
		if _, ok := entries[key.Path]; !ok {
			missing = append(missing, key.Path)
		}
	}
	var unknown []string
	for path := range entries {
		if !known[path] {
			unknown = append(unknown, path)
		}
	}
	sort.Strings(unknown)
	if len(missing) > 0 || len(unknown) > 0 {
		return fmt.Errorf("configdocs: keys without entries: %v; entries without keys: %v", missing, unknown)
	}
	return nil
}

func writeEntry(out *bytes.Buffer, key config.YAMLKey) {
	e := entries[key.Path]
	fmt.Fprintf(out, "### `%s`\n\n", key.Path)
	fmt.Fprintf(out, "**Type:** `%s`", key.Type)
	if !key.Section {
		if e.Default == "" {
			out.WriteString("  \n**Default:** none")
		} else {
			fmt.Fprintf(out, "  \n**Default:** `%s`", e.Default)
		}
	}
	fmt.Fprintf(out, "\n\n%s\n\n", e.Description)
	if key.Section {
		return
	}
	out.WriteString("```yaml\n")
	out.WriteString(yamlSnippet(key.Path, e.Example))
	out.WriteString("```\n\n")
}

// yamlSnippet writes the key at its full nesting so the reader can paste it
// into a file, for example "analytics:\n  log:\n    rotate: 1h\n".
func yamlSnippet(path, value string) string {
	segments := strings.Split(path, ".")
	var b strings.Builder
	indent := ""
	for i, seg := range segments {
		name := strings.TrimSuffix(seg, "[]")
		prefix := indent
		switch {
		case i > 0 && strings.HasSuffix(segments[i-1], "[]"):
			// First key of a list item: "  - key:" then children indent past the dash.
			prefix = indent + "  - "
			indent += "    "
		case i > 0:
			indent += "  "
			prefix = indent
		}
		if i == len(segments)-1 {
			if value == "" {
				fmt.Fprintf(&b, "%s%s:\n", prefix, name)
			} else {
				fmt.Fprintf(&b, "%s%s: %s\n", prefix, name, value)
			}
		} else {
			fmt.Fprintf(&b, "%s%s:\n", prefix, name)
		}
	}
	return b.String()
}
