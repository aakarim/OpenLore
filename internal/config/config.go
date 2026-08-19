package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// Config holds the resolved server configuration.
type Config struct {
	ConfigVersion   string
	Debug           bool
	Port            int
	MetricsPort     int
	HostKeyPath     string
	AllowKeyless    bool
	UnknownIdentity string // "allow" (default) or "deny"
	DefaultCwd      string
	MOTD            string
	AuthFile        string
	SkillsDir       string
	// WritableDir is the disk-backed content root layered over embedded docs.
	// Its directory hierarchy is exposed directly at the virtual root.
	WritableDir string
	// DataDir is the server's writable control-plane data root. Distinct from
	// docset content. Defaults to ./.openlore.
	DataDir         string
	HTTPPort        int
	ExternalSSHPort int // advertised SSH port (for X-SSH-Port header behind a LB)
	// MCPEnabled controls whether the always-on MCP-over-HTTP endpoint runs.
	// Default true. The endpoint is mounted at MCPPath on the HTTP server.
	MCPEnabled bool
	MCPPath    string
	// MCPRequireAuth overrides the SSH-derived authentication posture for the
	// MCP endpoint. Nil inherits !AllowKeyless; true forces OAuth so clients
	// such as Claude open the browser login flow.
	MCPRequireAuth *bool
	// APIEnabled controls whether the plain JSON HTTP API (backed by the MCP
	// server) runs. Default true. It is mounted at APIPath on the HTTP server.
	APIEnabled   bool
	APIPath      string
	TLSCert      string
	TLSKey       string
	MTLS         MTLSConfig
	CAKeysFile   string
	HostCertFile string
	Files        FilesConfig
	Passkeys     PasskeysConfig
	// Shellexec is the external-command middleware config (pre_read, pre_commit,
	// post_write) run by the built-in shellexec plugin. Replaces the legacy
	// event-bus `hooks` path with middleware on the read/write chains.
	Shellexec ShellexecConfig
	Logger    *slog.Logger

	// Readonly is the global write lock. Default true: the substrate is a
	// read-only filesystem and no write verbs are available. Set false to
	// enable the experimental writable substrate (SetWriteable is called at
	// startup). Global readonly is a hard physical lock — a per-docset
	// readonly=false cannot loosen it.
	Readonly bool

	// WriteConflictPolicy is the global default policy for whole-file overwrite
	// verbs (`>`, tee, sed -i, publish). Default "hash" (compare-and-swap); set
	// "last_write_wins" for unconditional overwrites. A per-docset override
	// (DocsetSpec.WriteConflictPolicy) takes precedence for that docset.
	WriteConflictPolicy vfs.WriteConflictPolicy

	// MaxJobs bounds concurrent async `spawn` jobs (Part D). Default 8.
	MaxJobs int

	// Tokens configures bearer-token issuance/verification for the MCP + HTTP
	// API. This is server infrastructure (issuer identity, audience, signing
	// key, TTLs) — not per-lore access policy — so it lives in openlore.yml
	// alongside passkeys, not in lore.json. When nil, token auth is disabled
	// and the MCP/HTTP endpoints behave as anonymous callers (Phase 0).
	Tokens  *AuthTokensConfig
	Inbox   InboxConfig
	Plugins PluginsConfig

	// OIDCIssuers are external IdPs whose JWTs may be exchanged for OpenLore
	// tokens at the token endpoint via the jwt-bearer grant (workload identity
	// federation). When set, each issuer's JWKS is fetched (discovery) and its
	// assertions are verified and mapped to identities. Server infrastructure,
	// hence openlore.yml.
	OIDCIssuers []OIDCIssuer

	// Track sources for conflict detection.
	configFileLoaded   bool
	embeddedConfigUsed bool
}

type PluginsConfig struct{ Skills SkillsPluginConfig }
type SkillsPluginConfig struct {
	Enabled        bool
	RemoteCheckTTL time.Duration
	RemoteTimeout  time.Duration
	RemoteMaxBytes int64
}

const DefaultInboxMaxUploadSize int64 = 10 * 1024 * 1024

type InboxConfig struct {
	MaxUploadSize int64
	AllowedTypes  map[string]string
}
type inboxAllowedYAML struct {
	Extensions []string `yaml:"extensions"`
	MIME       string   `yaml:"mime"`
}
type inboxYAML struct {
	MaxUploadSize string             `yaml:"max_upload_size"`
	AllowedTypes  []inboxAllowedYAML `yaml:"allowed_types"`
}

func parseByteSize(value string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(value))
	multiplier := int64(1)
	for suffix, m := range map[string]int64{"KB": 1024, "MB": 1024 * 1024, "GB": 1024 * 1024 * 1024} {
		if strings.HasSuffix(s, suffix) {
			multiplier = m
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("invalid inbox max_upload_size %q", value)
	}
	return n * multiplier, nil
}

func applyInboxConfig(cfg *Config, in *inboxYAML) error {
	if in == nil {
		return nil
	}
	if in.MaxUploadSize != "" {
		n, err := parseByteSize(in.MaxUploadSize)
		if err != nil {
			return err
		}
		cfg.Inbox.MaxUploadSize = n
	}
	if in.AllowedTypes != nil {
		cfg.Inbox.AllowedTypes = map[string]string{}
		for _, item := range in.AllowedTypes {
			mt, _, err := mime.ParseMediaType(item.MIME)
			if err != nil || mt == "" || mt != strings.ToLower(item.MIME) {
				return fmt.Errorf("invalid inbox MIME %q", item.MIME)
			}
			if len(item.Extensions) == 0 {
				return fmt.Errorf("inbox allowed type has no extensions")
			}
			for _, raw := range item.Extensions {
				ext := strings.ToLower(strings.TrimSpace(raw))
				if len(ext) < 2 || ext[0] != '.' || strings.ContainsAny(ext, "/\\\x00") || ext == "." || strings.ContainsAny(ext[1:], ". ") {
					return fmt.Errorf("unsafe inbox extension %q", raw)
				}
				if old, ok := cfg.Inbox.AllowedTypes[ext]; ok {
					return fmt.Errorf("duplicate/conflicting inbox extension %q (%s, %s)", ext, old, mt)
				}
				cfg.Inbox.AllowedTypes[ext] = mt
			}
		}
	}
	return nil
}

// ShellexecConfig is the openlore.yml `shellexec:` block: external commands run
// as middleware on the read and write paths. pre_read runs before a read (may
// abort it), pre_commit runs before a write commits (may reject it), post_write
// runs after a durable commit (fire-and-forget: never halts the log).
type ShellexecConfig struct {
	PreRead   []ShellexecCmd `yaml:"pre_read"`
	PreCommit []ShellexecCmd `yaml:"pre_commit"`
	PostWrite []ShellexecCmd `yaml:"post_write"`
}

// ShellexecCmd is a single external command run by the shellexec plugin. It is
// run via `sh -c` with the OPENLORE_* env protocol.
type ShellexecCmd struct {
	// Cmd is the shell command line to execute.
	Cmd string `yaml:"cmd"`
	// Timeout is a duration string (e.g. "30s") capping wall-clock runtime.
	// Empty means 30s. A timeout counts as a failure.
	Timeout string `yaml:"timeout"`
	// FailOnError makes a non-zero exit fatal to the operation for pre_read /
	// pre_commit (the read/write is aborted). Defaults to true (nil → true).
	// Ignored for post_write, which never halts the log.
	FailOnError *bool `yaml:"fail_on_error"`
	// Debounce is a duration string coalescing repeated pre_read hits on the
	// same path. Empty means 2s. Only applies to pre_read.
	Debounce string `yaml:"debounce"`
	// Async runs the command in the background (fire-and-forget). Default false
	// (synchronous). An async pre_read / pre_commit cannot abort the operation.
	Async bool `yaml:"async"`
}

// IsEmpty reports whether no shellexec commands are configured.
func (c ShellexecConfig) IsEmpty() bool {
	return len(c.PreRead) == 0 && len(c.PreCommit) == 0 && len(c.PostWrite) == 0
}

// OKFDocsetConfig configures the built-in Open Knowledge Format validator for a
// docset. Its presence on a DocsetSpec activates OKF validation across that
// docset's subtree (defaults: enforce=true, patterns=["*.md"]).
//
// It lives on the docset (in lore.json) rather than as a global block so OKF
// scoping is defined in the same place as the docset's paths and grants and can
// never drift from them: a write is validated by the OKF config of the docset
// that owns its path (the longest matching display root, exactly as authz
// resolves grants). Include/exclude for narrower subtrees is expressed with
// nested docsets — a child docset with OKF adds validation to that subtree; a
// child docset without OKF shadows a parent's OKF and exempts that subtree.
type OKFDocsetConfig struct {
	// Enforce rejects non-conformant writes when true (nil → true, the default).
	// When false, a non-conformant write is logged but allowed through.
	Enforce *bool `json:"enforce,omitempty"`
	// Patterns are globs matched against a write target's basename to select
	// which files are validated. Empty defaults to ["*.md"].
	Patterns []string `json:"patterns,omitempty"`
}

// PasskeysConfig holds WebAuthn passkey configuration.
type PasskeysConfig struct {
	Enabled      bool
	RPID         string
	RPName       string
	RPOrigins    []string
	LorePath     string
	PasskeysFile string
	SessionTTL   string // parsed as time.Duration
}

// FilesConfig controls which files are served.
type FilesConfig struct {
	Allowed []string
	Denied  []string
	Ignore  []string
}

// AuthConfig is loaded from lore.json.
type AuthConfig struct {
	AllowKeyless    *bool                 `json:"allow_keyless,omitempty"`
	UnknownIdentity string                `json:"unknown_identity,omitempty"`
	DefaultCwd      string                `json:"default_cwd,omitempty"`
	Docsets         map[string]DocsetSpec `json:"docsets"`
	Roles           map[string]RoleSpec   `json:"roles,omitempty"`
	// Default is a legacy authority field retained only for JSON parsing. It is ignored.
	Default    map[string]string `json:"default,omitempty"`
	Identities []AuthIdentity    `json:"identities"`
}

type CapabilityRules struct {
	Capabilities []string `json:"capabilities,omitempty"`
}

// RoleSpec is a reusable set of capabilities. Docset grants are resource-side
// ACL entries, not properties of the role itself.
type RoleSpec struct {
	Comment string          `json:"comment,omitempty"`
	Allow   CapabilityRules `json:"allow,omitempty"`
	Deny    CapabilityRules `json:"deny,omitempty"`
}

type DocsetAccess struct {
	Allow map[string]string `json:"allow,omitempty"`
	Deny  []string          `json:"deny,omitempty"`
}

// AuthTokensConfig controls the bearer-token issuer for the MCP + HTTP API.
// It is server infrastructure and loaded from openlore.yml (hence yaml tags).
type AuthTokensConfig struct {
	Issuer     string `yaml:"issuer" json:"issuer,omitempty"`           // `iss` claim + JWKS base
	Audience   string `yaml:"audience" json:"audience,omitempty"`       // required `aud`; one per instance
	AccessTTL  string `yaml:"access_ttl" json:"access_ttl,omitempty"`   // duration string, default 30m
	RefreshTTL string `yaml:"refresh_ttl" json:"refresh_ttl,omitempty"` // duration string, default 720h
}

// OIDCIssuer is an external IdP trusted for WIF token exchange. Server
// infrastructure, loaded from openlore.yml.
type OIDCIssuer struct {
	IssuerURL string   `yaml:"issuer_url" json:"issuer_url"`
	JWKS      JWKSSpec `yaml:"jwks" json:"jwks,omitempty"`
}

// JWKSSpec configures how an OIDC issuer's public keys are obtained.
// "discovery" (default) fetches them via the issuer's
// .well-known/openid-configuration; "url" fetches a JWKS document directly from
// URL, for issuers that publish keys without a discovery document (e.g. a SPIRE
// trust-bundle endpoint).
type JWKSSpec struct {
	Mode string `yaml:"mode" json:"mode,omitempty"` // "discovery" (default) or "url"
	URL  string `yaml:"url" json:"url,omitempty"`   // JWKS document URL; required iff mode is "url"
}

// DocsetSpec defines a named set of path mappings.
type DocsetSpec struct {
	Paths  []PathMapping `json:"paths"`
	Access DocsetAccess  `json:"access,omitempty"`
	// AgentSkills is ignored. Collections are selected dynamically by xattr.
	AgentSkills bool `json:"-"`
	// Aliases are alternate display roots for the first path. They expose the
	// same content while the first path remains canonical for home, inbox,
	// policy, hooks, and changesets.
	Aliases []string `json:"aliases,omitempty"`
	// Inbox names a subfolder (VFS path, relative to a docset root or absolute)
	// that the `publish` grant confines create/edit to. Empty = the docset has
	// no inbox, so a `publish` grant on it can write nothing.
	Inbox string `json:"inbox,omitempty"`
	// MaxWriteSize caps a single write's bytes for this docset; 0 = default (2.5MB).
	MaxWriteSize int64 `json:"max_write_size,omitempty"`

	// Readonly is the per-docset policy check (enforced in the write pipeline,
	// not on the substrate). nil means "inherit" (writable when the global lock
	// is open). A docset can only further restrict: setting it true blocks
	// writes to this docset even when the global lock is open; setting it false
	// is meaningless when the global lock is closed.
	Readonly *bool `json:"readonly,omitempty"`

	// WriteConflictPolicy overrides the global write-conflict policy for writes
	// to this docset. "" inherits Config.WriteConflictPolicy; "hash" forces
	// compare-and-swap overwrites; "last_write_wins" forces unconditional ones.
	WriteConflictPolicy string `json:"write_conflict_policy,omitempty"`

	// OKF, when non-nil, activates the built-in Open Knowledge Format validator
	// for this docset's subtree (see OKFDocsetConfig). nil means OKF is off for
	// this docset; scope narrower subtrees with nested docsets.
	OKF *OKFDocsetConfig `json:"okf,omitempty"`
}

// PathMapping represents a path entry — either a simple string path or a
// source→display mapping.
type PathMapping struct {
	Source  string // the real path (relative to root dir or assets/lore)
	Display string // the path shown in the shell (empty = same as Source)
}

// MarshalJSON preserves the two input forms accepted by UnmarshalJSON.
func (p PathMapping) MarshalJSON() ([]byte, error) {
	if p.Display == "" || p.Display == p.Source {
		return json.Marshal(p.Source)
	}
	return json.Marshal(map[string]string{p.Source: p.Display})
}

// UnmarshalJSON supports both string and {"source": "display"} forms.
func (p *PathMapping) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		p.Source = s
		p.Display = s
		return nil
	}

	// Try dict (single key-value)
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("path must be a string or {\"source\": \"display\"} object: %s", string(data))
	}
	if len(m) != 1 {
		return fmt.Errorf("path object must have exactly one key: %s", string(data))
	}
	for k, v := range m {
		p.Source = k
		p.Display = v
	}
	return nil
}

// AuthIdentity defines a user identity and its role membership.
type AuthIdentity struct {
	Name             string            `json:"name"`
	Comment          string            `json:"comment,omitempty"`
	CreatedBy        string            `json:"created_by,omitempty"`
	ClientIDMetadata *ClientIDMetadata `json:"client_id_metadata,omitempty"`
	// PublicKey is optional: an identity may exist purely as a passkey/token
	// login target (no SSH key). Empty = no SSH public-key auth for this identity.
	PublicKey string   `json:"public_key,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	// Docsets is a legacy authority field retained only for JSON parsing. It is ignored.
	Docsets map[string]string `json:"docsets"`
	// Home names the docset that serves as this identity's home directory. Its
	// display path becomes $HOME and the session's initial working directory.
	// Home ownership provides implicit rw unless a nested docset takes precedence.
	Home string `json:"home,omitempty"`
	// Capabilities is a legacy authority field retained only for JSON parsing. It is ignored.
	Capabilities []string `json:"capabilities,omitempty"`

	// Match lists the token-claim predicates that resolve TO this identity.
	// Resolution criteria live on the identity they select (rather than a
	// separate rule list) since every rule maps to exactly one identity. The
	// human case needs no entry: a token whose `sub` equals this identity's
	// Name resolves here implicitly. WIF exchanges (jwt-bearer) match on
	// `sub`/`sub_prefix`/`aud`/`claims` entries with narrowing `scope`/`ttl`.
	Match []IdentityMatch `json:"match,omitempty"`
	// Delegates are identities permitted to act on behalf of this principal.
	// An omitted Roles field inherits all principal roles; a present field is
	// intersected with the principal's roles. Denials always subtract authority.
	Delegates []DelegateEntry `json:"delegates,omitempty"`
}

type ClientIDMetadata struct {
	URL        string    `json:"url"`
	PinnedName string    `json:"pinned_name"`
	FirstSeen  time.Time `json:"first_seen"`
}

// DelegateEntry grants an identity permission to act for a principal while
// capping its authority at the principal's current authority.
type DelegateEntry struct {
	Identity         string   `json:"identity"`
	Roles            []string `json:"roles,omitempty"`
	DenyDocsets      []string `json:"deny_docsets,omitempty"`
	DenyCapabilities []string `json:"deny_capabilities,omitempty"`
	ClientAuth       string   `json:"client_auth,omitempty"`
}

// MarshalJSON preserves the semantic distinction between an omitted roles
// field (inherit all principal roles) and an explicitly empty list (inherit no
// roles). encoding/json's ordinary omitempty handling collapses those states.
func (d DelegateEntry) MarshalJSON() ([]byte, error) {
	type wire struct {
		Identity         string   `json:"identity"`
		Roles            []string `json:"roles,omitempty"`
		DenyDocsets      []string `json:"deny_docsets,omitempty"`
		DenyCapabilities []string `json:"deny_capabilities,omitempty"`
		ClientAuth       string   `json:"client_auth,omitempty"`
	}
	if d.Roles == nil {
		return json.Marshal(wire(d))
	}
	type wireWithRoles struct {
		Identity         string   `json:"identity"`
		Roles            []string `json:"roles"`
		DenyDocsets      []string `json:"deny_docsets,omitempty"`
		DenyCapabilities []string `json:"deny_capabilities,omitempty"`
		ClientAuth       string   `json:"client_auth,omitempty"`
	}
	return json.Marshal(wireWithRoles(d))
}

// IdentityMatch is a token-claim predicate attached to an AuthIdentity. When a
// verified assertion's claims satisfy it (all specified fields must hold), the
// assertion resolves to the enclosing identity. Exact `sub` takes precedence
// over `sub_prefix`/`aud`/`claims` pattern matches; `scope` narrows and `ttl`
// caps the brokered OpenLore token.
type IdentityMatch struct {
	Sub       string            `json:"sub,omitempty"`
	SubPrefix string            `json:"sub_prefix,omitempty"`
	Aud       string            `json:"aud,omitempty"`
	Claims    map[string]string `json:"claims,omitempty"`
	Scope     string            `json:"scope,omitempty"` // narrowing scope for matched tokens (WIF)
	TTL       string            `json:"ttl,omitempty"`   // caps brokered token TTL (WIF)
}

// Option is a functional option for configuring the server.
type Option func(*Config) error

// fileConfig mirrors Config for YAML deserialization.
type fileConfig struct {
	ConfigVersion       string                 `yaml:"version"`
	Debug               bool                   `yaml:"debug"`
	Port                int                    `yaml:"port"`
	MetricsPort         int                    `yaml:"metrics_port"`
	HostKeyPath         string                 `yaml:"host_key_path"`
	MOTD                string                 `yaml:"motd"`
	MOTDFile            string                 `yaml:"motd_file"`
	AuthFile            string                 `yaml:"auth_file"`
	SkillsDir           string                 `yaml:"skills_dir"`
	WritableDir         string                 `yaml:"writable_dir"`
	DataDir             string                 `yaml:"data_dir"`
	HTTPPort            int                    `yaml:"http_port"`
	ExternalSSHPort     int                    `yaml:"external_ssh_port"`
	MCP                 *mcpYAML               `yaml:"mcp"`
	API                 *apiYAML               `yaml:"api"`
	TLSCert             string                 `yaml:"tls_cert"`
	TLSKey              string                 `yaml:"tls_key"`
	Auth                authInfrastructureYAML `yaml:"auth"`
	CAKeysFile          string                 `yaml:"ca_keys_file"`
	HostCertFile        string                 `yaml:"host_cert_file"`
	DefaultCwd          string                 `yaml:"default_cwd"`
	Files               *filesYAML             `yaml:"files"`
	Passkeys            *passkeysYAML          `yaml:"passkeys"`
	Shellexec           *ShellexecConfig       `yaml:"shellexec"`
	Readonly            *bool                  `yaml:"readonly"`
	WriteConflictPolicy string                 `yaml:"write_conflict_policy"`
	MaxJobs             int                    `yaml:"max_jobs"`
	// Tokens + OIDCIssuers are server infrastructure (bearer-token issuance for
	// the MCP + HTTP API), hence configured here rather than in lore.json.
	Tokens      *AuthTokensConfig `yaml:"tokens"`
	OIDCIssuers []OIDCIssuer      `yaml:"oidc_issuers"`
	Inbox       *inboxYAML        `yaml:"inbox"`
	Plugins     pluginsYAML       `yaml:"plugins"`
}

type authInfrastructureYAML struct {
	MTLS MTLSConfig `yaml:"mtls"`
}

type MTLSConfig struct {
	CABundle string `yaml:"ca_bundle" json:"ca_bundle,omitempty"`
}

type pluginsYAML struct {
	Skills skillsPluginYAML `yaml:"skills"`
}
type skillsPluginYAML struct {
	Enabled        bool   `yaml:"enabled"`
	RemoteCheckTTL string `yaml:"remote_check_ttl"`
	RemoteTimeout  string `yaml:"remote_timeout"`
	RemoteMaxBytes string `yaml:"remote_max_bytes"`
}

func applySkillsConfig(cfg *Config, in skillsPluginYAML) error {
	cfg.Plugins.Skills.Enabled = in.Enabled
	for _, setting := range []struct {
		value  string
		target *time.Duration
	}{{in.RemoteCheckTTL, &cfg.Plugins.Skills.RemoteCheckTTL}, {in.RemoteTimeout, &cfg.Plugins.Skills.RemoteTimeout}} {
		value, target := setting.value, setting.target
		if value == "" {
			continue
		}
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 {
			return fmt.Errorf("invalid skills remote duration %q", value)
		}
		*target = d
	}
	if in.RemoteMaxBytes != "" {
		n, err := parseByteSize(in.RemoteMaxBytes)
		if err != nil {
			return fmt.Errorf("invalid skills remote_max_bytes: %w", err)
		}
		cfg.Plugins.Skills.RemoteMaxBytes = n
	}
	return nil
}

type mcpYAML struct {
	Enabled     *bool  `yaml:"enabled"`
	Path        string `yaml:"path"`
	RequireAuth *bool  `yaml:"require_auth"`
}

type apiYAML struct {
	Enabled *bool  `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type passkeysYAML struct {
	Enabled      bool     `yaml:"enabled"`
	RPID         string   `yaml:"rp_id"`
	RPName       string   `yaml:"rp_name"`
	RPOrigins    []string `yaml:"rp_origins"`
	LorePath     string   `yaml:"lore_path"`
	PasskeysFile string   `yaml:"passkeys_file"`
	SessionTTL   string   `yaml:"session_ttl"`
}

type filesYAML struct {
	Allowed []string `yaml:"allowed"`
	Denied  []string `yaml:"denied"`
	Ignore  []string `yaml:"ignore"`
}

// New creates a Config by applying options to the defaults.
// Returns an error if both a config file and embedded config are used.
func New(opts ...Option) (Config, error) {
	cfg := Config{
		Port:                2222,
		HTTPPort:            8080,
		MetricsPort:         3000,
		MCPEnabled:          true,
		MCPPath:             "/mcp",
		APIEnabled:          true,
		APIPath:             "/api",
		HostKeyPath:         ".ssh/openlore_ed25519",
		AllowKeyless:        true,
		UnknownIdentity:     "allow",
		DefaultCwd:          "/openlore",
		Readonly:            true,                           // safe default: read-only substrate
		WriteConflictPolicy: vfs.DefaultWriteConflictPolicy, // "hash": overwrites are compare-and-swap
		MaxJobs:             8,                              // bound concurrent async spawn jobs
		Plugins:             PluginsConfig{Skills: SkillsPluginConfig{RemoteCheckTTL: 60 * time.Second, RemoteTimeout: 3 * time.Second, RemoteMaxBytes: 10 * 1024 * 1024}},
		Inbox:               InboxConfig{MaxUploadSize: DefaultInboxMaxUploadSize, AllowedTypes: map[string]string{".md": "text/markdown", ".markdown": "text/markdown"}},
		Files: FilesConfig{
			Allowed: []string{
				"*.md", "*.markdown", "*.txt",
				"*.html", "*.htm", "*.css", "*.js",
				"*.json", "*.yaml", "*.yml",
				"*.csv", "*.tsv", "*.xml", "*.toml",
				"*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg", "*.webp",
			},
			Ignore: []string{
				".git/**", "node_modules/**", ".env*", "**/.DS_Store",
			},
		},
	}

	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return Config{}, err
		}
	}

	if cfg.configFileLoaded && cfg.embeddedConfigUsed {
		return Config{}, errors.New("conflict: cannot use both a config file and embedded config")
	}
	if cfg.MCPEnabled && cfg.MCPRequireAuth != nil && *cfg.MCPRequireAuth && cfg.Tokens == nil {
		return Config{}, errors.New("mcp.require_auth requires tokens to be configured")
	}

	return cfg, nil
}

// WithConfigFile loads configuration from a YAML file.
// Fields in the file override defaults. If the file does not exist,
// no error is returned and the config is unchanged.
func WithConfigFile(path string) Option {
	return func(cfg *Config) error {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("reading config file: %w", err)
		}

		var fc fileConfig
		if err := yaml.Unmarshal(data, &fc); err != nil {
			return fmt.Errorf("parsing config file: %w", err)
		}

		cfg.configFileLoaded = true

		if fc.ConfigVersion != "" {
			cfg.ConfigVersion = fc.ConfigVersion
		}
		cfg.Debug = fc.Debug
		if fc.Port != 0 {
			cfg.Port = fc.Port
		}
		if fc.MetricsPort != 0 {
			cfg.MetricsPort = fc.MetricsPort
		}
		if fc.HostKeyPath != "" {
			cfg.HostKeyPath = fc.HostKeyPath
		}
		if fc.MOTDFile != "" {
			motdData, err := os.ReadFile(fc.MOTDFile)
			if err != nil {
				return fmt.Errorf("reading MOTD file %q: %w", fc.MOTDFile, err)
			}
			cfg.MOTD = string(motdData)
		} else if fc.MOTD != "" {
			cfg.MOTD = fc.MOTD
		}
		if fc.AuthFile != "" {
			cfg.AuthFile = fc.AuthFile
		}
		if fc.SkillsDir != "" {
			cfg.SkillsDir = fc.SkillsDir
		}
		if fc.WritableDir != "" {
			cfg.WritableDir = fc.WritableDir
		}
		if fc.DataDir != "" {
			cfg.DataDir = fc.DataDir
		}
		if fc.HTTPPort != 0 {
			cfg.HTTPPort = fc.HTTPPort
		}
		if fc.ExternalSSHPort != 0 {
			cfg.ExternalSSHPort = fc.ExternalSSHPort
		}
		if fc.TLSCert != "" {
			cfg.TLSCert = fc.TLSCert
		}
		if fc.TLSKey != "" {
			cfg.TLSKey = fc.TLSKey
		}
		cfg.MTLS = fc.Auth.MTLS
		if fc.CAKeysFile != "" {
			cfg.CAKeysFile = fc.CAKeysFile
		}
		if fc.HostCertFile != "" {
			cfg.HostCertFile = fc.HostCertFile
		}
		if fc.DefaultCwd != "" {
			cfg.DefaultCwd = fc.DefaultCwd
		}
		if fc.Files != nil {
			if len(fc.Files.Allowed) > 0 {
				cfg.Files.Allowed = fc.Files.Allowed
			}
			if len(fc.Files.Denied) > 0 {
				cfg.Files.Denied = fc.Files.Denied
			}
			if len(fc.Files.Ignore) > 0 {
				cfg.Files.Ignore = fc.Files.Ignore
			}
		}
		if fc.Shellexec != nil {
			cfg.Shellexec = *fc.Shellexec
		}
		if fc.Readonly != nil {
			cfg.Readonly = *fc.Readonly
		}
		if fc.WriteConflictPolicy != "" {
			p, err := vfs.ParseWriteConflictPolicy(fc.WriteConflictPolicy)
			if err != nil {
				return err
			}
			cfg.WriteConflictPolicy = p
		}
		if fc.MaxJobs > 0 {
			cfg.MaxJobs = fc.MaxJobs
		}
		if err := applySkillsConfig(cfg, fc.Plugins.Skills); err != nil {
			return err
		}
		applyPasskeysConfig(cfg, fc.Passkeys)
		applyMCPConfig(cfg, fc.MCP)
		applyAPIConfig(cfg, fc.API)
		applyTokensConfig(cfg, fc.Tokens, fc.OIDCIssuers)
		if err := applyInboxConfig(cfg, fc.Inbox); err != nil {
			return err
		}

		return nil
	}
}

// applyTokensConfig maps bearer-token server settings from the file config.
func applyTokensConfig(cfg *Config, tokens *AuthTokensConfig, oidc []OIDCIssuer) {
	if tokens != nil {
		cfg.Tokens = tokens
	}
	if len(oidc) > 0 {
		cfg.OIDCIssuers = oidc
	}
}

// WithEmbeddedConfig loads config from an embedded YAML byte slice.
// The MOTD fallback is set separately from the config fields.
func WithEmbeddedConfig(data []byte, motdFallback string) Option {
	return func(cfg *Config) error {
		if len(data) > 0 {
			cfg.embeddedConfigUsed = true

			var fc fileConfig
			if err := yaml.Unmarshal(data, &fc); err != nil {
				return fmt.Errorf("parsing embedded config: %w", err)
			}

			if fc.ConfigVersion != "" {
				cfg.ConfigVersion = fc.ConfigVersion
			}
			cfg.Debug = fc.Debug
			if fc.Port != 0 {
				cfg.Port = fc.Port
			}
			if fc.MetricsPort != 0 {
				cfg.MetricsPort = fc.MetricsPort
			}
			if fc.HostKeyPath != "" {
				cfg.HostKeyPath = fc.HostKeyPath
			}
			if fc.MOTDFile != "" {
				motdData, err := os.ReadFile(fc.MOTDFile)
				if err != nil {
					return fmt.Errorf("reading MOTD file %q: %w", fc.MOTDFile, err)
				}
				cfg.MOTD = string(motdData)
			} else if fc.MOTD != "" {
				cfg.MOTD = fc.MOTD
			}
			if fc.AuthFile != "" {
				cfg.AuthFile = fc.AuthFile
			}
			if fc.SkillsDir != "" {
				cfg.SkillsDir = fc.SkillsDir
			}
			if fc.WritableDir != "" {
				cfg.WritableDir = fc.WritableDir
			}
			if fc.DataDir != "" {
				cfg.DataDir = fc.DataDir
			}
			if fc.HTTPPort != 0 {
				cfg.HTTPPort = fc.HTTPPort
			}
			if fc.ExternalSSHPort != 0 {
				cfg.ExternalSSHPort = fc.ExternalSSHPort
			}
			if fc.TLSCert != "" {
				cfg.TLSCert = fc.TLSCert
			}
			if fc.TLSKey != "" {
				cfg.TLSKey = fc.TLSKey
			}
			cfg.MTLS = fc.Auth.MTLS
			if fc.CAKeysFile != "" {
				cfg.CAKeysFile = fc.CAKeysFile
			}
			if fc.HostCertFile != "" {
				cfg.HostCertFile = fc.HostCertFile
			}
			if fc.DefaultCwd != "" {
				cfg.DefaultCwd = fc.DefaultCwd
			}
			if fc.Files != nil {
				if len(fc.Files.Allowed) > 0 {
					cfg.Files.Allowed = fc.Files.Allowed
				}
				if len(fc.Files.Denied) > 0 {
					cfg.Files.Denied = fc.Files.Denied
				}
				if len(fc.Files.Ignore) > 0 {
					cfg.Files.Ignore = fc.Files.Ignore
				}
			}
			if fc.Shellexec != nil {
				cfg.Shellexec = *fc.Shellexec
			}
			if fc.Readonly != nil {
				cfg.Readonly = *fc.Readonly
			}
			if err := applySkillsConfig(cfg, fc.Plugins.Skills); err != nil {
				return err
			}
			if fc.WriteConflictPolicy != "" {
				p, err := vfs.ParseWriteConflictPolicy(fc.WriteConflictPolicy)
				if err != nil {
					return err
				}
				cfg.WriteConflictPolicy = p
			}
			if fc.MaxJobs > 0 {
				cfg.MaxJobs = fc.MaxJobs
			}
			applyPasskeysConfig(cfg, fc.Passkeys)
			applyMCPConfig(cfg, fc.MCP)
			applyAPIConfig(cfg, fc.API)
			applyTokensConfig(cfg, fc.Tokens, fc.OIDCIssuers)
			if err := applyInboxConfig(cfg, fc.Inbox); err != nil {
				return err
			}
		}

		// MOTD fallback: only set if nothing else has set it yet
		if cfg.MOTD == "" && motdFallback != "" {
			cfg.MOTD = motdFallback
		}

		return nil
	}
}

// WithPort sets the SSH server port.
func WithPort(port int) Option {
	return func(cfg *Config) error {
		cfg.Port = port
		return nil
	}
}

// WithMetricsPort sets the metrics HTTP port. 0 disables metrics.
func WithMetricsPort(port int) Option {
	return func(cfg *Config) error {
		cfg.MetricsPort = port
		return nil
	}
}

// WithReadonly sets the global write lock. true (the default) keeps the
// substrate read-only; false enables the experimental writable substrate.
func WithReadonly(readonly bool) Option {
	return func(cfg *Config) error {
		cfg.Readonly = readonly
		return nil
	}
}

// WithWriteConflictPolicy sets the global default write-conflict policy for
// whole-file overwrite verbs. Empty resolves to the default (hash). Invalid
// values are rejected.
func WithWriteConflictPolicy(policy string) Option {
	return func(cfg *Config) error {
		p, err := vfs.ParseWriteConflictPolicy(policy)
		if err != nil {
			return err
		}
		cfg.WriteConflictPolicy = p
		return nil
	}
}

// WithHostKeyPath sets the path to the SSH host key.
func WithHostKeyPath(path string) Option {
	return func(cfg *Config) error {
		cfg.HostKeyPath = path
		return nil
	}
}

// WithAllowKeyless controls whether keyless SSH connections are allowed.
func WithAllowKeyless(allow bool) Option {
	return func(cfg *Config) error {
		cfg.AllowKeyless = allow
		return nil
	}
}

// WithDefaultCwd sets the default working directory for shell sessions.
func WithDefaultCwd(cwd string) Option {
	return func(cfg *Config) error {
		cfg.DefaultCwd = cwd
		return nil
	}
}

// WithMOTD sets the message of the day, replacing any previous value.
func WithMOTD(motd string) Option {
	return func(cfg *Config) error {
		cfg.MOTD = motd
		return nil
	}
}

// WithMOTDFile loads the MOTD from a file path, replacing any previous value.
func WithMOTDFile(path string) Option {
	return func(cfg *Config) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading MOTD file: %w", err)
		}
		cfg.MOTD = string(data)
		return nil
	}
}

// WithAuthFile sets the path to the auth.json file.
func WithAuthFile(path string) Option {
	return func(cfg *Config) error {
		cfg.AuthFile = path
		return nil
	}
}

// WithSkillsDir sets the directory for loading runtime skills.
func WithSkillsDir(dir string) Option {
	return func(cfg *Config) error {
		cfg.SkillsDir = dir
		return nil
	}
}

// WithDataDir sets the server's writable control-plane data root.
func WithDataDir(dir string) Option {
	return func(cfg *Config) error {
		cfg.DataDir = dir
		return nil
	}
}

// WithWritableDir sets the disk-backed content root layered over embedded docs.
func WithWritableDir(dir string) Option {
	return func(cfg *Config) error {
		cfg.WritableDir = dir
		return nil
	}
}

// WithAllowedPatterns sets the file patterns to serve.
func WithAllowedPatterns(patterns []string) Option {
	return func(cfg *Config) error {
		cfg.Files.Allowed = patterns
		return nil
	}
}

// WithIgnorePatterns sets the ignore patterns.
func WithIgnorePatterns(patterns []string) Option {
	return func(cfg *Config) error {
		cfg.Files.Ignore = patterns
		return nil
	}
}

// WithLogger sets the structured logger.
func WithLogger(logger *slog.Logger) Option {
	return func(cfg *Config) error {
		cfg.Logger = logger
		return nil
	}
}

// WithDebug enables debug-level server logging.
func WithDebug(enabled bool) Option {
	return func(cfg *Config) error {
		cfg.Debug = enabled
		return nil
	}
}

// WithHTTPPort sets the HTTP front page server port. 0 disables it.
func WithHTTPPort(port int) Option {
	return func(cfg *Config) error {
		cfg.HTTPPort = port
		return nil
	}
}

// WithCAKeysFile sets the path to a file containing trusted CA public keys
// for SSH certificate authentication (analogous to OpenSSH TrustedUserCAKeys).
func WithCAKeysFile(path string) Option {
	return func(cfg *Config) error {
		cfg.CAKeysFile = path
		return nil
	}
}

// WithHostCertFile sets the path to the SSH host certificate file
// (signed by a CA, analogous to OpenSSH HostCertificate).
func WithHostCertFile(path string) Option {
	return func(cfg *Config) error {
		cfg.HostCertFile = path
		return nil
	}
}

// WithTLS sets TLS certificate and key paths for the HTTP server.
func WithTLS(cert, key string) Option {
	return func(cfg *Config) error {
		cfg.TLSCert = cert
		cfg.TLSKey = key
		return nil
	}
}

// applyMCPConfig merges an mcpYAML into the config.
func applyMCPConfig(cfg *Config, m *mcpYAML) {
	if m == nil {
		return
	}
	if m.Enabled != nil {
		cfg.MCPEnabled = *m.Enabled
	}
	if m.Path != "" {
		cfg.MCPPath = m.Path
	}
	if m.RequireAuth != nil {
		cfg.MCPRequireAuth = m.RequireAuth
	}
}

// MCPAuthRequired resolves the MCP-specific override. When it is omitted, MCP
// retains the historical behavior of mirroring the SSH keyless posture.
func (cfg Config) MCPAuthRequired() bool {
	if cfg.MCPRequireAuth != nil {
		return *cfg.MCPRequireAuth
	}
	return !cfg.AllowKeyless
}

// WithMCPPath sets the path the MCP-over-HTTP endpoint is mounted at on the
// HTTP server (e.g. "/mcp").
func WithMCPPath(path string) Option {
	return func(cfg *Config) error {
		cfg.MCPPath = path
		return nil
	}
}

// WithMCPEnabled toggles the MCP-over-HTTP endpoint.
func WithMCPEnabled(enabled bool) Option {
	return func(cfg *Config) error {
		cfg.MCPEnabled = enabled
		return nil
	}
}

// applyAPIConfig merges an apiYAML into the config.
func applyAPIConfig(cfg *Config, a *apiYAML) {
	if a == nil {
		return
	}
	if a.Enabled != nil {
		cfg.APIEnabled = *a.Enabled
	}
	if a.Path != "" {
		cfg.APIPath = a.Path
	}
}

// WithAPIPath sets the path the JSON HTTP API is mounted at on the HTTP server
// (e.g. "/api").
func WithAPIPath(path string) Option {
	return func(cfg *Config) error {
		cfg.APIPath = path
		return nil
	}
}

// WithAPIEnabled toggles the JSON HTTP API.
func WithAPIEnabled(enabled bool) Option {
	return func(cfg *Config) error {
		cfg.APIEnabled = enabled
		return nil
	}
}

// applyPasskeysConfig merges a passkeysYAML into the config.
func applyPasskeysConfig(cfg *Config, pk *passkeysYAML) {
	if pk == nil {
		return
	}
	cfg.Passkeys.Enabled = pk.Enabled
	if pk.RPID != "" {
		cfg.Passkeys.RPID = pk.RPID
	}
	if pk.RPName != "" {
		cfg.Passkeys.RPName = pk.RPName
	}
	if len(pk.RPOrigins) > 0 {
		cfg.Passkeys.RPOrigins = pk.RPOrigins
	}
	if pk.LorePath != "" {
		cfg.Passkeys.LorePath = pk.LorePath
	}
	if pk.PasskeysFile != "" {
		cfg.Passkeys.PasskeysFile = pk.PasskeysFile
	}
	if pk.SessionTTL != "" {
		cfg.Passkeys.SessionTTL = pk.SessionTTL
	}
}

// WithPasskeys sets the passkeys configuration.
func WithPasskeys(pk PasskeysConfig) Option {
	return func(cfg *Config) error {
		cfg.Passkeys = pk
		return nil
	}
}

// LoadAuthConfig loads auth configuration from a JSON file.
func LoadAuthConfig(path string) (*AuthConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var auth AuthConfig
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}

	if err := ValidateAuthConfig(&auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// ValidateAuthConfig validates a parsed static authorization policy. Legacy
// authority fields are deliberately ignored.
func ValidateAuthConfig(auth *AuthConfig) error {
	if _, ok := auth.Roles["guest"]; ok {
		return fmt.Errorf("role %q is reserved", "guest")
	}
	validateNames := func(where string, values []string) error {
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || strings.TrimSpace(value) != value {
				return fmt.Errorf("%s contains an empty or untrimmed name", where)
			}
			if seen[value] {
				return fmt.Errorf("%s contains duplicate %q", where, value)
			}
			seen[value] = true
		}
		return nil
	}
	for name, role := range auth.Roles {
		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("invalid role name %q", name)
		}
		if err := validateNames(fmt.Sprintf("role %q allow capabilities", name), role.Allow.Capabilities); err != nil {
			return err
		}
		if err := validateNames(fmt.Sprintf("role %q deny capabilities", name), role.Deny.Capabilities); err != nil {
			return err
		}
	}
	homes := map[string]string{}
	identities := map[string]bool{}
	keys := map[string]string{}
	for _, ident := range auth.Identities {
		if ident.Name == "" || strings.TrimSpace(ident.Name) != ident.Name {
			return fmt.Errorf("invalid identity name %q", ident.Name)
		}
		if strings.Contains(ident.Name, "/") || (strings.Contains(ident.Name, "@") && ident.CreatedBy != "oauth") {
			return fmt.Errorf("identity name %q uses a reserved character", ident.Name)
		}
		if ident.CreatedBy != "" && ident.CreatedBy != "oauth" {
			return fmt.Errorf("identity %q has unsupported created_by %q", ident.Name, ident.CreatedBy)
		}
		if ident.Name == "guest" || ident.Name == "anonymous" {
			return fmt.Errorf("identity %q is reserved", ident.Name)
		}
		if identities[ident.Name] {
			return fmt.Errorf("duplicate identity name %q", ident.Name)
		}
		identities[ident.Name] = true
		if key := strings.TrimSpace(ident.PublicKey); key != "" {
			parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
			if err != nil {
				return fmt.Errorf("identity %q has invalid SSH public key: %w", ident.Name, err)
			}
			normalized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed)))
			if other, ok := keys[normalized]; ok {
				return fmt.Errorf("identities %q and %q share public key", other, ident.Name)
			}
			keys[normalized] = ident.Name
		}
		if err := validateNames(fmt.Sprintf("identity %q roles", ident.Name), ident.Roles); err != nil {
			return err
		}
		for _, role := range ident.Roles {
			if role == "guest" {
				return fmt.Errorf("identity %q cannot be assigned reserved role guest", ident.Name)
			}
			if _, ok := auth.Roles[role]; !ok {
				return fmt.Errorf("identity %q references unknown role %q", ident.Name, role)
			}
		}
		delegateNames := map[string]bool{}
		for _, delegate := range ident.Delegates {
			if delegate.Identity == "" || strings.TrimSpace(delegate.Identity) != delegate.Identity || strings.Contains(delegate.Identity, "/") {
				return fmt.Errorf("identity %q has invalid delegate %q", ident.Name, delegate.Identity)
			}
			if delegateNames[delegate.Identity] {
				return fmt.Errorf("identity %q has duplicate delegate %q", ident.Name, delegate.Identity)
			}
			delegateNames[delegate.Identity] = true
			if err := validateNames(fmt.Sprintf("identity %q delegate %q roles", ident.Name, delegate.Identity), delegate.Roles); err != nil {
				return err
			}
			for _, role := range delegate.Roles {
				if _, ok := auth.Roles[role]; !ok {
					return fmt.Errorf("identity %q delegate %q references unknown role %q", ident.Name, delegate.Identity, role)
				}
			}
			if err := validateNames(fmt.Sprintf("identity %q delegate %q denied docsets", ident.Name, delegate.Identity), delegate.DenyDocsets); err != nil {
				return err
			}
			for _, docset := range delegate.DenyDocsets {
				if _, ok := auth.Docsets[docset]; !ok {
					return fmt.Errorf("identity %q delegate %q denies unknown docset %q", ident.Name, delegate.Identity, docset)
				}
			}
			if err := validateNames(fmt.Sprintf("identity %q delegate %q denied capabilities", ident.Name, delegate.Identity), delegate.DenyCapabilities); err != nil {
				return err
			}
		}
		if ident.Home != "" {
			if _, ok := auth.Docsets[ident.Home]; !ok {
				return fmt.Errorf("identity %q references unknown home docset %q", ident.Name, ident.Home)
			}
			if other, ok := homes[ident.Home]; ok {
				return fmt.Errorf("identities %q and %q share home docset %q", other, ident.Name, ident.Home)
			}
			homes[ident.Home] = ident.Name
		}
	}
	for _, ident := range auth.Identities {
		for _, delegate := range ident.Delegates {
			if !identities[delegate.Identity] {
				return fmt.Errorf("identity %q references unknown delegate identity %q", ident.Name, delegate.Identity)
			}
		}
	}
	for docset, ds := range auth.Docsets {
		for role, grant := range ds.Access.Allow {
			if grant == "" || strings.TrimSpace(grant) != grant {
				return fmt.Errorf("docset %q has invalid grant for role %q", docset, role)
			}
			if role != "guest" {
				if _, ok := auth.Roles[role]; !ok {
					return fmt.Errorf("docset %q references unknown role %q", docset, role)
				}
			}
		}
		if err := validateNames(fmt.Sprintf("docset %q deny roles", docset), ds.Access.Deny); err != nil {
			return err
		}
		for _, role := range ds.Access.Deny {
			if role != "guest" {
				if _, ok := auth.Roles[role]; !ok {
					return fmt.Errorf("docset %q denies unknown role %q", docset, role)
				}
			}
		}
	}
	for _, ident := range auth.Identities {
		if ident.Home != "" {
			ds := auth.Docsets[ident.Home]
			for _, denied := range ds.Access.Deny {
				for _, role := range ident.Roles {
					if denied == role {
						return fmt.Errorf("identity %q role %q is denied on its home %q", ident.Name, role, ident.Home)
					}
				}
			}
		}
	}

	return nil
}
