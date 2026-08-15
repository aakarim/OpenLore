package openlore

import (
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// FileSystem is the read-only filesystem interface used by OpenLore.
type FileSystem = vfs.FileSystem

// Option is a functional option for configuring the server.
type Option = config.Option

// FilesConfig controls which files are served.
type FilesConfig = config.FilesConfig

// AuthConfig is loaded from auth.json.
type AuthConfig = config.AuthConfig

// DocsetSpec defines a named set of path mappings for a docset.
type DocsetSpec = config.DocsetSpec

// PathMapping represents a path entry.
type PathMapping = config.PathMapping

// AuthIdentity defines a user identity with access to a lore spec.
type AuthIdentity = config.AuthIdentity

// PasskeysConfig holds WebAuthn passkey configuration.
type PasskeysConfig = config.PasskeysConfig

// Configuration options re-exported for external consumers.
var (
	WithConfigFile      = config.WithConfigFile
	WithEmbeddedConfig  = config.WithEmbeddedConfig
	WithPort            = config.WithPort
	WithMetricsPort     = config.WithMetricsPort
	WithHostKeyPath     = config.WithHostKeyPath
	WithAllowKeyless    = config.WithAllowKeyless
	WithDefaultCwd      = config.WithDefaultCwd
	WithMOTD            = config.WithMOTD
	WithMOTDFile        = config.WithMOTDFile
	WithAuthFile        = config.WithAuthFile
	WithAllowedPatterns = config.WithAllowedPatterns
	WithIgnorePatterns  = config.WithIgnorePatterns
	WithLogger          = config.WithLogger
	WithSkillsDir       = config.WithSkillsDir
	WithWritableDir     = config.WithWritableDir
	WithHTTPPort        = config.WithHTTPPort
	WithMCPPath         = config.WithMCPPath
	WithMCPEnabled      = config.WithMCPEnabled
	WithTLS             = config.WithTLS
	WithCAKeysFile      = config.WithCAKeysFile
	WithHostCertFile    = config.WithHostCertFile
	WithPasskeys        = config.WithPasskeys
	WithReadonly        = config.WithReadonly
	LoadAuthConfig      = config.LoadAuthConfig
	ValidateAuthConfig  = config.ValidateAuthConfig
)
