package openlore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aakarim/go-openlore/assets"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/httpserver"
	"github.com/aakarim/go-openlore/internal/legal"
	"github.com/aakarim/go-openlore/internal/metrics"
	"github.com/aakarim/go-openlore/internal/passkeys"
	"github.com/aakarim/go-openlore/internal/skills"
	"github.com/aakarim/go-openlore/internal/webstyle"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/logging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// SessionFSFn returns the filesystem to use for a given SSH session identity.
// The default implementation returns the base FS unchanged.
type SessionFSFn func(id Identity, base vfs.FileSystem) vfs.FileSystem

// Server is the main OpenLore SSH server.
type Server struct {
	config config.Config
	// auth is always non-nil so downstream code never nil-checks it. When
	// authEnforced is false, auth is an empty policy and the server runs in
	// trusted/unrestricted mode (local `openlore .`, or an embedded server such
	// as knowledge-backend that does its own scoping): callers get full access.
	// When true, an access-control policy was loaded and identity scoping is
	// enforced (docsets filtered, anonymous is read-only).
	auth         *config.AuthConfig
	authMu       sync.RWMutex
	runtimeAuth  atomic.Pointer[config.AuthConfig]
	authEnforced bool
	// grants is the registry of grant types (ro/rw + plugin-contributed like
	// publish). A grant name in lore.json with no registered type fails startup.
	grants   *grantRegistry
	fs       vfs.FileSystem
	merge    *MergeFS
	metrics  *metrics.Metrics
	srv      *ssh.Server
	httpSrv  *httpserver.Server
	passkeys *passkeys.Passkeys
	logger   *slog.Logger
	motd     string

	onConnect    OnConnectFunc
	onDisconnect OnDisconnectFunc
	sessionFSFn  SessionFSFn

	// jobs runs async external work (Part D), surfaced read-only at /jobs.
	jobs        *JobManager
	historyPath string

	// writeLog is the single ordered write log + serialized applier: the sole
	// writer to the substrate. Non-nil only when the substrate is writable
	// (readonly=false). Every session's mutations funnel through it, so writes,
	// directory creation, and removals are globally ordered. Closed on Shutdown.
	writeLog *writeLog

	// writeMW is the admission (pre-commit) middleware contributed by plugins,
	// composed after the fixed scope layer and before the log in registration
	// order. Empty in core go-openlore (the mechanism carries no policy).
	writeMW []WriteMiddleware

	// readMW is the read (before-read) middleware contributed by plugins, run in
	// front of Stat/ReadDir/ReadFile in registration order. Empty in core
	// go-openlore; when empty no read wrapper is installed (zero read overhead).
	readMW            []ReadMiddleware
	contentTransforms []ContentTransform
	agentSkills       *agentSkillsPlugin

	// metaExtenders are the `lore meta` extenders contributed by plugins,
	// installed per session in buildSessionShell.
	metaExtenders []meta.Extender
	metaFilters   []meta.Filter
	validators    []validation.Validator

	// postCommitMW is the post-commit middleware contributed by plugins, run at
	// the applier after a durable commit (feed emit, post_write hooks) in
	// registration order. Empty in core go-openlore.
	postCommitMW []PostCommitMiddleware
	httpRoutes   []HTTPRouteProvider

	// Bearer-token auth for the MCP + HTTP API (docs/mcp-bearer-auth.md).
	// identityStore is always set (resolves claims → Identity). The rest are
	// non-nil only when auth.tokens is configured, which enables token auth.
	identityStore      IdentityStore
	authorizationStore AuthorizationStore
	issuer             Issuer
	refreshStore       RefreshTokenStore
	clientStore        ClientStore
	cimdResolver       CIMDResolver
	clientAuth         ClientAuthenticator
	authCodes          *authCodeStore
	authorizeReqs      *authorizeStore
	consents           *consentStore
	tokens             *tokenEndpoint
	// oidc verifies external IdP assertions for the jwt-bearer (WIF) grant. It
	// is non-nil only when oidc_issuers are configured alongside auth.tokens.
	oidc  OIDCVerifier
	audit AuditLog
}

// NewServer creates a new OpenLore SSH server.
// rootDir is the primary directory to serve (can be empty if using Mount).
// Options are applied using the functional options pattern via config.Option.
func NewServer(rootDir string, opts ...config.Option) (*Server, error) {
	return newServerWithRoot(rootDir, nil, nil, opts...)
}

// NewServerWithRootFS creates a server whose root filesystem is a caller-supplied
// vfs.FileSystem, set BEFORE the writable substrate and the ordered write log are
// established. Use this (instead of NewServer + SetRootBashFS) when the root is a
// custom writable backend and writes should flow through the ordered log: a late
// SetRootBashFS runs after SetWriteable()/newWriteLog and would leave the log
// with no writable backend at construction time.
func NewServerWithRootFS(root vfs.FileSystem, opts ...config.Option) (*Server, error) {
	return newServerWithRoot("", root, nil, opts...)
}

// NewServerWithLowerFS creates a server with a read-only lower filesystem.
// When writable_dir is configured, its disk tree is layered over lower at the
// same virtual root and receives all writes.
func NewServerWithLowerFS(lower fs.FS, opts ...config.Option) (*Server, error) {
	return newServerWithRoot("", nil, NewFSAdapter(lower), opts...)
}

// newServerWithRoot is the shared constructor. When rootFS is non-nil it becomes
// the merge root (rootDir is ignored); otherwise rootDir (if non-empty) is served
// via a DirFS. The root is installed before the writable block so the write log's
// substrate is live at construction.
func newServerWithRoot(rootDir string, rootFS, lowerFS vfs.FileSystem, opts ...config.Option) (*Server, error) {
	cfg, err := config.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	logger := cfg.Logger
	if logger == nil {
		if cfg.Debug {
			logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
		} else {
			logger = slog.Default()
		}
	}
	for _, warning := range cfg.Warnings() {
		logger.Warn("ignoring invalid configuration value", "error", warning)
	}

	s := &Server{
		config: cfg,
		// Default to an empty (unenforced) policy; a loaded auth file replaces
		// it and sets authEnforced below.
		auth:    &config.AuthConfig{},
		grants:  newGrantRegistry(),
		merge:   NewMergeFS(),
		metrics: &metrics.Metrics{},
		logger:  logger,
		motd:    cfg.MOTD,
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = ".openlore"
	}
	s.audit = NewJSONLAuditLog(filepath.Join(dataDir, "audit", "events.jsonl"))

	// Construct shellexec now, but register built-ins after auth/docsets are
	// loaded so Agent Skills admission can be the outermost write middleware.
	var shellPlugin *shellexecPlugin
	if !cfg.Shellexec.IsEmpty() {
		plug, err := newShellexec(cfg.Shellexec, cfg.DataDir, ShellRunner{}, logger)
		if err != nil {
			return nil, fmt.Errorf("configuring shellexec plugin: %w", err)
		}
		shellPlugin = plug
	}

	// Load auth config
	if cfg.AuthFile != "" {
		auth, err := config.LoadAuthConfig(cfg.AuthFile)
		if err != nil {
			return nil, fmt.Errorf("loading auth config: %w", err)
		}
		s.auth = auth
		s.authorizationStore = fileAuthorizationStore{auth: auth}
		s.authEnforced = true

		// Auth policy fields override config defaults
		if auth.AllowKeyless != nil {
			s.config.AllowKeyless = *auth.AllowKeyless
		}
		if auth.UnknownIdentity != "" {
			s.config.UnknownIdentity = auth.UnknownIdentity
		}
		if auth.DefaultCwd != "" {
			s.config.DefaultCwd = auth.DefaultCwd
		}
	} else {
		// No auth file: run in trusted/unenforced mode as a single `public`
		// docset rooted at "/". Unenforced sessions receive synthetic `rw`
		// authority on it, so every consumer reuses normal docset scoping.
		s.auth.Docsets = map[string]config.DocsetSpec{"public": {
			Paths: []config.PathMapping{{Source: "/", Display: "/"}},
		}}
	}

	// Collect docset root display paths so DirFS.Mkdir can enforce the
	// "strictly below a docset root" boundary.
	var docsetRoots []string
	for _, ds := range s.auth.Docsets {
		for _, pm := range ds.Paths {
			display := pm.Display
			if display == "" {
				display = pm.Source
			}
			docsetRoots = append(docsetRoots, display)
		}
	}

	// Built-in OKF plugin: validates Open Knowledge Format documents on write
	// (pre-commit admission middleware). Registered here — after docsets are
	// resolved (auth config or the unenforced-mode public docset) but before the
	// write log is built — because it resolves the effective OKF config per write
	// from the docset that owns the target path. Only registered when at least
	// one docset carries OKF config.
	var agentSkills *agentSkillsPlugin
	if cfg.Plugins.Skills.Enabled {
		agentSkills = newAgentSkills(s.auth.Docsets, s.merge, s.canonicalPath, logger, cfg.Plugins.Skills)
		s.agentSkills = agentSkills
		if err := s.registerPlugin(agentSkills); err != nil {
			return nil, err
		}
	}
	rulesPlugin, err := newRulesPlugin(s.auth, rules.Defaults{Growth: cfg.Rules.Growth}, s.merge, logger)
	if err != nil {
		return nil, err
	}
	if err := s.registerPlugin(rulesPlugin); err != nil {
		return nil, err
	}
	// OKF metadata remains owned by the existing adapter; admission and
	// validation are provided exclusively by the rules engine above.
	if anyDocsetHasOKF(s.auth.Docsets) {
		s.metaExtenders = append(s.metaExtenders, newOKF(s.auth.Docsets, logger).MetaExtenders()...)
	}
	if shellPlugin != nil {
		if err := s.registerPlugin(shellPlugin); err != nil {
			return nil, err
		}
	}

	// Set up root directory. A caller-supplied rootFS wins (installed before the
	// writable block so the write log has a live substrate); otherwise serve
	// rootDir via a DirFS.
	if rootFS != nil {
		s.merge.SetRoot(rootFS)
	} else if rootDir != "" {
		dirFS := NewDirFS(rootDir, cfg.Files).WithDocsetRoots(docsetRoots)
		s.merge.SetRoot(dirFS)
	} else if cfg.WritableDir != "" {
		info, err := os.Stat(cfg.WritableDir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("writable_dir %q is not a directory", cfg.WritableDir)
		}
		upper := NewDirFS(cfg.WritableDir, cfg.Files).WithDocsetRoots(docsetRoots)
		if lowerFS != nil {
			s.merge.SetRoot(NewOverlayFS(upper, lowerFS))
		} else {
			s.merge.SetRoot(upper)
		}
	} else if lowerFS != nil {
		s.merge.SetRoot(lowerFS)
	}

	// Enable the experimental writable substrate when the global lock is open.
	// Fail fast if writes were requested but no backend can support them.
	if !cfg.Readonly {
		if err := s.merge.SetWriteable(); err != nil {
			return nil, fmt.Errorf("enabling writable mode (readonly=false): %w", err)
		}
		logger.Info("writable substrate enabled (readonly=false)")

		// The single ordered write log + serialized applier over the writable
		// substrate. Every session routes its mutations here (via middlewareFS),
		// so it is the sole writer and gives globally ordered writes/removes. The
		// applier runs the post-commit chain after each durable commit.
		s.writeLog = newWriteLog(s.merge, s.postCommitChain(), logger, 0)
		s.historyPath = filepath.Join(dataDir, "history", "commits.jsonl")
		s.writeLog.SetCommitRecorder(NewJSONLCommitRecorder(s.historyPath))
		if agentSkills != nil {
			agentSkills.submit = func(ctx context.Context, cs vfs.ChangeSet) error {
				_, err := s.CommitChangeSet(ctx, Attribution{Principal: "agent_skills_remote", internal: true}, cs)
				return err
			}
			s.writeLog.SetPreApply(agentSkills.validateMutation)
		}

		// Async external work (Part D): the `spawn` command runs a command in a
		// bounded goroutine and writes its stdout back through the captured
		// scoped FS. Only meaningful when writes are possible. Jobs are in-memory
		// (lost on restart) and surfaced read-only at /jobs.
		s.jobs = NewJobManager(cfg.MaxJobs, ShellRunner{}, logger)
		s.merge.MountSystem("jobs", NewJobsFS(s.jobs))
	}

	// Load skills
	skillReg := skills.NewRegistry()

	// Load embedded skills
	if embSkills := assets.Skills(); embSkills != nil {
		if err := skillReg.LoadFromFS(embSkills); err != nil {
			return nil, fmt.Errorf("loading embedded skills: %w", err)
		}
	}

	// Load runtime skills from directory
	if cfg.SkillsDir != "" {
		if err := skillReg.LoadFromDir(cfg.SkillsDir); err != nil {
			return nil, fmt.Errorf("loading skills from %s: %w", cfg.SkillsDir, err)
		}
	}

	// Register skills as shell commands
	for name, skill := range skillReg.All() {
		cmds.RegisterSkill(name, skill.Description, skill.Content)
	}

	s.fs = s.merge

	// Set up passkeys if enabled
	if cfg.Passkeys.Enabled {
		pkFile := cfg.Passkeys.PasskeysFile
		if pkFile == "" {
			pkFile = "./config/passkeys.json"
		}
		rpName := cfg.Passkeys.RPName
		if rpName == "" {
			rpName = "OpenLore"
		}
		sessionTTL := 24 * time.Hour
		if cfg.Passkeys.SessionTTL != "" {
			if d, err := time.ParseDuration(cfg.Passkeys.SessionTTL); err == nil {
				sessionTTL = d
			}
		}

		// Read host key material for session signing
		sessionKey := []byte("openlore-default-session-key")
		if keyData, err := os.ReadFile(cfg.HostKeyPath); err == nil {
			sessionKey = keyData
		}

		pk, err := passkeys.New(passkeys.Config{
			Enabled:      true,
			RPID:         cfg.Passkeys.RPID,
			RPName:       rpName,
			RPOrigins:    cfg.Passkeys.RPOrigins,
			LorePath:     cfg.Passkeys.LorePath,
			PasskeysFile: pkFile,
			SessionTTL:   sessionTTL,
		}, sessionKey, logger)
		if err != nil {
			return nil, fmt.Errorf("setting up passkeys: %w", err)
		}
		s.passkeys = pk

		pk.SetAuthConfig(s.auth)
	}

	if err := s.initAuth(); err != nil {
		return nil, fmt.Errorf("setting up token auth: %w", err)
	}

	return s, nil
}

func (s *Server) currentAuth() *config.AuthConfig {
	if auth := s.runtimeAuth.Load(); auth != nil {
		return auth
	}
	return s.auth
}

// Config returns the resolved configuration.
func (s *Server) Config() config.Config {
	return s.config
}

// Mount adds a named filesystem mount point using a vfs.FileSystem.
func (s *Server) Mount(name string, fs vfs.FileSystem) {
	s.merge.Mount(name, fs)
}

// MountFS adds a named filesystem mount point using a standard fs.FS.
func (s *Server) MountFS(name string, fsys fs.FS) {
	s.merge.Mount(name, NewFSAdapter(fsys))
}

// SetRootFS sets the root filesystem using a standard fs.FS.
func (s *Server) SetRootFS(fsys fs.FS) {
	s.merge.SetRoot(NewFSAdapter(fsys))
}

// SetRootBashFS sets the root filesystem using a vfs.FileSystem. Paths
// that don't match any mount fall through to this filesystem.
func (s *Server) SetRootBashFS(fsys vfs.FileSystem) {
	s.merge.SetRoot(fsys)
}

// SetSessionFSFn registers a per-session filesystem decorator. When set,
// the server calls fn(identity, baseFS) for each new SSH session and uses
// the returned filesystem for that session's shell.
func (s *Server) SetSessionFSFn(fn SessionFSFn) {
	s.sessionFSFn = fn
}

func (s *Server) advertisedSSHPort() int {
	if s.config.ExternalSSHPort != 0 {
		return s.config.ExternalSSHPort
	}
	return s.config.Port
}

func publicKeyMatches(configured string, presented gossh.PublicKey) bool {
	parsed, _, _, _, err := gossh.ParseAuthorizedKey([]byte(configured))
	return err == nil && bytes.Equal(parsed.Marshal(), presented.Marshal())
}

// OnConnect registers a callback for new connections.
func (s *Server) OnConnect(fn OnConnectFunc) {
	s.onConnect = fn
}

// OnDisconnect registers a callback for disconnections.
func (s *Server) OnDisconnect(fn OnDisconnectFunc) {
	s.onDisconnect = fn
}

// FileSystem returns the server's filesystem.
func (s *Server) FileSystem() vfs.FileSystem {
	return s.fs
}

func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) resolveIdentity(sess ssh.Session) Identity {
	conn := Identity{
		RemoteAddr:  sess.RemoteAddr().String(),
		User:        sess.User(),
		PublicKey:   sess.PublicKey(),
		SessionID:   generateSessionID(),
		ConnectedAt: time.Now(),
	}

	if !s.authEnforced {
		// Auth not enforced: full access to the synthetic `public` docset.
		return withConn(conn, s.anonymousIdentity())
	}

	if conn.PublicKey != nil {
		// Extract the underlying public key for matching. If the client
		// presented a certificate, sess.PublicKey() returns the certificate
		// itself — we need the inner key to match against raw public keys stored
		// in lore.json identities.
		matchKey := conn.PublicKey
		var cert *gossh.Certificate
		if c, ok := conn.PublicKey.(*gossh.Certificate); ok {
			cert = c
			matchKey = c.Key
		}
		// First: match by public key. An authenticated key holds the identity's
		// full authority (shared with token resolution).
		for _, ident := range s.currentAuth().Identities {
			if publicKeyMatches(ident.PublicKey, matchKey) {
				return withConn(conn, s.identityFromAuth(ident))
			}
		}

		// Second: a certificate's principals name identities directly. This lets
		// you sign certs with `-n alfie` and have them get alfie's grants without
		// registering individual keys.
		if cert != nil {
			for _, principal := range cert.ValidPrincipals {
				if src, ok := s.identityForName(principal); ok {
					return withConn(conn, src)
				}
			}
		}
	}

	// Unrecognized key / keyless: the reserved guest identity.
	return withConn(conn, s.anonymousIdentity())
}

// withConn copies the per-connection fields (remote addr, user, public key)
// from a freshly built connection identity onto a resolved identity, so the
// resolved authority keeps its live connection context.
func withConn(conn, resolved Identity) Identity {
	resolved.RemoteAddr = conn.RemoteAddr
	resolved.User = conn.User
	resolved.PublicKey = conn.PublicKey
	resolved.Principal.Source = "ssh"
	resolved.Principal.Subject = resolved.IdentityName
	resolved.Principal.Claims = map[string]any{"user": conn.User, "remote_addr": conn.RemoteAddr}
	if conn.PublicKey != nil {
		resolved.Principal.Claims["public_key_fingerprint"] = gossh.FingerprintSHA256(conn.PublicKey)
		if cert, ok := conn.PublicKey.(*gossh.Certificate); ok {
			resolved.Principal.Claims["certificate_serial"] = cert.Serial
			resolved.Principal.Claims["certificate_principals"] = append([]string(nil), cert.ValidPrincipals...)
		}
	}
	return resolved
}

// resolveHomeDir returns the display path of the named home docset, used as
// the session's $HOME and initial working directory. It uses the first path
// mapping of the docset (its Display, falling back to Source). Returns "" when
// no home is set or the docset has no paths.
func (s *Server) resolveHomeDir(homeDocset string) string {
	if homeDocset == "" {
		return ""
	}
	ds, ok := s.currentAuth().Docsets[homeDocset]
	if !ok || len(ds.Paths) == 0 {
		return ""
	}
	pm := ds.Paths[0]
	display := pm.Display
	if display == "" {
		display = pm.Source
	}
	return vfs.CleanPath(display)
}

// writeConflictPolicy resolves the policy governing whole-file overwrites to
// virtual path p. A per-docset override (DocsetSpec.WriteConflictPolicy) wins
// for paths inside that docset; otherwise the global default applies. An
// invalid per-docset value is ignored in favor of the global default.
func (s *Server) writeConflictPolicy(p string) vfs.WriteConflictPolicy {
	global := s.config.WriteConflictPolicy
	if global == "" {
		global = vfs.DefaultWriteConflictPolicy
	}
	clean := s.canonicalPath(p)
	for _, ds := range s.currentAuth().Docsets {
		if ds.WriteConflictPolicy == "" {
			continue
		}
		for _, pm := range ds.Paths {
			display := pm.Display
			if display == "" {
				display = pm.Source
			}
			if pathWithinRoot(vfs.CleanPath(display), clean) {
				if parsed, err := vfs.ParseWriteConflictPolicy(ds.WriteConflictPolicy); err == nil {
					return parsed
				}
				return global
			}
		}
	}
	return global
}

// pathWithinRoot reports whether p is the docset root itself or sits beneath it.
// A root of "/" contains every path.
func pathWithinRoot(root, p string) bool {
	if root == "/" {
		return true
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

func (s *Server) sftpSubsystem(sess ssh.Session) {
	// SFTP must enforce the same read scoping and write authorization as the
	// interactive shell. Resolve the session identity and build the identical
	// layered session FS (scoped reads + per-op write authz) rather than the raw
	// merge FS, so an accepted SSH user cannot read, list, stat, or write
	// outside their grants over SFTP.
	id := s.resolveIdentity(sess)
	handler := NewSFTPHandler(s.buildSessionFS(id))
	server := sftp.NewRequestServer(sess, sftp.Handlers{
		FileGet:  handler,
		FilePut:  handler,
		FileCmd:  handler,
		FileList: handler,
	})
	if err := server.Serve(); err != nil && err.Error() != "EOF" {
		s.logger.Error("sftp server error", "error", err)
	}
}

func (s *Server) shellHandler(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		id := s.resolveIdentity(sess)

		fingerprint := "none"
		if id.PublicKey != nil {
			fingerprint = gossh.FingerprintSHA256(id.PublicKey)
		}

		s.logger.Info("session started",
			"remote_addr", id.RemoteAddr,
			"user", id.User,
			"pubkey_fingerprint", fingerprint,
			"session_id", id.SessionID,
		)

		s.metrics.ActiveSessions.Add(1)
		s.metrics.TotalSessions.Add(1)

		if s.onConnect != nil {
			s.onConnect(id)
		}

		defer func() {
			s.metrics.ActiveSessions.Add(-1)
			s.logger.Info("session ended",
				"remote_addr", id.RemoteAddr,
				"user", id.User,
				"session_id", id.SessionID,
			)
			if s.onDisconnect != nil {
				s.onDisconnect(id)
			}
		}()

		// Store the resolved identity on the session context so the shell is
		// built from the same context-carried identity as MCP/HTTP callers —
		// one identity resolution model, one scoping path across transports.
		ctx := contextWithIdentity(sess.Context(), id)
		sh := s.shellForContext(ctx)

		// Execute the original SSH command string. Session.Command tokenizes it
		// first and cannot faithfully reconstruct shell operators such as output
		// redirection, especially when an operator is adjacent to another word.
		if cmdLine := sess.RawCommand(); cmdLine != "" {
			s.metrics.TotalCommands.Add(1)
			exitCode := sh.ExecPipeline(cmdLine, sess, sess.Stderr(), sess)
			sess.Exit(exitCode)
			return
		}

		interactiveOpts := shell.InteractiveOptions{Width: 80}
		if pty, windowChanges, ok := sess.Pty(); ok {
			interactiveOpts.Width = pty.Window.Width
			widthChanges := make(chan int, 1)
			interactiveOpts.WidthChanges = widthChanges
			go func() {
				defer close(widthChanges)
				for {
					select {
					case window, ok := <-windowChanges:
						if !ok {
							return
						}
						select {
						case widthChanges <- window.Width:
						default:
							select {
							case <-widthChanges:
							default:
							}
							select {
							case widthChanges <- window.Width:
							default:
							}
						}
					case <-sess.Context().Done():
						return
					}
				}
			}()
		}
		sh.RunInteractiveWithOptions(sess, sess, s.motd, "lore", interactiveOpts)
		sess.Exit(0)
	}
}

// RegisterPlugin wires a plugin's middleware into the admission, read, and
// post-commit chains, in registration order. It is the exported seam for
// consumers (e.g. the knowledge-backend approvals plugin) to contribute
// middleware after NewServer returns.
//
// Admission (write) and read middleware take effect for every session created
// afterward, because those chains are composed per-session in buildSessionShell.
// Post-commit middleware is refreshed onto the running write log here, so a
// post-commit provider registered after construction still fires on commits.
//
// Call it before serving; it is not safe to call concurrently with live traffic.
func (s *Server) RegisterPlugin(p any) error {
	if err := s.registerPlugin(p); err != nil {
		return err
	}
	if s.writeLog != nil {
		s.writeLog.SetPostCommit(s.postCommitChain())
	}
	return nil
}

// CommitChangeSet appends an already-authorized ChangeSet directly to the ordered
// log, skipping the admission chain but still running the serialized applier
// (compare-and-swap against current state) and the post-commit chain. It is how a
// consumer commits a previously-deferred change after human approval: the change
// already passed admission when it was first parked, so re-running admission would
// let the approval middleware defer it again in an infinite loop.
//
// It returns the committed hash (empty for non-write actions) or a CAS/commit
// error (*vfs.PreconditionError / *vfs.TreeStaleError on drift). If the substrate
// is read-only (no write log), it returns vfs.ErrReadOnly.
func (s *Server) CommitChangeSet(ctx context.Context, attribution Attribution, cs vfs.ChangeSet) (WriteResult, error) {
	if s.writeLog == nil {
		return WriteResult{}, vfs.ErrReadOnly
	}
	cs = s.canonicalChangeSet(cs)
	h, err := s.writeLog.Submit(ctx, attribution, cs)
	return WriteResult{Hash: h}, err
}

func (s *Server) AdmitChangeSet(ctx context.Context, id Identity, cs vfs.ChangeSet) (WriteResult, error) {
	if s.writeLog == nil {
		return WriteResult{}, vfs.ErrReadOnly
	}
	cs = s.canonicalChangeSet(cs)
	if err := vfs.ValidateChangeSet(cs); err != nil {
		return WriteResult{}, err
	}
	for _, change := range cs.Leaves() {
		if !s.identityCanWrite(id, change.Action, change.Target) {
			return WriteResult{}, mutationDeniedError(s.merge, change.Action, change.Target)
		}
	}
	return s.writeChain()(ctx, NewWriteOp(id.attribution(), cs))
}

type HTTPRouteProvider interface {
	PrepareHTTPRoutes(*Server) (HTTPRouteRegistrar, error)
}
type HTTPRouteRegistrar func(*http.ServeMux)
type routeExtender struct{ register HTTPRouteRegistrar }

func (e routeExtender) RegisterHTTPHandlers(mux *http.ServeMux) { e.register(mux) }

func (s *Server) canonicalChangeSet(cs vfs.ChangeSet) vfs.ChangeSet {
	if len(cs.Changes) == 0 {
		cs.Target = s.canonicalPath(cs.Target)
		if cs.RemoveAll != nil && cs.RemoveAll.Opts.Expected != nil {
			remove := *cs.RemoveAll
			remove.Opts.Expected = copyCanonicalSnapshot(remove.Opts.Expected, s.canonicalPath)
			cs.RemoveAll = &remove
		}
	}
	for i := range cs.Changes {
		cs.Changes[i].Target = s.canonicalPath(cs.Changes[i].Target)
		if cs.Changes[i].RemoveAll != nil && cs.Changes[i].RemoveAll.Opts.Expected != nil {
			remove := *cs.Changes[i].RemoveAll
			remove.Opts.Expected = copyCanonicalSnapshot(remove.Opts.Expected, s.canonicalPath)
			cs.Changes[i].RemoveAll = &remove
		}
	}
	return cs
}

// registerPlugin wires any middleware a plugin provides into the admission,
// read, and post-commit chains, in registration order. Must be called during
// NewServer before the write log is built: read/write middleware are read
// per-session at buildSessionShell time, but the post-commit chain is composed
// once when newWriteLog is constructed.
func (s *Server) registerPlugin(p any) error {
	if fp, ok := p.(MetaFilterProvider); ok {
		claimed := map[string]bool{}
		for _, existing := range s.metaFilters {
			claimed[existing.Name] = true
			for _, alias := range existing.Aliases {
				claimed[alias] = true
			}
		}
		for _, f := range fp.MetaFilters() {
			for _, name := range append([]string{f.Name}, f.Aliases...) {
				if name == "" || claimed[name] {
					return fmt.Errorf("metadata filter name or alias %q is already registered", name)
				}
				claimed[name] = true
			}
		}
	}
	if wp, ok := p.(WriteMiddlewareProvider); ok {
		s.writeMW = append(s.writeMW, wp.WriteMiddleware()...)
	}
	if rp, ok := p.(ReadMiddlewareProvider); ok {
		s.readMW = append(s.readMW, rp.ReadMiddleware()...)
	}
	if tp, ok := p.(ContentTransformProvider); ok {
		s.contentTransforms = append(s.contentTransforms, tp.ContentTransforms()...)
	}
	if pc, ok := p.(PostCommitProvider); ok {
		s.postCommitMW = append(s.postCommitMW, pc.PostCommitMiddleware()...)
	}
	if gp, ok := p.(GrantTypeProvider); ok {
		for _, g := range gp.GrantTypes() {
			s.grants.register(g)
		}
	}
	if mp, ok := p.(MetaExtenderProvider); ok {
		s.metaExtenders = append(s.metaExtenders, mp.MetaExtenders()...)
	}
	if fp, ok := p.(MetaFilterProvider); ok {
		s.metaFilters = append(s.metaFilters, fp.MetaFilters()...)
	}
	if vp, ok := p.(ValidatorProvider); ok {
		s.validators = append(s.validators, vp.Validators()...)
	}
	// Record the plugin's identity + version in the boot logs. Logged per
	// registration so it captures plugins registered after NewServer (e.g. the
	// inbox plugin, wired by the CLI via RegisterPlugin) too.
	if ip, ok := p.(PluginInfoProvider); ok && s.logger != nil {
		info := ip.Info()
		s.logger.Info("plugin registered", "name", info.Name, "version", info.Version)
	}
	if hp, ok := p.(HTTPRouteProvider); ok {
		s.httpRoutes = append(s.httpRoutes, hp)
	}
	return nil
}

// writeChain composes the admission (pre-commit) middleware around a terminal
// handler that submits the ChangeSet to the global log and awaits the committed
// hash. Plugin middleware (s.writeMW) runs in registration order before the log;
// a middleware may allow (call next), defer (return *vfs.PendingChangeError), or
// reject (return an error). Core go-openlore registers no middleware, so the
// chain is just the terminal submit.
func (s *Server) writeChain() WriteHandler {
	terminal := func(ctx context.Context, op WriteOp) (WriteResult, error) {
		h, err := s.writeLog.Submit(ctx, op.Attribution, op.persistenceChangeSet())
		return WriteResult{Hash: h}, err
	}
	return chainWrite(terminal, s.writeMW...)
}

// readChain composes the read (before-read) middleware around a no-op terminal
// (the actual read is performed by readChainFS after the gate passes). Plugin
// middleware runs in registration order; any non-nil error aborts the read.
func (s *Server) readChain() ReadHandler {
	terminal := func(ctx context.Context, op ReadOp) error { return nil }
	return chainRead(terminal, s.readMW...)
}

// postCommitChain composes the post-commit middleware around a no-op terminal.
// It runs at the applier after a durable commit (feed emit, post_write hooks),
// in registration order; failures are logged and the log keeps moving.
func (s *Server) postCommitChain() PostCommitHandler {
	terminal := func(ctx context.Context, info CommitInfo) error { return nil }
	return chainPostCommit(terminal, s.postCommitMW...)
}

// buildSessionShell constructs a fully-configured shell scoped to the given
// identity: a per-identity filesystem (read scoping, write authorization,
// read-tracking CAS), capability-gated allowed actions, and
// OPENLORE_* environment variables. This is the single source of truth for
// per-identity scoping, shared by the SSH shell handler and the MCP/HTTP tool
// handlers so all transports enforce the same access rules.
// buildSessionFS constructs the per-session filesystem for an identity and
// exposes its configured path aliases for filesystem-oriented transports.
func (s *Server) buildSessionFS(id Identity) vfs.FileSystem {
	id = s.resolveSessionIdentity(id)
	sessionFS := s.buildCanonicalSessionFS(id)
	// Expose this identity's aliases outermost so callers can retain the alias
	// spelling while every internal operation, including CAS tracking, sees the
	// canonical path. Ungranted aliases are never added to the session tree.
	if aliases := s.aliasesForIdentity(id); len(aliases) > 0 {
		sessionFS = newAliasFS(sessionFS, aliases)
	}
	return sessionFS
}

// buildCanonicalSessionFS constructs the per-session filesystem without path
// aliases. The web browser uses this view so its folder tree presents each
// docset only once; filesystem-oriented transports decorate it with aliases in
// buildSessionFS.
func (s *Server) buildCanonicalSessionFS(id Identity) vfs.FileSystem {
	id = s.resolveSessionIdentity(id)

	// Build per-session filesystem scoped to the identity's readable roots.
	// Docsets are display-path subtrees of a shared backing filesystem, so read
	// scoping is by path (not mount name): a session only sees the docsets it
	// holds a grant on, plus the ancestor directories leading to them and the
	// always-visible system mounts. Only when auth is enforced; in
	// unenforced/trusted mode the session sees the whole merge FS (all mounts).
	sessionFS := vfs.FileSystem(s.merge)
	if s.authEnforced {
		sessionFS = newScopedReadFS(sessionFS, s.readableRoots(id), s.allDocsetRoots())
	}
	// Read (before-read) gate: run the read middleware chain in front of every
	// Stat/ReadDir/ReadFile so a plugin can (e.g.) refresh the substrate or
	// abort a read. Innermost read wrapper so it fires for every read that
	// reaches storage. Only installed when a plugin registered read middleware.
	if len(s.readMW) > 0 {
		sessionFS = newReadChainFS(sessionFS, id.attribution(), s.readChain())
	}
	// Route this session's mutations through the single global ordered log:
	// every write/mkdir/remove becomes a ChangeSet, runs the admission chain,
	// and is submitted to the serialized applier (the sole substrate writer).
	// Innermost writable wrapper, so the outer layers (scope) can deny or defer
	// a mutation before it ever becomes a log entry.
	if s.writeLog != nil {
		sessionFS = newMiddlewareFS(sessionFS, id.attribution(), s.writeChain())
	}
	// A write-capable session must be a named non-guest identity with full token
	// scope. The per-operation authorizer below resolves current roles and grants;
	// a read-scoped token remains read-only regardless of RBAC authority.
	canWrite := s.authEnforced && id.IdentityName != "" && id.IdentityName != "guest" && scopeGrantsWrite(id.Scopes)
	// Every mutation is checked against current role membership, the governing
	// docset ACL, token scope, and readonly locks. A grant such as `publish` can
	// further restrict actions and paths. Guest/read-only identities receive a
	// nil authorizer and fail closed.
	if s.authEnforced {
		var authz writeAuthorizer
		if canWrite {
			// Writes deliberately resolve current policy on every operation. The
			// session snapshot remains authoritative for its stable read view.
			writeID := id
			writeID.policySnapshot = nil
			authz = func(action vfs.ChangeAction, p string) bool {
				return s.identityCanWrite(writeID, action, p)
			}
		}
		sessionFS = newScopedWriteFS(sessionFS, authz)
	}
	if s.sessionFSFn != nil {
		sessionFS = s.sessionFSFn(id, sessionFS)
	}
	if s.config.AuthFile != "" && id.policySnapshot != nil && scopeGrantsWrite(id.Scopes) && s.hasCapabilityForPolicy(*id.policySnapshot, "lore:config:edit") {
		if writable, ok := sessionFS.(vfs.WritableFS); ok {
			sessionFS = &configViewFS{WritableFS: writable, server: s, identity: id, attribution: id.attribution()}
		}
	}
	// Session CAS: track the hash of every file read (and written) so a
	// later blind overwrite compare-and-swaps against the version the
	// caller last saw, without naming a hash — an overwrite fails if the
	// file changed since it was read. Outside policy layers so it observes all
	// reads, but inside aliasing so both paths share canonical CAS state. Only
	// meaningful (and only added) when the substrate is writable.
	if !s.config.Readonly {
		if w, ok := sessionFS.(vfs.WritableFS); ok {
			sessionFS = newReadTrackingFS(w)
		}
	}
	// Presentation-only transforms must be outside tracking: tracked hashes are
	// hashes of durable bytes, never injected status annotations.
	if len(s.contentTransforms) > 0 {
		if writable, ok := sessionFS.(vfs.WritableFS); ok {
			sessionFS = &writableReadTransformFS{WritableFS: writable, transforms: s.contentTransforms}
		} else {
			sessionFS = &readTransformFS{FileSystem: sessionFS, transforms: s.contentTransforms}
		}
		if s.agentSkills != nil {
			switch transformed := sessionFS.(type) {
			case *writableReadTransformFS:
				transformed.updateRemoteSkill = s.agentSkills.updateRemoteSkill
			case *readTransformFS:
				transformed.updateRemoteSkill = s.agentSkills.updateRemoteSkill
			}
		}
	}
	return sessionFS
}

func (s *Server) resolveSessionIdentity(id Identity) Identity {
	if s.authEnforced && id.policySnapshot == nil {
		if policy, err := s.currentPolicy(id); err == nil {
			id.policySnapshot = &policy
			id.HomeDocset = policy.HomeDocset
			id.HomeDir = s.resolveHomeDir(policy.HomeDocset)
		} else {
			deny := AuthorizationPolicy{}
			id.policySnapshot = &deny
		}
	}
	return id
}

func (s *Server) buildSessionShell(id Identity) *shell.Shell {
	if s.authEnforced {
		if policy, err := s.currentPolicy(id); err == nil {
			id.policySnapshot = &policy
			id.HomeDocset = policy.HomeDocset
			id.HomeDir = s.resolveHomeDir(policy.HomeDocset)
		} else {
			deny := AuthorizationPolicy{}
			id.policySnapshot = &deny
			id.HomeDocset, id.HomeDir = "", ""
		}
	}
	sessionFS := s.buildSessionFS(id)
	// Command visibility is snapshotted, but privileged operations are narrowed
	// again at invocation. Guest, read-scoped, and currently read-only sessions
	// do not see write verbs.
	canWrite := s.authEnforced && id.IdentityName != "" && id.IdentityName != "guest" && len(s.writableDocsetNames(id)) > 0
	canAdmin := s.authEnforced && id.policySnapshot != nil && scopeGrantsWrite(id.Scopes) && s.hasCapabilityForPolicy(*id.policySnapshot, "lore:config:edit")

	sh := shell.NewShell(sessionFS)
	if s.config.Debug {
		sh.SetUnsupportedUsageHandler(func(usage shell.UnsupportedUsage) {
			attrs := []any{"kind", usage.Kind}
			if usage.Command != "" {
				attrs = append(attrs, "command", usage.Command)
			}
			if usage.Syntax != "" {
				attrs = append(attrs, "syntax", usage.Syntax, "error", usage.Error)
			}
			if id.IdentityName != "" {
				attrs = append(attrs, "identity", id.IdentityName)
			}
			s.logger.Debug("unsupported shell usage", attrs...)
		})
	}
	if s.config.DefaultCwd != "" {
		sh.SetCwd(s.config.DefaultCwd)
	}
	// Per-docset / global write-conflict policy for overwrite verbs.
	sh.SetConflictPolicyFn(s.writeConflictPolicy)

	// Capability gating (Part B). Only applies when an auth config is
	// present — without one (local `openlore .` or an embedded KB server
	// that does its own scoping) the shell stays unrestricted. With auth,
	// an unrecognized/guest identity is read-only; a recognized
	// identity may write and publish within its docsets.
	if s.authEnforced {
		if canWrite || canAdmin {
			allowed := []cmds.Action{}
			if canWrite {
				allowed = append(allowed, cmds.ActionWrite, cmds.ActionPublish)
			} else if canAdmin {
				allowed = append(allowed, cmds.ActionWrite)
			}
			// spawn runs an external command as the OpenLore service
			// user, so it's gated on an explicit `spawn` capability
			// (Part D) — never granted to ordinary writers.
			if s.hasCapabilityForPolicy(*id.policySnapshot, "spawn") {
				allowed = append(allowed, cmds.ActionSpawn)
			}
			if canAdmin {
				allowed = append(allowed, cmds.ActionAdmin)
			}
			sh.SetAllowedActions(allowed)
		} else {
			sh.SetAllowedActions(nil) // read-only (ActionRead implied)
		}
		sh.SetActionAuthorizer(func(action cmds.Action) bool {
			if action == cmds.ActionWrite && !canWrite {
				return s.hasCurrentCapability(id, "lore:config:edit")
			}
			if action == cmds.ActionSpawn {
				return s.hasCurrentCapability(id, "spawn")
			}
			if action == cmds.ActionAdmin {
				return s.hasCurrentCapability(id, "lore:config:edit")
			}
			return true
		})
	}

	// Per-session docset views for `lore docsets` and publish inboxes for
	// `publish`. Computed once here, where the access authority lives.
	sh.SetDocsets(s.sessionDocsets(id))
	sh.SetSkillsManagementEnabled(s.config.Plugins.Skills.Enabled)
	sh.SetSkillsRemoteConfig(s.config.Plugins.Skills.RemoteTimeout, s.config.Plugins.Skills.RemoteMaxBytes)
	sh.SetMetaFilters(s.sessionMetaFilters(id))
	sh.SetPublishTargets(s.sessionPublishTargets(id))
	sh.SetMetaExtenders(s.metaExtenders)
	sh.SetValidators(s.validators)
	a := id.attribution()
	sh.SetCommandAttribution(cmds.JobAttribution{Principal: a.Principal, Actor: a.Actor, ClientAuth: string(a.ClientAuth)})
	if canAdmin {
		sh.SetConfigReloadBackend(s)
	}
	if s.historyPath != "" {
		sh.SetHistoryBackend(fileHistory{path: s.historyPath, roots: historyRoots(s.sessionDocsets(id))})
	}
	sh.SetJobBackend(s.jobs)

	// Set identity info as environment variables
	if id.IdentityName != "" {
		sh.SetEnv("OPENLORE_IDENTITY", id.IdentityName)
	}
	if id.Attribution.Actor != "" {
		sh.SetEnv("OPENLORE_ACTOR", id.Attribution.Actor)
	}
	if id.Attribution.ClientAuth != "" {
		sh.SetEnv("OPENLORE_CLIENT_AUTH", string(id.Attribution.ClientAuth))
	}
	// $HOME points at the identity's home docset (enables ~ expansion and
	// `cd` with no arguments).
	if id.HomeDir != "" {
		sh.SetEnv("HOME", id.HomeDir)
	}
	// Expose the user as the agent ID so commands like `kb` and
	// the writable VFS can scope operations per-agent.
	if id.User != "" {
		sh.SetEnv("OPENLORE_USER", id.User)
		sh.SetEnv("OPENLORE_AGENT_ID", id.User)
	}

	return sh
}

// sessionDocsets resolves one row per accessible canonical or alias mount. A
// docset's canonical paths are emitted first, followed by aliases targeting its
// first path; `lore docsets` preserves this order for older consumers that use
// the first row as the docset's mount.
func (s *Server) sessionDocsets(id Identity) []cmds.DocsetInfo {
	writable := s.writableDocsetNames(id)
	var out []cmds.DocsetInfo
	for name := range s.currentAuth().Docsets {
		ds, ok := s.currentAuth().Docsets[name]
		if !ok {
			continue
		}
		grantNames, accessible := s.effectiveGrantNames(id, name)
		if !accessible {
			continue
		}
		legacyGrant := ""
		if len(grantNames) == 1 && s.authorizationStore == nil {
			legacyGrant = grantNames[0]
		}
		for i, pm := range ds.Paths {
			out = append(out, cmds.DocsetInfo{
				Name:        name,
				Paths:       []string{displayPath(pm)},
				Grants:      grantNames,
				Grant:       legacyGrant,
				Writable:    writable[name],
				Home:        i == 0 && name == id.HomeDocset,
				Inbox:       inboxPath(ds) != "",
				AgentSkills: directMarker(s.merge, s.canonicalPath(displayPath(pm))),
			})
		}
		target := primaryDisplayPath(ds)
		for _, alias := range ds.Aliases {
			out = append(out, cmds.DocsetInfo{
				Name:        name,
				Paths:       []string{vfs.CleanPath(alias)},
				AliasTarget: target,
				Grants:      grantNames,
				Grant:       legacyGrant,
				Writable:    writable[name],
				AgentSkills: directMarker(s.merge, s.canonicalPath(target)),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func directMarker(fsys vfs.FileSystem, p string) bool {
	if fsys == nil || (reflect.ValueOf(fsys).Kind() == reflect.Ptr && reflect.ValueOf(fsys).IsNil()) {
		return false
	}
	x, ok := fsys.(vfs.XattrReader)
	if !ok {
		return false
	}
	b, err := x.GetXattr(p, agentSkillsMarker)
	return err == nil && len(b) == 0
}

func (s *Server) sessionMetaFilters(id Identity) []meta.Filter {
	var out []meta.Filter
	for _, f := range s.metaFilters {
		nf := f
		nf.Roots = nil
		seen := map[string]bool{}
		for docsetName, ds := range s.currentAuth().Docsets {
			grantNames, accessible := s.effectiveGrantNames(id, docsetName)
			if !accessible {
				continue
			}
			for _, pm := range ds.Paths {
				docRoot := s.canonicalPath(displayPath(pm))
				for _, providerRoot := range f.Roots {
					providerRoot = s.canonicalPath(providerRoot)
					// Filters are bound to docset roots, not merely intersecting
					// descendants. A grant on a nested ordinary docset must not
					// expose a filter contributed by its skills-enabled ancestor.
					readable := false
					for _, grantName := range grantNames {
						if grant, ok := s.grants.get(grantName); ok && grant.CanRead(ds, providerRoot) {
							readable = true
							break
						}
					}
					if providerRoot == docRoot && !seen[providerRoot] && readable {
						seen[providerRoot] = true
						nf.Roots = append(nf.Roots, providerRoot)
					}
				}
			}
		}
		if len(nf.Roots) > 0 {
			out = append(out, nf)
		}
	}
	return out
}

// sessionPublishTargets resolves the write inboxes this session may publish to:
// docsets whose grant allows writes and that declare an inbox. Only exist when
// auth is enforced.
func (s *Server) sessionPublishTargets(id Identity) []cmds.PublishTarget {
	if !s.authEnforced {
		return nil
	}
	var out []cmds.PublishTarget
	for name := range s.currentAuth().Docsets {
		ds, ok := s.currentAuth().Docsets[name]
		if !ok {
			continue
		}
		grantNames, ok := s.effectiveGrantNames(id, name)
		if !ok {
			continue
		}
		write := false
		for _, grantName := range grantNames {
			if grant, ok := s.grants.get(grantName); ok && grant.AllowsWrite() {
				write = true
			}
		}
		if !write {
			continue
		}
		inbox := inboxPath(ds)
		if inbox == "" {
			continue
		}
		out = append(out, cmds.PublishTarget{Name: name, InboxPath: inbox, MaxFileSize: ds.MaxWriteSize})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// writableDocsetNames reports, by docset name, which docsets this session may
// write to directly with the normal write verbs. The global lock must be open;
// in unenforced mode every docset is writable; when enforced, the identity must
// be named, hold write scope, and hold a grant whose AllowsWrite is true and
// that is not per-docset readonly.
func (s *Server) writableDocsetNames(id Identity) map[string]bool {
	out := map[string]bool{}
	if s.config.Readonly {
		return out // global lock closed: nothing is writable
	}
	if !s.authEnforced {
		for name := range s.currentAuth().Docsets {
			out[name] = true
		}
		return out
	}
	if id.IdentityName == "" || !scopeGrantsWrite(id.Scopes) {
		return out
	}
	for name := range s.currentAuth().Docsets {
		ds, ok := s.currentAuth().Docsets[name]
		if !ok {
			continue
		}
		if ds.Readonly != nil && *ds.Readonly {
			continue
		}
		grantNames, ok := s.effectiveGrantNames(id, name)
		if !ok {
			continue
		}
		for _, grantName := range grantNames {
			if grant, ok := s.grants.get(grantName); ok && grant.AllowsWrite() {
				out[name] = true
			}
		}
	}
	return out
}

// anonymousIdentity returns the identity used for callers that present no
// credential (keyless SSH, or an unauthenticated MCP/HTTP request). Enforced
// sessions resolve the reserved guest role; unenforced sessions receive
// synthetic public rw authority in effectiveGrantNames.
func (s *Server) anonymousIdentity() Identity {
	id := Identity{
		SessionID:   generateSessionID(),
		ConnectedAt: time.Now(),
	}
	if s.authEnforced {
		id.IdentityName = "guest"
		id.Attribution = Attribution{Principal: "guest"}
		id.Principal = AuthenticatedPrincipal{Subject: "guest", IdentityName: "guest", Source: "guest"}
		id.Scopes = []string{ScopeFull}
	} else {
		id.Scopes = []string{ScopeFull}
	}
	return id
}

// identityCtxKey is the context key under which every transport stores the
// resolved caller Identity. SSH stores it after public-key/cert resolution;
// the MCP/HTTP boundary stores it after bearer-token verification (Phase 1).
type identityCtxKey struct{}

// contextWithIdentity returns ctx carrying the resolved identity. Both the SSH
// shell handler and the MCP/HTTP request boundary call this so that all
// downstream scoping reads the identity from one place, regardless of
// transport.
func contextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// identityFromContext returns the caller identity previously stored in ctx by
// the transport boundary. If none is present (no credential resolved) it falls
// back to the anonymous identity — mirroring keyless SSH. This is the single
// accessor used by both SSH and MCP/HTTP tool handling.
func (s *Server) identityFromContext(ctx context.Context) Identity {
	if id, ok := ctx.Value(identityCtxKey{}).(Identity); ok {
		return id
	}
	return s.anonymousIdentity()
}

// shellForContext builds a per-identity shell from the identity carried in ctx.
// It is the single factory used by both the SSH shell handler and the MCP/HTTP
// tool handler, so every transport enforces the same per-identity scoping.
func (s *Server) shellForContext(ctx context.Context) *shell.Shell {
	return s.buildSessionShell(s.identityFromContext(ctx))
}

// ListenAndServe starts the SSH server (blocks).
func (s *Server) ListenAndServe() error {
	if err := s.validateGrants(); err != nil {
		return err
	}
	preparedRoutes := make([]HTTPRouteRegistrar, 0, len(s.httpRoutes))
	if s.config.HTTPPort > 0 {
		for _, provider := range s.httpRoutes {
			register, err := provider.PrepareHTTPRoutes(s)
			if err != nil {
				return err
			}
			preparedRoutes = append(preparedRoutes, register)
		}
	}
	opts := []ssh.Option{
		wish.WithAddress(fmt.Sprintf(":%d", s.config.Port)),
		wish.WithHostKeyPath(s.config.HostKeyPath),
		wish.WithSubsystem("sftp", s.sftpSubsystem),
		wish.WithMiddleware(
			s.shellHandler,
			logging.Middleware(),
		),
	}

	if s.config.AllowKeyless {
		opts = append(opts, ssh.EmulatePty())
		// Keyless mode accepts everyone, but we must still capture the
		// client's public key when they present one so identity-based access
		// (lore, publish docsets, whoami) resolves. We therefore set a
		// PublicKeyAuth handler that accepts any key — this makes
		// sess.PublicKey() available for matching in buildIdentity. To still
		// admit clients with *no* key at all (fresh containers, CI runners,
		// brand-new VMs), we additionally enable keyboard-interactive auth
		// that auto-succeeds. Key-bearing clients authenticate via publickey
		// (key captured); keyless clients fall back to keyboard-interactive.
		opts = append(opts, ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			return true
		}))
		opts = append(opts, ssh.KeyboardInteractiveAuth(func(ctx ssh.Context, challenger gossh.KeyboardInteractiveChallenge) bool {
			return true
		}))
	} else {
		opts = append(opts, ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			if s.config.UnknownIdentity == "deny" && s.authEnforced {
				// Extract underlying key from certificates
				matchKey := key
				var cert *gossh.Certificate
				if c, ok := key.(*gossh.Certificate); ok {
					cert = c
					matchKey = c.Key
				}

				for _, ident := range s.currentAuth().Identities {
					if publicKeyMatches(ident.PublicKey, matchKey) {
						return true
					}
				}

				// Allow cert-authenticated users whose principal names an identity.
				if cert != nil {
					for _, principal := range cert.ValidPrincipals {
						if _, ok := s.findAuthIdentity(principal); ok {
							return true
						}
					}
				}

				return false
			}
			return true
		}))
	}

	if s.config.CAKeysFile != "" {
		opts = append(opts, wish.WithTrustedUserCAKeys(s.config.CAKeysFile))
	}

	srv, err := wish.NewServer(opts...)
	if err != nil {
		return fmt.Errorf("creating SSH server: %w", err)
	}

	if s.config.HostCertFile != "" {
		certBytes, err := os.ReadFile(s.config.HostCertFile)
		if err != nil {
			return fmt.Errorf("reading host certificate: %w", err)
		}
		parsed, _, _, _, err := gossh.ParseAuthorizedKey(certBytes)
		if err != nil {
			return fmt.Errorf("parsing host certificate: %w", err)
		}
		cert, ok := parsed.(*gossh.Certificate)
		if !ok {
			return fmt.Errorf("host certificate file does not contain a certificate")
		}

		hostKeyBytes, err := os.ReadFile(s.config.HostKeyPath)
		if err != nil {
			return fmt.Errorf("reading host key for certificate: %w", err)
		}
		hostSigner, err := gossh.ParsePrivateKey(hostKeyBytes)
		if err != nil {
			return fmt.Errorf("parsing host key for certificate: %w", err)
		}
		certSigner, err := gossh.NewCertSigner(cert, hostSigner)
		if err != nil {
			return fmt.Errorf("creating host certificate signer: %w", err)
		}
		srv.AddHostKey(certSigner)
	}

	s.srv = srv

	if s.config.MetricsPort > 0 {
		metrics.StartServer(s.config.MetricsPort, s.metrics, s.logger)
	}

	if s.config.HTTPPort > 0 {
		webFS := assets.Web()
		if webFS != nil {
			httpCfg := httpserver.Config{
				Port:           s.config.HTTPPort,
				TLSCert:        s.config.TLSCert,
				TLSKey:         s.config.TLSKey,
				ClientCABundle: s.config.MTLS.CABundle,
				HostKeyPath:    s.config.HostKeyPath,
				SSHPort:        s.advertisedSSHPort(),
				Logger:         s.logger,
				ExtraHandlers:  map[string]http.Handler{},
			}

			// Third-party license notices, served from the embedded legal
			// filesystem so recipients of a distributed binary/image can read
			// the attributions required by our dependencies' licenses.
			if legalFS := assets.Legal(); legalFS != nil {
				legalHandler := legal.Handler(legalFS)
				httpCfg.ExtraHandlers["/legal"] = legalHandler
				httpCfg.ExtraHandlers["/legal/"] = legalHandler
				s.logger.Info("legal notices mounted", "path", "/legal", "http_port", s.config.HTTPPort)
			}
			httpCfg.ExtraHandlers["/assets/openlore.css"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
				w.Header().Set("Cache-Control", "public, max-age=3600")
				_, _ = w.Write(webstyle.CSS)
			})

			// A single MCP server backs both the Streamable HTTP endpoint and
			// the plain JSON HTTP API below. Its `shell` tool builds a
			// per-identity scoped shell from the request context, so MCP/HTTP
			// callers get the same lore/capability scoping as an SSH session.
			var mcpServer *mcp.Server
			if (s.config.MCPEnabled && s.config.MCPPath != "") || (s.config.APIEnabled && s.config.APIPath != "") {
				mcpServer = NewMCPServer(
					s.fs,
					WithMCPReadOnly(s.config.Readonly),
					withMCPShellFactory(s.shellForContext),
				)
			}

			// Mount the MCP-over-HTTP (Streamable HTTP) endpoint on the HTTP
			// server at the configured path, so it reuses the same port/TLS as
			// the front page (and any load balancer rule fronting it).
			if s.config.MCPEnabled && s.config.MCPPath != "" {
				mcpPath := "/" + strings.Trim(s.config.MCPPath, "/")
				mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
					return mcpServer
				}, nil)
				// Posture-aware bearer auth (§4): identity from a verified token
				// (or anonymous) is placed on the request context, which the
				// Streamable transport carries into the tool handler.
				h := s.authMiddleware(mcpHandler, s.config.MCPAuthRequired())
				httpCfg.ExtraHandlers[mcpPath] = h
				httpCfg.ExtraHandlers[mcpPath+"/"] = h
				s.logger.Info("MCP endpoint mounted", "path", mcpPath, "http_port", s.config.HTTPPort)
			}

			// Mount the plain JSON HTTP API (backed by the same MCP server) at
			// the configured path. It exposes the MCP tools over simple REST
			// endpoints: POST {path}/shell and GET {path}/commands.
			if s.config.APIEnabled && s.config.APIPath != "" {
				apiPath := "/" + strings.Trim(s.config.APIPath, "/")
				api := NewMCPHTTPAPI(mcpServer, s.shellForContext)
				httpCfg.ExtraHandlers[apiPath+"/"] = s.authMiddleware(api.Handler(apiPath), !s.config.AllowKeyless)
				s.logger.Info("HTTP API mounted", "path", apiPath, "http_port", s.config.HTTPPort)
			}

			// Mount the OAuth token endpoint + JWKS when token auth is enabled
			// (auth.tokens configured). This is the single mint step for login
			// (authorization_code) and, later, WIF exchange (jwt-bearer).
			if s.tokens != nil {
				for path, h := range s.oauthRoutes() {
					httpCfg.ExtraHandlers[path] = h
				}
				s.logger.Info("token endpoint mounted", "path", tokenPath, "http_port", s.config.HTTPPort)
				s.logger.Info("authorize endpoint mounted", "path", authorizePath, "http_port", s.config.HTTPPort)
				s.logger.Info("client registration mounted", "path", registrationPath, "http_port", s.config.HTTPPort)
				s.logger.Info("oauth metadata mounted", "path", protectedResourceMetadataPath, "http_port", s.config.HTTPPort)
			}
			for _, register := range preparedRoutes {
				httpCfg.Extenders = append(httpCfg.Extenders, routeExtender{register: register})
			}

			if s.passkeys != nil {
				// Build the HTTP base URL for the passkey shell command.
				// If rp_origins are configured, use the first one as the base URL
				// (handles TLS termination at a load balancer).
				var baseURL string
				if len(s.config.Passkeys.RPOrigins) > 0 {
					baseURL = strings.TrimRight(s.config.Passkeys.RPOrigins[0], "/")
				} else {
					scheme := "http"
					if s.config.TLSCert != "" {
						scheme = "https"
					}
					baseURL = fmt.Sprintf("%s://localhost:%d", scheme, s.config.HTTPPort)
					if s.config.Passkeys.RPID != "" && s.config.Passkeys.RPID != "localhost" {
						baseURL = fmt.Sprintf("%s://%s", scheme, s.config.Passkeys.RPID)
						if (scheme == "http" && s.config.HTTPPort != 80) || (scheme == "https" && s.config.HTTPPort != 443) {
							baseURL = fmt.Sprintf("%s:%d", baseURL, s.config.HTTPPort)
						}
					}
				}

				passkeys.RegisterShellCommand(s.passkeys, baseURL)

				// Wire the token-minting seam so passkey login can drive the
				// OAuth authorization-code flow and resolve identity → lore for
				// the browser cookie. Always set (not gated on token auth): the
				// Server implements the full interface, and IssueAuthCode /
				// CompleteAuthorize return ok=false when token auth is disabled.
				s.passkeys.SetTokenIssuer(s)

				httpCfg.Extenders = append(httpCfg.Extenders, s.passkeys)
				httpCfg.ExtraHandlers[permissionsPath] = http.HandlerFunc(s.handlePermissionsPage)
				httpCfg.ExtraHandlers[permissionsUpdatePath] = http.HandlerFunc(s.handleDelegateUpdate)
				httpCfg.ExtraHandlers[permissionsRemovePath] = http.HandlerFunc(s.handleDelegateRemove)

				lorePath := s.config.Passkeys.LorePath
				if lorePath == "" {
					lorePath = "/lore"
				}
				lorePath = "/" + strings.Trim(lorePath, "/")
				// The /lore web browser reads through the same per-identity
				// scoped canonical FS as every other transport, so docset
				// boundaries (including nested-docset carve-outs) are enforced
				// identically, without exposing filesystem path aliases in the
				// human-facing folder tree. An unresolved identity falls back to
				// the anonymous/default authority.
				httpCfg.ExtraHandlers[lorePath+"/"] = s.passkeys.LoreBrowserHandler(func(name string) vfs.FileSystem {
					id, ok := s.identityForName(name)
					if !ok {
						id = s.anonymousIdentity()
					}
					return s.buildCanonicalSessionFS(id)
				}, func(name, filePath string) ([]passkeys.FileHistoryEntry, error) {
					if s.historyPath == "" {
						return nil, nil
					}
					id, ok := s.identityForName(name)
					if !ok {
						id = s.anonymousIdentity()
					}
					entries, err := (fileHistory{path: s.historyPath, roots: historyRoots(s.sessionDocsets(id))}).QueryFile(filePath)
					if err != nil {
						return nil, err
					}
					history := make([]passkeys.FileHistoryEntry, len(entries))
					for i, entry := range entries {
						history[i] = passkeys.FileHistoryEntry{
							Time: entry.Time, Attribution: entry.Attribution, Action: entry.Action, Hash: entry.Hash,
						}
					}
					return history, nil
				})

				cmds.PublishBaseURL = baseURL + lorePath
			}

			httpSrv, err := httpserver.New(webFS, httpCfg)
			if err != nil {
				return fmt.Errorf("creating HTTP server: %w", err)
			}
			httpSrv.Start()
			s.httpSrv = httpSrv
		}
	}

	s.logger.Info("SSH server starting", "port", s.config.Port)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.passkeys != nil {
		s.passkeys.Shutdown()
	}
	// Give in-flight async jobs (Part D) a few seconds to commit their
	// write-back before we exit, shrinking the loss window.
	if s.jobs != nil {
		if !s.jobs.Drain(jobDrainTimeout) {
			s.logger.Warn("shutdown: async jobs still running after drain timeout", "timeout", jobDrainTimeout)
		}
	}
	// Stop accepting writes and drain the log so every acknowledged mutation
	// commits before we exit (in-flight + queued entries still get their reply).
	if s.writeLog != nil {
		if err := s.writeLog.Close(ctx); err != nil {
			s.logger.Warn("shutdown: write log did not drain before deadline", "err", err)
		}
	}
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
