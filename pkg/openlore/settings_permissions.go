package openlore

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/passkeys"
	"github.com/aakarim/go-openlore/internal/webstyle"
)

const (
	// permissionsPath is shared with the lore browser's identity menu, which
	// links here from internal/passkeys.
	permissionsPath       = passkeys.PermissionsPath
	permissionsUpdatePath = permissionsPath + "/update"
	permissionsRemovePath = permissionsPath + "/remove"
)

type permissionToggle struct {
	Name    string
	Allowed bool
}

type delegateView struct {
	Identity              string
	DisplayName           string
	ResolutionError       string
	ClientAuth            ClientAuthLevel
	ClientAuthLabel       string
	ClientAuthVerified    bool
	Docsets               []permissionToggle
	Capabilities          []permissionToggle
	EffectiveDocsets      []string
	EffectiveCapabilities []string
	DeniedDocsets         []string
	DeniedCapabilities    []string
}

type permissionsView struct {
	Principal string
	CSRF      string
	Delegates []delegateView
}

var permissionsTmpl = template.Must(template.New("permissions").Funcs(template.FuncMap{"join": strings.Join}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Delegate permissions — OpenLore</title>` + webstyle.Link + `</head><body>
<main class="settings"><header><h1>Delegate permissions</h1><p class="subtitle">Signed in as <strong>{{.Principal}}</strong>. Delegates inherit your access, minus these denials. Effective permissions never exceed yours.</p></header>
{{if .Delegates}}<div class="delegate-list">{{range .Delegates}}<section class="delegate">
<div class="delegate-head"><div><h2>{{.Identity}}</h2>{{if .DisplayName}}<p class="display-name">{{.DisplayName}}</p>{{end}}</div><span class="badge{{if .ClientAuthVerified}} verified{{end}}">{{.ClientAuthLabel}}</span></div>
{{if .ResolutionError}}<p class="preview"><strong>Permissions unavailable:</strong> {{.ResolutionError}}. You can still disconnect this delegate.</p>{{else}}
<form method="post" action="` + permissionsUpdatePath + `"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="actor" value="{{.Identity}}">
<fieldset><legend>Docsets</legend>{{range .Docsets}}<label class="toggle"><input type="checkbox" name="allowed_docsets" value="{{.Name}}"{{if .Allowed}} checked{{end}}> {{.Name}}</label>{{else}}<span class="muted">No docset access</span>{{end}}</fieldset>
<fieldset><legend>Capabilities</legend>{{range .Capabilities}}<label class="toggle"><input type="checkbox" name="allowed_capabilities" value="{{.Name}}"{{if .Allowed}} checked{{end}}> {{.Name}}</label>{{else}}<span class="muted">No capabilities</span>{{end}}</fieldset>
<p class="preview"><strong>Can currently reach:</strong> {{if .EffectiveDocsets}}{{join .EffectiveDocsets ", "}}{{else}}no docsets{{end}}{{if .EffectiveCapabilities}}; capabilities: {{join .EffectiveCapabilities ", "}}{{end}}.<br><strong>Denied:</strong> {{if .DeniedDocsets}}{{join .DeniedDocsets ", "}}{{else}}no docsets{{end}}{{if .DeniedCapabilities}}; capabilities: {{join .DeniedCapabilities ", "}}{{end}}.</p>
<button type="submit">Save permissions</button></form>{{end}}<form class="disconnect-form" method="post" action="` + permissionsRemovePath + `" onsubmit="return confirm('Disconnect {{.Identity}}? Existing access tokens remain valid until they expire.')"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="actor" value="{{.Identity}}"><button class="danger" type="submit">Disconnect</button></form>
</section>{{end}}</div>{{else}}<div class="empty">No OAuth clients are delegated to act for your identity.</div>{{end}}</main></body></html>`))

func (s *Server) permissionsSession(w http.ResponseWriter, r *http.Request) (*passkeys.SessionInfo, bool) {
	if s.passkeys == nil {
		http.Error(w, "passkey authentication is not enabled", http.StatusNotFound)
		return nil, false
	}
	session, ok := s.passkeys.Session(r)
	if !ok {
		http.Redirect(w, r, "/passkey/login?redirect="+permissionsPath, http.StatusFound)
		return nil, false
	}
	if _, ok := s.findAuthIdentity(session.Identity); !ok {
		http.Error(w, "unknown identity", http.StatusForbidden)
		return nil, false
	}
	return session, true
}

func (s *Server) handlePermissionsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, ok := s.permissionsSession(w, r)
	if !ok {
		return
	}
	principal, _ := s.findAuthIdentity(session.Identity)
	docsets, capabilities, err := s.permissionsFor(session.Identity, "")
	if err != nil {
		http.Error(w, "failed to resolve permissions", http.StatusInternalServerError)
		return
	}
	views := make([]delegateView, 0, len(principal.Delegates))
	for _, delegate := range principal.Delegates {
		view := delegateView{
			Identity:           delegate.Identity,
			ClientAuth:         ClientAuthLevel(delegate.ClientAuth),
			ClientAuthLabel:    clientAuthLabel(ClientAuthLevel(delegate.ClientAuth)),
			ClientAuthVerified: ClientAuthLevel(delegate.ClientAuth).Verified(),
		}
		if identity, found := s.findAuthIdentity(delegate.Identity); found {
			if identity.ClientIDMetadata != nil {
				view.DisplayName = identity.ClientIDMetadata.PinnedName
			} else if strings.HasPrefix(identity.Comment, "Display: ") {
				view.DisplayName = strings.TrimPrefix(identity.Comment, "Display: ")
			}
		}
		effectiveDocsets, effectiveCapabilities, err := s.permissionsFor(session.Identity, delegate.Identity)
		if err != nil {
			view.ResolutionError = err.Error()
			views = append(views, view)
			continue
		}
		allowedDocsets := stringSet(effectiveDocsets)
		allowedCapabilities := stringSet(effectiveCapabilities)
		view.EffectiveDocsets = effectiveDocsets
		view.EffectiveCapabilities = effectiveCapabilities
		view.DeniedDocsets = difference(docsets, allowedDocsets)
		view.DeniedCapabilities = difference(capabilities, allowedCapabilities)
		for _, name := range docsets {
			view.Docsets = append(view.Docsets, permissionToggle{Name: name, Allowed: allowedDocsets[name]})
		}
		for _, name := range capabilities {
			view.Capabilities = append(view.Capabilities, permissionToggle{Name: name, Allowed: allowedCapabilities[name]})
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Identity < views[j].Identity })
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = permissionsTmpl.Execute(w, permissionsView{Principal: session.Identity, CSRF: s.passkeys.CSRFToken(session), Delegates: views})
}

func (s *Server) handleDelegateUpdate(w http.ResponseWriter, r *http.Request) {
	session, ok := s.validPermissionsPost(w, r)
	if !ok {
		return
	}
	actor := r.Form.Get("actor")
	docsets, capabilities, err := s.permissionsFor(session.Identity, "")
	if err != nil {
		http.Error(w, "failed to resolve permissions", http.StatusInternalServerError)
		return
	}
	selectedDocsets := stringSet(r.Form["allowed_docsets"])
	selectedCapabilities := stringSet(r.Form["allowed_capabilities"])
	err = s.updateAuth(Attribution{Principal: session.Identity, Actor: actor}, "delegate.update", func(auth *config.AuthConfig) error {
		delegate, err := authDelegate(auth, session.Identity, actor)
		if err != nil {
			return err
		}
		delegate.DenyDocsets = difference(docsets, selectedDocsets)
		delegate.DenyCapabilities = difference(capabilities, selectedCapabilities)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, permissionsPath, http.StatusSeeOther)
}

func (s *Server) handleDelegateRemove(w http.ResponseWriter, r *http.Request) {
	session, ok := s.validPermissionsPost(w, r)
	if !ok {
		return
	}
	actor := r.Form.Get("actor")
	err := s.updateAuth(Attribution{Principal: session.Identity, Actor: actor}, "delegate.remove", func(auth *config.AuthConfig) error {
		for i := range auth.Identities {
			if auth.Identities[i].Name != session.Identity {
				continue
			}
			for j, delegate := range auth.Identities[i].Delegates {
				if delegate.Identity == actor {
					auth.Identities[i].Delegates = append(auth.Identities[i].Delegates[:j], auth.Identities[i].Delegates[j+1:]...)
					return nil
				}
			}
			return fmt.Errorf("delegate %q not found", actor)
		}
		return fmt.Errorf("principal %q not found", session.Identity)
	}, func() (map[string]any, error) {
		if s.refreshStore == nil {
			return map[string]any{"revoked_refresh_chains": 0}, nil
		}
		revoked, err := s.refreshStore.RevokeDelegation(session.Identity, actor)
		return map[string]any{"revoked_refresh_chains": revoked}, err
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, permissionsPath, http.StatusSeeOther)
}

func (s *Server) validPermissionsPost(w http.ResponseWriter, r *http.Request) (*passkeys.SessionInfo, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	session, ok := s.permissionsSession(w, r)
	if !ok {
		return nil, false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return nil, false
	}
	if !s.passkeys.ValidateCSRF(session, r.Form.Get("csrf")) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return nil, false
	}
	return session, true
}

func (s *Server) permissionsFor(principal, actor string) ([]string, []string, error) {
	id, ok := s.identityForName(principal)
	if !ok {
		return nil, nil, fmt.Errorf("principal %q not found", principal)
	}
	if actor != "" {
		id.Principal.Claims = map[string]any{"actor": actor}
		id.Attribution.Actor = actor
	}
	policy, err := s.currentPolicy(id)
	if err != nil {
		return nil, nil, err
	}
	var docsets []string
	for name := range s.currentAuth().Docsets {
		if _, allowed := s.effectiveGrantNames(id, name); allowed {
			docsets = append(docsets, name)
		}
	}
	seen := map[string]bool{}
	for _, role := range policy.Roles {
		for _, capability := range s.currentAuth().Roles[role].Allow.Capabilities {
			if s.hasCapabilityForPolicy(policy, capability) {
				seen[capability] = true
			}
		}
	}
	seen["lore:config:edit"] = s.hasCapabilityForPolicy(policy, "lore:config:edit")
	var capabilities []string
	for capability, allowed := range seen {
		if allowed {
			capabilities = append(capabilities, capability)
		}
	}
	sort.Strings(docsets)
	sort.Strings(capabilities)
	return docsets, capabilities, nil
}

func authDelegate(auth *config.AuthConfig, principal, actor string) (*config.DelegateEntry, error) {
	for i := range auth.Identities {
		if auth.Identities[i].Name == principal {
			for j := range auth.Identities[i].Delegates {
				if auth.Identities[i].Delegates[j].Identity == actor {
					return &auth.Identities[i].Delegates[j], nil
				}
			}
			return nil, fmt.Errorf("delegate %q not found", actor)
		}
	}
	return nil, fmt.Errorf("principal %q not found", principal)
}

func clientAuthLabel(level ClientAuthLevel) string {
	switch level {
	case AuthPrivateKeyJWTMTLS:
		return "mTLS"
	case AuthPrivateKeyJWT:
		return "JWKS"
	case AuthCIMD:
		return "CIMD"
	default:
		return "Unverified"
	}
}

func (s *Server) recordDelegateClientAuth(principal, actor string, level ClientAuthLevel) error {
	current, ok := s.findAuthIdentity(principal)
	if !ok {
		return fmt.Errorf("principal %q not found", principal)
	}
	delegate, ok := findDelegate(current, actor)
	if !ok {
		return fmt.Errorf("delegate %q not found", actor)
	}
	if clientAuthRank(ClientAuthLevel(delegate.ClientAuth)) >= clientAuthRank(level) {
		return nil
	}
	return s.updateAuth(Attribution{Principal: principal, Actor: actor, ClientAuth: level}, "delegate.client_auth", func(auth *config.AuthConfig) error {
		delegate, err := authDelegate(auth, principal, actor)
		if err != nil {
			return err
		}
		if clientAuthRank(ClientAuthLevel(delegate.ClientAuth)) < clientAuthRank(level) {
			delegate.ClientAuth = string(level)
		}
		return nil
	})
}

func clientAuthRank(level ClientAuthLevel) int {
	switch level {
	case AuthPrivateKeyJWTMTLS:
		return 4
	case AuthPrivateKeyJWT:
		return 3
	case AuthCIMD:
		return 2
	case AuthDCRDomain, AuthDCRLocal:
		return 1
	default:
		return 0
	}
}
