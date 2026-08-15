package cmds

import (
	"io"
	"time"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// CmdContext provides the interface that commands use to interact with the shell.
// Implemented by shell.Shell.
type CmdContext interface {
	FS() vfs.FileSystem
	Cwd() string
	SetCwd(dir string)
	Resolve(path string) string
	GetEnv(key string) string
	SetEnv(key, value string)
	DeleteEnv(key string)
	AllEnv() map[string]string
	Exec(cmdLine string, w io.Writer, errW io.Writer, stdin io.Reader) int
	ExecPipeline(line string, w io.Writer, errW io.Writer, stdin io.Reader) int
	// ActionAllowed reports whether the session may perform the given
	// capability class. A command that introspects the available surface
	// (e.g. help) uses this to hide actions the session is not allowed to use.
	ActionAllowed(a Action) bool
	// WriteConflictPolicy reports the policy governing whole-file overwrites to
	// the given resolved path. Defaults to vfs.PolicyHash (compare-and-swap)
	// when the host has not configured a resolver.
	WriteConflictPolicy(resolvedPath string) vfs.WriteConflictPolicy
	// Docsets reports the docsets this session can access, with their display
	// paths, direct writability, and attributes. Used by `lore docsets`. The
	// host (server) computes this once per session; a standalone shell returns
	// nil.
	Docsets() []DocsetInfo
	// PublishTargets reports the publish inboxes this session may publish to,
	// with their per-docset size caps. Used by `publish`. The host computes this
	// once per session; a standalone shell returns nil.
	PublishTargets() []PublishTarget
	// MetaExtenders reports the plugin-contributed extenders that enrich `lore
	// meta` records. The host installs these once per session; a standalone
	// shell returns nil.
	MetaExtenders() []meta.Extender
	MetaFilters() []meta.Filter
	// Validators reports plugin-contributed checks used by `lore validate`.
	Validators() []validation.Validator
	SkillsManagementEnabled() bool
	SkillsRemoteTimeout() time.Duration
	SkillsRemoteMaxBytes() int64
}

type unsupportedFlagReporter interface {
	ReportUnsupportedFlag(command, flag string)
}

// ReportUnsupportedFlag records a flag the command parser does not implement.
// The optional interface keeps commands usable with lightweight CmdContext
// implementations that do not collect telemetry.
func ReportUnsupportedFlag(ctx CmdContext, command, flag string) {
	if reporter, ok := ctx.(unsupportedFlagReporter); ok {
		reporter.ReportUnsupportedFlag(command, flag)
	}
}

// DocsetInfo describes one docset a session can access. It is the per-session
// view surfaced by `lore docsets` — the host resolves it from the session's
// role-policy snapshot.
type DocsetInfo struct {
	// Name is the docset's logical name (its key in the auth config).
	Name string
	// Paths contains the single display path represented by this row. It remains
	// a slice so existing command consumers can parse canonical rows unchanged.
	Paths []string
	// AliasTarget is the canonical display path when this row represents an
	// alias. Empty means the row is canonical.
	AliasTarget string
	// Grant is retained for standalone callers that provide one grant directly.
	Grant string
	// Grants are the effective grants contributed by the session's roles.
	Grants []string
	// Writable reports whether this session may write to the docset directly
	// with the normal write verbs. This is the FS-authoritative answer.
	Writable bool
	// Home reports whether this docset is the session's home docset ($HOME).
	Home bool
	// Inbox reports whether the docset declares an inbox folder (used by the
	// publish grant). It says nothing about the inbox path or size — that lives
	// in PublishTarget.
	Inbox       bool
	AgentSkills bool
}

// PublishTarget is a writable inbox: its logical docset name (the first path
// segment used in `publish /<name>/<file>`), the resolved virtual filesystem
// path of the docset's inbox that a publish is routed into, and the per-docset
// size cap. A `publish /<name>/<rest>` writes to InboxPath/<rest>, not to the
// docset root — the inbox is the only place the publish grant may create files.
type PublishTarget struct {
	Name        string
	InboxPath   string
	MaxFileSize int64
}
