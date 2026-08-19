package openlore

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
)

// authorizeRequest holds the validated parameters of an in-flight OAuth
// authorization-code request (RFC 6749 §4.1 + PKCE RFC 7636), created at
// GET /authorize and consumed once the login ceremony (or public-access choice)
// completes.
type authorizeRequest struct {
	ClientID            string
	Client              OAuthClient
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

type consentRequest struct {
	AuthorizeID string
	Principal   string
	Expires     time.Time
}

type consentStore struct {
	mu       sync.Mutex
	requests map[string]consentRequest
}

func newConsentStore() *consentStore { return &consentStore{requests: map[string]consentRequest{}} }
func (s *consentStore) put(req consentRequest) string {
	id := randomToken()
	req.Expires = time.Now().Add(10 * time.Minute)
	s.mu.Lock()
	s.requests[id] = req
	s.mu.Unlock()
	return id
}
func (s *consentStore) get(id string, consume bool) (consentRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.requests[id]
	if !ok || req.Expires.Before(time.Now()) {
		delete(s.requests, id)
		return consentRequest{}, false
	}
	if consume {
		delete(s.requests, id)
	}
	return req, true
}

func (a *authorizeStore) get(id string) (authorizeRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	req, ok := a.requests[id]
	if !ok || req.Expires.Before(time.Now()) {
		delete(a.requests, id)
		return authorizeRequest{}, false
	}
	return req, true
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
	client, ok := s.resolveOAuthClient(r.Context(), clientID)
	if !ok && validNativeRedirectURI(redirectURI) {
		client, ok = OAuthClient{ClientID: clientID, RedirectURIs: []string{redirectURI}}, true
	}
	if !ok || !client.AllowsRedirect(redirectURI) {
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
		Client:              client,
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
	client, ok := s.resolveOAuthClient(ctx, clientID)
	return (ok && client.AllowsRedirect(redirectURI)) || (!ok && validNativeRedirectURI(redirectURI))
}

func (s *Server) resolveOAuthClient(ctx context.Context, clientID string) (OAuthClient, bool) {
	if clientID != "" && s.clientStore != nil {
		if client, ok, err := s.clientStore.Lookup(ctx, clientID); err == nil && ok {
			return client, true
		}
	}
	if strings.HasPrefix(clientID, "https://") && s.cimdResolver != nil {
		if client, err := s.cimdResolver.Resolve(ctx, clientID); err == nil {
			return oauthClientFromCIMD(client), true
		}
	}
	return OAuthClient{}, false
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
	req, ok := s.authorizeReqs.get(requestID)
	if !ok {
		return "", false
	}
	if sub != anonymousSubject && req.ClientID != "" {
		if req.Client.ClientID != "" {
			consent := s.consents.put(consentRequest{AuthorizeID: requestID, Principal: sub})
			return authorizeConsentPath + "?consent=" + url.QueryEscape(consent), true
		}
	}
	return s.finishAuthorize(requestID, sub, "")
}

func (s *Server) finishAuthorize(requestID, sub, actor string) (string, bool) {
	req, ok := s.authorizeReqs.take(requestID)
	if !ok {
		return "", false
	}
	code := s.authCodes.Issue(authCode{
		Subject:             sub,
		Actor:               actor,
		Scope:               req.Scope,
		ClientID:            req.ClientID,
		Client:              req.Client,
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

var delegateSlugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func redirectDomain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func defaultDelegateName(domain, clientName string) string {
	switch domain {
	case "claude.ai":
		return "claude"
	case "chatgpt.com", "openai.com":
		return "chatgpt"
	}
	s := strings.ToLower(clientName)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "oauth-client"
	}
	return s
}

type consentView struct {
	Consent, Principal, Domain, ClientName, DefaultName, Selected string
	AuthLabel, AuthClass, FixedIdentity                           string
	Delegates, Docsets, Capabilities                              []string
	EnabledDocsets, EnabledCapabilities                           map[string]bool
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Authorize OpenLore</title></head><body>
<main><h1>Authorize {{.ClientName}}</h1><p class="{{.AuthClass}}">{{.AuthLabel}}</p><p>Acting for <strong>{{.Principal}}</strong></p>
<form method="post" action="` + authorizeConsentPath + `"><input type="hidden" name="consent" value="{{.Consent}}">
<fieldset><legend>Agent identity</legend>{{range .Delegates}}<label><input type="radio" name="delegate" value="{{.}}"{{if eq . $.Selected}} checked{{end}}> {{.}}</label><br>{{end}}
{{if .FixedIdentity}}<label><input type="radio" name="delegate" value="new"{{if not .Selected}} checked{{end}}> connect as {{.FixedIdentity}}</label>{{else}}<label><input type="radio" name="delegate" value="new"{{if not .Selected}} checked{{end}}> create new: <input name="name" pattern="[a-z0-9-]+" value="{{.DefaultName}}">@{{.Domain}}</label>{{end}}</fieldset>
<fieldset><legend>Docsets</legend>{{range .Docsets}}<label><input type="checkbox" name="docset" value="{{.}}"{{if index $.EnabledDocsets .}} checked{{end}}> {{.}}</label><br>{{end}}</fieldset>
<fieldset><legend>Capabilities</legend>{{range .Capabilities}}<label><input type="checkbox" name="capability" value="{{.}}"{{if index $.EnabledCapabilities .}} checked{{end}}> {{.}}</label><br>{{end}}</fieldset>
<button type="submit">Authorize</button></form></main></body></html>`))

func (s *Server) authorizeConsentHandler(w http.ResponseWriter, r *http.Request) {
	consentID := r.URL.Query().Get("consent")
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		consentID = r.Form.Get("consent")
	}
	consent, ok := s.consents.get(consentID, r.Method == http.MethodPost)
	if !ok {
		http.Error(w, "consent request expired", http.StatusBadRequest)
		return
	}
	authz, ok := s.authorizeReqs.get(consent.AuthorizeID)
	if !ok {
		http.Error(w, "authorization request expired", 400)
		return
	}
	client := authz.Client
	if client.ClientID == "" {
		http.Error(w, "unknown client", 400)
		return
	}
	domain := redirectDomain(authz.RedirectURI)
	defaultName := defaultDelegateName(domain, client.ClientName)
	fixedIdentity, authLabel, authClass := "", "Unverified client", "unverified"
	if client.CIMD != nil {
		domain = client.CIMD.Origin
		defaultName = slugClientName(client.CIMD.ClientName, domain)
		fixedIdentity = defaultName + "@" + domain
		authLabel, authClass = "Verified client metadata", "verified"
	} else if domain == "localhost" || domain == "127.0.0.1" || domain == "::1" {
		domain = "local"
	}
	if domain == "" {
		http.Error(w, "client redirect has no domain", 400)
		return
	}
	principal, ok := s.findAuthIdentity(consent.Principal)
	if !ok {
		http.Error(w, "unknown principal", 400)
		return
	}
	var delegates []string
	for _, d := range principal.Delegates {
		if strings.HasSuffix(d.Identity, "@"+domain) {
			delegates = append(delegates, d.Identity)
		}
	}
	sort.Strings(delegates)
	id, _ := s.identityForName(consent.Principal)
	policy, _ := s.currentPolicy(id)
	var docsets []string
	for name := range s.currentAuth().Docsets {
		if _, ok := s.effectiveGrantNames(id, name); ok {
			docsets = append(docsets, name)
		}
	}
	var capabilities []string
	seen := map[string]bool{}
	for _, role := range policy.Roles {
		for _, c := range s.currentAuth().Roles[role].Allow.Capabilities {
			if s.hasCapabilityForPolicy(policy, c) {
				seen[c] = true
			}
		}
	}
	seen["lore:config:edit"] = s.hasCapabilityForPolicy(policy, "lore:config:edit")
	for c, allowed := range seen {
		if allowed {
			capabilities = append(capabilities, c)
		}
	}
	sort.Strings(docsets)
	sort.Strings(capabilities)
	selected := client.LastDelegate
	if client.CIMD != nil {
		if _, ok := findDelegate(principal, fixedIdentity); ok {
			selected = fixedIdentity
		}
	}
	if !strings.HasSuffix(selected, "@"+domain) {
		selected = ""
	}
	enabledDocsets, enabledCapabilities := stringSet(docsets), stringSet(capabilities)
	if selected != "" {
		if delegate, ok := findDelegate(principal, selected); ok {
			for _, denied := range delegate.DenyDocsets {
				delete(enabledDocsets, denied)
			}
			for _, denied := range delegate.DenyCapabilities {
				delete(enabledCapabilities, denied)
			}
		}
	}
	view := consentView{
		Consent: consentID, Principal: consent.Principal, Domain: domain,
		ClientName: client.ClientName, DefaultName: defaultName, Selected: selected,
		AuthLabel: authLabel, AuthClass: authClass, FixedIdentity: fixedIdentity,
		Delegates: delegates, Docsets: docsets, Capabilities: capabilities,
		EnabledDocsets: enabledDocsets, EnabledCapabilities: enabledCapabilities,
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = consentTmpl.Execute(w, view)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	delegateName := r.Form.Get("delegate")
	created := false
	identityCreated := false
	if delegateName == "new" {
		slug := r.Form.Get("name")
		if client.CIMD != nil {
			slug = defaultName
		}
		if !delegateSlugRE.MatchString(slug) {
			http.Error(w, "invalid identity name", 400)
			return
		}
		delegateName = slug + "@" + domain
		created = true
	} else if !containsString(delegates, delegateName) {
		http.Error(w, "delegate was not offered", 400)
		return
	}
	selectedDocsets, selectedCapabilities := stringSet(r.Form["docset"]), stringSet(r.Form["capability"])
	eventType := "config.edit"
	if created {
		eventType = "delegate.create"
	}
	err := s.updateAuth(Attribution{Principal: consent.Principal, Actor: delegateName}, eventType, func(auth *config.AuthConfig) error {
		var p *config.AuthIdentity
		principalIndex := -1
		for i := range auth.Identities {
			if auth.Identities[i].Name == consent.Principal {
				principalIndex = i
				p = &auth.Identities[i]
			}
		}
		if p == nil {
			return fmt.Errorf("principal disappeared")
		}
		idx := -1
		for i := range p.Delegates {
			if p.Delegates[i].Identity == delegateName {
				idx = i
			}
		}
		if idx < 0 {
			p.Delegates = append(p.Delegates, config.DelegateEntry{Identity: delegateName})
			idx = len(p.Delegates) - 1
		}
		if created {
			found := false
			for _, identity := range auth.Identities {
				if identity.Name == delegateName {
					if !compatibleOAuthIdentity(identity, client, domain) {
						return fmt.Errorf("identity already exists")
					}
					found = true
					break
				}
			}
			if !found {
				identity := config.AuthIdentity{Name: delegateName, Comment: "Display: " + client.ClientName, CreatedBy: "oauth"}
				if client.CIMD != nil {
					identity.ClientIDMetadata = &config.ClientIDMetadata{URL: client.CIMD.ClientID, PinnedName: client.CIMD.ClientName, FirstSeen: time.Now().UTC()}
				}
				auth.Identities = append(auth.Identities, identity)
				identityCreated = true
			}
		}
		p = &auth.Identities[principalIndex]
		d := &p.Delegates[idx]
		d.DenyDocsets = difference(docsets, selectedDocsets)
		d.DenyCapabilities = difference(capabilities, selectedCapabilities)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	client.LastDelegate = delegateName
	if client.CIMD == nil {
		_ = s.clientStore.Save(r.Context(), client)
	} else if identityCreated && s.audit != nil {
		_ = s.audit.Record(r.Context(), AuditEvent{Type: "client.cimd_pin", Attribution: Attribution{Principal: consent.Principal, Actor: delegateName}, Details: map[string]any{
			"url": client.CIMD.ClientID, "origin": client.CIMD.Origin, "pinned_name": client.CIMD.ClientName,
		}})
	}
	redirect, ok := s.finishAuthorize(consent.AuthorizeID, consent.Principal, delegateName)
	if !ok {
		http.Error(w, "authorization request expired", 400)
		return
	}
	if s.audit != nil {
		_ = s.audit.Record(r.Context(), AuditEvent{Type: "oauth.login", Attribution: Attribution{Principal: consent.Principal, Actor: delegateName}, Details: map[string]any{
			"client_id": client.ClientID, "client_name": client.ClientName, "delegate": delegateName, "domain": domain, "client_auth": baselineClientAuth(client),
		}})
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

func compatibleOAuthIdentity(identity config.AuthIdentity, client OAuthClient, domain string) bool {
	if identity.CreatedBy != "oauth" || !strings.HasSuffix(identity.Name, "@"+domain) {
		return false
	}
	if client.CIMD == nil {
		return identity.ClientIDMetadata == nil && identity.Comment == "Display: "+client.ClientName
	}
	return identity.ClientIDMetadata != nil && identity.ClientIDMetadata.PinnedName == client.CIMD.ClientName
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func difference(all []string, selected map[string]bool) []string {
	var out []string
	for _, v := range all {
		if !selected[v] {
			out = append(out, v)
		}
	}
	return out
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
