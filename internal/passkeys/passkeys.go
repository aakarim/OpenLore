package passkeys

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

//go:embed login.html register.html
var pages embed.FS

// TokenIssuer is the seam through which a successful passkey login mints a
// bearer token for the MCP + HTTP API. pkg/openlore injects a *Server here;
// internal/passkeys cannot import pkg/openlore (import cycle), so the contract
// lives on this side. See docs/mcp-bearer-auth.md §8.2.
type TokenIssuer interface {
	// IdentityExists reports whether name is a registered identity in the auth
	// table. Registration references an identity by name (Q6/§8.3), so this
	// validates the target at register time.
	IdentityExists(name string) bool
	// IssueAuthCode mints a single-use OAuth authorization code for sub, to be
	// exchanged at /oauth/token. ok is false when token auth is disabled, in
	// which case login still sets the browser cookie but issues no bearer token.
	IssueAuthCode(sub, scope string) (code string, ok bool)
	// CompleteAuthorize finalizes an in-flight OAuth authorization-code request
	// (started at GET /authorize) for the authenticated sub, returning the
	// redirect URL (redirect_uri?code=&state=) the browser should navigate to.
	// ok is false when the request id is unknown/expired or token auth is off.
	CompleteAuthorize(requestID, sub string) (redirectURL string, ok bool)
}

// Config holds the resolved passkey configuration.
type Config struct {
	Enabled      bool
	RPID         string
	RPName       string
	RPOrigins    []string
	LorePath     string
	PasskeysFile string
	SessionTTL   time.Duration
}

// Passkeys orchestrates WebAuthn registration and login ceremonies and serves
// the HTTP endpoints that drive them. Credentials persist to a JSON file that
// agents can read and edit directly via the shell.
type Passkeys struct {
	cfg      Config
	wa       *webauthn.WebAuthn
	store    *Store
	sessions *SessionManager
	pending  *PendingStore
	logger   *slog.Logger

	auth   *config.AuthConfig
	tokens TokenIssuer

	// loginSessions holds in-flight discoverable-login ceremonies keyed by the
	// short-lived openlore_login cookie token.
	mu            sync.Mutex
	loginSessions map[string]*webauthn.SessionData
}

const loginCookieName = "openlore_login"

// New constructs a Passkeys instance. The sessionKey seeds the HMAC used to
// sign browser session cookies.
func New(cfg Config, sessionKey []byte, logger *slog.Logger) (*Passkeys, error) {
	store, err := NewStore(cfg.PasskeysFile)
	if err != nil {
		return nil, fmt.Errorf("loading passkey store: %w", err)
	}

	wconfig := &webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPName,
		RPOrigins:     cfg.RPOrigins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute, TimeoutUVD: 5 * time.Minute},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute, TimeoutUVD: 5 * time.Minute},
		},
	}
	wa, err := webauthn.New(wconfig)
	if err != nil {
		return nil, fmt.Errorf("initialising webauthn: %w", err)
	}

	return &Passkeys{
		cfg:           cfg,
		wa:            wa,
		store:         store,
		sessions:      NewSessionManager(sessionKey, cfg.SessionTTL),
		pending:       NewPendingStore(),
		logger:        logger,
		loginSessions: make(map[string]*webauthn.SessionData),
	}, nil
}

// SetAuthConfig provides the auth config used to map a lore spec to the docset
// paths a browser session may view.
func (p *Passkeys) SetAuthConfig(auth *config.AuthConfig) {
	p.auth = auth
}

// SetTokenIssuer wires the token-minting seam used by the login-success hook to
// issue bearer tokens (docs/mcp-bearer-auth.md §8.2). When nil, login only sets
// the browser session cookie.
func (p *Passkeys) SetTokenIssuer(ti TokenIssuer) {
	p.tokens = ti
}

// Shutdown stops background goroutines.
func (p *Passkeys) Shutdown() {
	if p.pending != nil {
		p.pending.Stop()
	}
}

// RegisterHTTPHandlers implements httpserver.MuxExtender. It mounts the passkey
// registration and login ceremony endpoints.
func (p *Passkeys) RegisterHTTPHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/passkey/login", p.handleLoginPage)
	mux.HandleFunc("/passkey/login/status", p.handleLoginStatus)
	mux.HandleFunc("/passkey/login/begin", p.handleLoginBegin)
	mux.HandleFunc("/passkey/login/finish", p.handleLoginFinish)
	mux.HandleFunc("/passkey/r/", p.handleRegister)
}

// --- registration ---

func (p *Passkeys) handleRegister(w http.ResponseWriter, r *http.Request) {
	// Path is /passkey/r/<token>[/info|/begin|/finish]
	rest := r.URL.Path[len("/passkey/r/"):]
	token := rest
	action := ""
	if i := indexByte(rest, '/'); i >= 0 {
		token = rest[:i]
		action = rest[i+1:]
	}

	pr := p.pending.Get(token)
	if pr == nil {
		http.NotFound(w, r)
		return
	}

	switch action {
	case "":
		page, _ := pages.ReadFile("register.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	case "info":
		writeJSON(w, map[string]string{"name": pr.Name, "identity": pr.Identity})
	case "begin":
		p.handleRegisterBegin(w, r, pr)
	case "finish":
		p.handleRegisterFinish(w, r, pr)
	default:
		http.NotFound(w, r)
	}
}

func (p *Passkeys) handleRegisterBegin(w http.ResponseWriter, r *http.Request, pr *PendingRegistration) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name != "" {
		pr.Name = body.Name
	}

	user := &passkeyUser{id: pr.UserID, name: pr.Name}

	// Exclude credentials already registered so the same authenticator isn't
	// enrolled twice.
	var existing []webauthn.Credential
	for _, c := range p.store.AllCredentials() {
		existing = append(existing, c.Credential)
	}

	creation, session, err := p.wa.BeginRegistration(
		user,
		webauthn.WithExclusions(webauthn.Credentials(existing).CredentialDescriptors()),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pr.Session = session
	writeJSON(w, creation)
}

func (p *Passkeys) handleRegisterFinish(w http.ResponseWriter, r *http.Request, pr *PendingRegistration) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if pr.Session == nil {
		http.Error(w, "registration not started", http.StatusBadRequest)
		return
	}

	user := &passkeyUser{id: pr.UserID, name: pr.Name}
	cred, err := p.wa.FinishRegistration(user, *pr.Session, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := p.store.Add(StoredCredential{
		UserID:     pr.UserID,
		Name:       pr.Name,
		Identity:   pr.Identity,
		CreatedAt:  time.Now().UTC(),
		Credential: *cred,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.pending.Delete(pr.Token)

	if p.logger != nil {
		p.logger.Info("passkey registered", "device", pr.Name, "identity", pr.Identity)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- login ---

func (p *Passkeys) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	page, _ := pages.ReadFile("login.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

func (p *Passkeys) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	creds := p.store.AllCredentials()
	writeJSON(w, map[string]any{"count": len(creds), "has_passkeys": len(creds) > 0})
}

func (p *Passkeys) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	assertion, session, err := p.wa.BeginDiscoverableLogin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	token, err := randomToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.mu.Lock()
	p.loginSessions[token] = session
	p.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     loginCookieName,
		Value:    token,
		Path:     "/passkey/login",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, assertion)
}

func (p *Passkeys) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cookie, err := r.Cookie(loginCookieName)
	if err != nil {
		http.Error(w, "no login session", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	session, ok := p.loginSessions[cookie.Value]
	if ok {
		delete(p.loginSessions, cookie.Value)
	}
	p.mu.Unlock()
	if !ok {
		http.Error(w, "login session expired", http.StatusBadRequest)
		return
	}

	var matched *StoredCredential
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		sc, ok := p.store.FindByUserID(userHandle)
		if !ok {
			return nil, fmt.Errorf("unknown user")
		}
		matched = sc
		return &passkeyUser{
			id:          sc.UserID,
			name:        sc.Name,
			credentials: []webauthn.Credential{sc.Credential},
		}, nil
	}

	cred, err := p.wa.FinishDiscoverableLogin(handler, *session, r)
	if err != nil || matched == nil {
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	_ = p.store.UpdateSignCount(cred.ID, cred.Authenticator.SignCount)

	// Set the browser session cookie for the /lore docs browser. The cookie
	// carries the identity name; the browser resolves its grants live from the
	// identity table.
	if err := p.sessions.SetCookie(w, matched.Identity); err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	// OAuth authorization-code flow: if this login was reached via /authorize
	// (?authz=<id>), finalize it and hand the browser a redirect back to the
	// client's redirect_uri with an auth code. This is the "normal OAuth"
	// callback path used by the Obsidian plugin.
	if authz := r.URL.Query().Get("authz"); authz != "" && p.tokens != nil {
		if redirectURL, ok := p.tokens.CompleteAuthorize(authz, matched.Identity); ok {
			writeJSON(w, map[string]string{"redirect": redirectURL})
			return
		}
		http.Error(w, "authorization request expired", http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]bool{"ok": true})
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Session authenticates a browser request using the passkey session cookie.
func (p *Passkeys) Session(r *http.Request) (*SessionInfo, bool) {
	return p.sessions.ValidateRequest(r)
}

// CSRFToken derives a form token bound to an authenticated browser session.
func (p *Passkeys) CSRFToken(session *SessionInfo) string {
	return p.sessions.CSRFToken(session)
}

// ValidateCSRF verifies a form token against an authenticated browser session.
func (p *Passkeys) ValidateCSRF(session *SessionInfo, token string) bool {
	return p.sessions.ValidateCSRF(session, token)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
