package openlore

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// authorizeRequest holds the validated parameters of an in-flight OAuth
// authorization-code request (RFC 6749 §4.1 + PKCE RFC 7636), created at
// GET /authorize and consumed once the login ceremony (or public-access choice)
// completes.
type authorizeRequest struct {
	ClientID            string
	RedirectURI         string
	State               string
	Scope               string
	Resource            string
	CodeChallenge       string
	CodeChallengeMethod string
	Expires             time.Time
}

// authorizeStore holds pending authorize requests keyed by an opaque request id
// carried through the passkey login page (?authz=<id>).
type authorizeStore struct {
	mu       sync.Mutex
	requests map[string]authorizeRequest
	ttl      time.Duration
}

func newAuthorizeStore() *authorizeStore {
	return &authorizeStore{requests: map[string]authorizeRequest{}, ttl: 10 * time.Minute}
}

func (a *authorizeStore) put(req authorizeRequest) string {
	id := randomToken()
	req.Expires = time.Now().Add(a.ttl)
	a.mu.Lock()
	a.requests[id] = req
	a.mu.Unlock()
	return id
}

// take returns and removes the request for id.
func (a *authorizeStore) take(id string) (authorizeRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	req, ok := a.requests[id]
	if !ok {
		return authorizeRequest{}, false
	}
	delete(a.requests, id)
	if req.Expires.Before(time.Now()) {
		return authorizeRequest{}, false
	}
	return req, true
}

// authorizeHandler serves GET /authorize: it validates the OAuth parameters,
// stashes the request, and renders the public-vs-login choice screen (§8.4).
// "Continue with public access" POSTs to /authorize/public (mints an anonymous
// code); "Log in with passkey" navigates into the passkey ceremony (?authz=<id>).
// Either path ends in a redirect back to redirect_uri?code=&state= — the
// "normal OAuth" callback flow (docs/mcp-bearer-auth.md §8.2, §8.4).
func (s *Server) authorizeHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" {
		http.Error(w, "unsupported response_type (want code)", http.StatusBadRequest)
		return
	}
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	if !s.validAuthorizeRedirect(r.Context(), clientID, redirectURI) {
		http.Error(w, "invalid or missing redirect_uri", http.StatusBadRequest)
		return
	}
	// PKCE is mandatory for browser-driven OAuth (RFC 7636); native and MCP
	// clients always send it. The only unbound codes are debug mints via
	// IssueAuthCode, which never pass through /authorize.
	challenge := q.Get("code_challenge")
	if challenge == "" {
		http.Error(w, "code_challenge required (PKCE)", http.StatusBadRequest)
		return
	}
	// Only S256 is offered (matches the advertised
	// code_challenge_methods_supported); `plain` is weaker and unnecessary.
	method := q.Get("code_challenge_method")
	if method == "" {
		method = "S256"
	}
	if method != "S256" {
		http.Error(w, "unsupported code_challenge_method (want S256)", http.StatusBadRequest)
		return
	}
	// If a resource indicator (RFC 8707) is present it must name this instance's
	// audience — the only resource OpenLore mints tokens for.
	resource := q.Get("resource")
	if resource != "" && s.config.Tokens != nil && !sameResourceIdentifier(resource, s.config.Tokens.Audience) {
		http.Error(w, "resource does not match this server's audience", http.StatusBadRequest)
		return
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = ScopeFull
	}
	req := authorizeRequest{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		State:               q.Get("state"),
		Scope:               scope,
		Resource:            resource,
		CodeChallenge:       challenge,
		CodeChallengeMethod: method,
	}
	id := s.authorizeReqs.put(req)
	s.renderAuthorizeChoice(w, id)
}

// sameResourceIdentifier treats the two valid spellings of an origin URL as
// equivalent: https://example.com and https://example.com/. OAuth clients such
// as Claude canonicalize an advertised origin to the latter form. Paths other
// than the root remain exact so this does not broaden a resource's boundary.
func sameResourceIdentifier(a, b string) bool {
	if a == b {
		return true
	}
	au, err := url.Parse(a)
	if err != nil || au.Scheme == "" || au.Host == "" || au.Opaque != "" || au.User != nil {
		return false
	}
	bu, err := url.Parse(b)
	if err != nil || bu.Scheme == "" || bu.Host == "" || bu.Opaque != "" || bu.User != nil {
		return false
	}
	if au.Scheme != bu.Scheme || au.Host != bu.Host || au.RawQuery != bu.RawQuery || au.Fragment != bu.Fragment {
		return false
	}
	ap, bp := au.EscapedPath(), bu.EscapedPath()
	if ap == "" {
		ap = "/"
	}
	if bp == "" {
		bp = "/"
	}
	return ap == bp
}

// authorizePublicHandler serves POST /authorize/public: the "continue with
// public access" choice (§8.4). It finalizes the pending request as the reserved
// anonymous subject and redirects back to the client's callback with a code that
// exchanges into a read-only default-lore token — so an OAuth-native client
// (Claude) completes the flow without logging in.
func (s *Server) authorizePublicHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}
	authz := r.Form.Get("authz")
	redirectURL, ok := s.CompleteAuthorize(authz, anonymousSubject)
	if !ok {
		http.Error(w, "authorization request expired — restart the flow", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// authorizeChoiceTmpl is the public-vs-login screen. The login button navigates
// to the passkey ceremony; the public button POSTs to /authorize/public. When
// passkeys are disabled only the public option is offered so OAuth still
// completes.
var authorizeChoiceTmpl = template.Must(template.New("authorize").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Connect — OpenLore</title>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0d1117;color:#c9d1d9;display:flex;align-items:center;justify-content:center;min-height:100vh}
  .card{background:#161b22;border:1px solid #30363d;border-radius:12px;padding:2.5rem;max-width:420px;width:100%;text-align:center}
  h1{font-size:1.5rem;margin-bottom:.5rem}
  .subtitle{color:#8b949e;margin-bottom:1.5rem;font-size:.9rem}
  button,a.btn{display:block;width:100%;background:#238636;color:#fff;border:none;padding:.75rem 2rem;border-radius:8px;font-size:1rem;cursor:pointer;text-decoration:none;margin-bottom:.75rem}
  button:hover,a.btn:hover{background:#2ea043}
  a.btn.secondary{background:#21262d;border:1px solid #30363d}
  a.btn.secondary:hover{background:#30363d}
</style></head>
<body><div class="card">
  <h1>📜 OpenLore</h1>
  <p class="subtitle">How do you want to connect?</p>
  {{if .Passkeys}}<a class="btn" href="{{.LoginURL}}">Log in with passkey</a>{{end}}
  <form method="post" action="{{.PublicPath}}">
    <input type="hidden" name="authz" value="{{.Authz}}">
    <button type="submit"{{if .Passkeys}} class="secondary-btn"{{end}}>Continue with public access</button>
  </form>
</div></body></html>`))

func (s *Server) renderAuthorizeChoice(w http.ResponseWriter, authz string) {
	loginURL := url.URL{Path: s.passkeyLoginPath()}
	lq := loginURL.Query()
	lq.Set("authz", authz)
	loginURL.RawQuery = lq.Encode()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	authorizeChoiceTmpl.Execute(w, struct {
		Authz      string
		LoginURL   string
		PublicPath string
		Passkeys   bool
	}{
		Authz:      authz,
		LoginURL:   loginURL.String(),
		PublicPath: authorizePublicPath,
		Passkeys:   s.passkeys != nil,
	})
}

// validAuthorizeRedirect decides whether redirectURI is acceptable for the given
// client_id. A registered client (via DCR) must present a redirect_uri that
// exactly matches one it registered — this is what safely admits remote HTTPS
// callbacks (Claude). An absent/unregistered client_id falls back to the native
// rules (loopback / custom scheme only), which never permit a remote origin.
func (s *Server) validAuthorizeRedirect(ctx context.Context, clientID, redirectURI string) bool {
	if clientID != "" && s.clientStore != nil {
		if client, ok, err := s.clientStore.Lookup(ctx, clientID); err == nil && ok {
			return client.AllowsRedirect(redirectURI)
		}
	}
	return validNativeRedirectURI(redirectURI)
}

// CompleteAuthorize is called by the passkey login-finish hook once a caller has
// authenticated as sub. It mints a PKCE-bound authorization code for the pending
// authorize request and returns the redirect URL (redirect_uri?code=&state=) the
// browser should navigate to. ok is false when the request id is unknown/expired
// or token auth is disabled.
func (s *Server) CompleteAuthorize(requestID, sub string) (string, bool) {
	if s.authorizeReqs == nil || s.authCodes == nil {
		return "", false
	}
	req, ok := s.authorizeReqs.take(requestID)
	if !ok {
		return "", false
	}
	code := s.authCodes.Issue(authCode{
		Subject:             sub,
		Scope:               req.Scope,
		ClientID:            req.ClientID,
		RedirectURI:         req.RedirectURI,
		Resource:            req.Resource,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
	})

	u, err := url.Parse(req.RedirectURI)
	if err != nil {
		return "", false
	}
	rq := u.Query()
	rq.Set("code", code)
	if req.State != "" {
		rq.Set("state", req.State)
	}
	u.RawQuery = rq.Encode()
	return u.String(), true
}

// passkeyLoginPath returns the path of the passkey login page.
func (s *Server) passkeyLoginPath() string {
	return "/passkey/login"
}

// IdentityExists reports whether name is a registered identity in the auth
// table. It backs passkeys.TokenIssuer so `passkey register --identity` can
// validate its target.
func (s *Server) IdentityExists(name string) bool {
	_, ok := s.findAuthIdentity(name)
	return ok
}
