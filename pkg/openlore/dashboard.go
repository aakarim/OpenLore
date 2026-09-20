package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/passkeys"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// CapabilityAccessView exposes policy explanations, never additional content
// or mutation rights. Operators grant it to their own administrative roles.
const CapabilityAccessView = "lore:access:view"

const dashboardMaxBytes = 64 << 20

// dashboardIdentity deliberately does not inherit the shell's optional-auth
// posture. A supplied bad Authorization header cannot fall back to a cookie.
func (s *Server) dashboardIdentity(r *http.Request) (Identity, bool) {
	if !s.authEnforced {
		return Identity{}, false
	}
	var id Identity
	var ok bool
	if r.Header.Get("Authorization") != "" {
		if s.issuer == nil || s.identityStore == nil || bearerToken(r) == "" {
			return Identity{}, false
		}
		claims, err := s.issuer.Verify(bearerToken(r))
		if err != nil {
			return Identity{}, false
		}
		id, err = s.identityStore.Resolve(r.Context(), claims)
		ok = err == nil
	} else if s.passkeys != nil {
		if session, valid := s.passkeys.Session(r); valid {
			id, ok = s.identityForName(session.Identity)
		}
	}
	if !ok && r.Header.Get("Authorization") == "" {
		// Only trusted in-process middleware can populate this private key.
		id, ok = r.Context().Value(identityCtxKey{}).(Identity)
	}
	if !ok || id.IdentityName == "" || id.IdentityName == "guest" {
		return Identity{}, false
	}
	policy, err := s.currentPolicy(id)
	if err != nil {
		return Identity{}, false
	}
	id.policySnapshot = &policy
	return id, true
}

func (s *Server) dashboardAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Vary", "Cookie, Authorization")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !s.authEnforced {
			dashboardError(w, http.StatusNotFound, "dashboard requires configured authentication")
			return
		}
		id, ok := s.dashboardIdentity(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			dashboardJSON(w, map[string]string{"error": "authentication required", "login_url": "/passkey/login"})
			return
		}
		next(w, r.WithContext(contextWithIdentity(r.Context(), id)))
	})
}

func (s *Server) dashboardLorePath() string {
	root := strings.Trim(s.config.Passkeys.LorePath, "/")
	if root == "" {
		return "/lore"
	}
	return "/" + root
}

// Dashboard data endpoints are independent of the optional built frontend.
// A plain Go build continues to expose the existing backend and lore browser.
func (s *Server) dashboardRoutes(frontend fs.FS) HTTPRouteRegistrar {
	return func(mux *http.ServeMux) {
		for route, handler := range map[string]http.HandlerFunc{
			"session": s.dashboardSession, "tree": s.dashboardTree,
			"context": s.dashboardContext, "file": s.dashboardFile,
			"raw": s.dashboardRaw, "history": s.dashboardHistory,
			"access": s.dashboardAccess, "usage": s.dashboardUsage,
		} {
			mux.Handle("GET /dashboard/api/"+route, s.dashboardAuth(handler))
		}
		// Do not let a SPA fallback turn unknown API routes/methods into HTML.
		mux.HandleFunc("/dashboard/api/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				dashboardError(w, http.StatusMethodNotAllowed, "dashboard is read-only")
				return
			}
			dashboardError(w, http.StatusNotFound, "not found")
		})
		if frontend != nil {
			mux.Handle("GET /dashboard/{$}", dashboardShell(frontend))
			mux.Handle("GET /dashboard/assets/", http.StripPrefix("/dashboard/", http.FileServer(http.FS(frontend))))
		}
	}
}

func (s *Server) dashboardLoreHandler(frontend fs.FS) http.Handler {
	shell := dashboardShell(frontend)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("raw") == "1" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				dashboardError(w, http.StatusMethodNotAllowed, "dashboard is read-only")
				return
			}
			request := r.Clone(r.Context())
			query := request.URL.Query()
			query.Set("path", strings.TrimPrefix(r.URL.Path, s.dashboardLorePath()))
			request.URL.RawQuery = query.Encode()
			s.dashboardAuth(s.dashboardRaw).ServeHTTP(w, request)
			return
		}
		shell.ServeHTTP(w, r)
	})
}

func dashboardShell(frontend fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		index, err := fs.ReadFile(frontend, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = w.Write(index)
	})
}

func dashboardJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func dashboardError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	dashboardJSON(w, map[string]string{"error": message})
}

func (s *Server) dashboardSession(w http.ResponseWriter, r *http.Request) {
	id := s.identityFromContext(r.Context())
	dashboardJSON(w, map[string]any{"identity": id.IdentityName, "lore_path": s.dashboardLorePath(), "access": s.hasCurrentCapability(id, CapabilityAccessView)})
}

func (s *Server) dashboardPath(r *http.Request) (Identity, vfs.FileSystem, string) {
	id := s.identityFromContext(r.Context())
	return id, s.buildCanonicalSessionFS(id), s.canonicalPath(r.URL.Query().Get("path"))
}

type dashboardNode struct {
	Path       string           `json:"path"`
	Name       string           `json:"name"`
	Directory  bool             `json:"directory"`
	Bytes      int64            `json:"bytes"`
	Lines      int64            `json:"lines"`
	Characters int64            `json:"characters"`
	Tokens     int64            `json:"tokens"`
	Children   []*dashboardNode `json:"children,omitempty"`
}

func (s *Server) dashboardTree(w http.ResponseWriter, r *http.Request) {
	_, scoped, target := s.dashboardPath(r)
	entries, err := scoped.ReadDir(target)
	if err != nil {
		dashboardError(w, http.StatusNotFound, "folder unavailable")
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name() < entries[j].Name()
	})
	nodes := make([]dashboardNode, 0, len(entries))
	for _, entry := range entries {
		nodes = append(nodes, dashboardNode{Path: path.Join(target, entry.Name()), Name: entry.Name(), Directory: entry.Dir, Bytes: entry.Size()})
	}
	dashboardJSON(w, map[string]any{"path": target, "entries": nodes})
}

var errDashboardSize = errors.New("context is too large; select a narrower folder")

// Limit response complexity and individual file reads rather than silently
// displaying a partial corpus as an exact total. The lazy tree remains
// available when a scope has too many nodes or is too deeply nested.
func (s *Server) dashboardContextNode(ctx context.Context, scoped vfs.FileSystem, target string, depth int, nodes *int) (*dashboardNode, error) {
	info, err := scoped.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", target, err)
	}
	return s.dashboardContextNodeFromInfo(ctx, scoped, target, info, depth, nodes)
}

func (s *Server) dashboardContextNodeFromInfo(ctx context.Context, scoped vfs.FileSystem, target string, info *vfs.FileInfo, depth int, nodes *int) (*dashboardNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*nodes++
	if *nodes > 10000 || depth > 64 {
		return nil, errDashboardSize
	}
	node := &dashboardNode{Path: target, Name: path.Base(target), Directory: info.Dir}
	if !info.Dir {
		if info.Size() > dashboardMaxBytes {
			// ReadDir metadata may be stale. Revalidate before rejecting an
			// oversized child so a file removed during the walk is handled as a
			// vanished descendant rather than an oversized live one.
			current, err := scoped.Stat(target)
			if err != nil {
				return nil, fmt.Errorf("stat %s: %w", target, err)
			}
			if current.Dir {
				return nil, fmt.Errorf("stat %s: changed type: %w", target, fs.ErrNotExist)
			}
			if current.Size() > dashboardMaxBytes {
				return nil, errDashboardSize
			}
		}
		if s.analytics != nil && s.analytics.HasFactsIndex() {
			facts, err := s.analytics.CurrentFacts(ctx, target, info, func() ([]byte, error) {
				content, err := readFileBounded(s.merge, target, dashboardMaxBytes)
				if errors.Is(err, fs.ErrNotExist) {
					// Session wrappers can synthesize files such as the authorized
					// config view. Prefer durable bytes for ordinary files, but read
					// a visible synthetic file from the scoped view that exposed it.
					return readFileBounded(scoped, target, dashboardMaxBytes)
				}
				return content, err
			})
			if err != nil {
				if errors.Is(err, errFileTooLarge) {
					return nil, errDashboardSize
				}
				return nil, fmt.Errorf("read %s: %w", target, err)
			}
			node.Bytes, node.Lines = int64(facts.Scalars["bytes"]), int64(facts.Scalars["lines"])
			node.Characters, node.Tokens = int64(facts.Scalars["characters"]), int64(facts.Scalars["tokens"])
			return node, nil
		}
		content, err := readFileBounded(scoped, target, dashboardMaxBytes)
		if err != nil {
			if errors.Is(err, errFileTooLarge) {
				return nil, errDashboardSize
			}
			return nil, fmt.Errorf("read %s: %w", target, err)
		}
		compute := analytics.ComputeScalars
		if s.analytics != nil {
			compute = s.analytics.ComputeScalars
		}
		facts := compute(target, content)
		node.Bytes, node.Lines, node.Tokens = int64(len(content)), int64(facts.Scalars["lines"]), int64(facts.Scalars["tokens"])
		node.Characters = int64(utf8.RuneCount(content))
		return node, nil
	}
	entries, err := scoped.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", target, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for i := range entries {
		entry := &entries[i]
		childTarget := path.Join(target, entry.Name())
		previousNodes := *nodes
		child, err := s.dashboardContextNodeFromInfo(ctx, scoped, childTarget, entry, depth+1, nodes)
		if err != nil {
			// A workspace-wide walk spans many files and can race a concurrent
			// removal. The vanished child is no longer part of the live context;
			// keep computing the remaining readable snapshot. Errors at the
			// selected target and all non-not-found errors still fail the request.
			if errors.Is(err, fs.ErrNotExist) {
				*nodes = previousNodes
				continue
			}
			return nil, err
		}
		node.Children = append(node.Children, child)
		node.Bytes += child.Bytes
		node.Lines += child.Lines
		node.Characters += child.Characters
		node.Tokens += child.Tokens
	}
	return node, nil
}

func (s *Server) dashboardContext(w http.ResponseWriter, r *http.Request) {
	_, scoped, target := s.dashboardPath(r)
	nodes := 0
	node, err := s.dashboardContextNode(r.Context(), scoped, target, 0, &nodes)
	if errors.Is(err, errDashboardSize) {
		dashboardError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		dashboardError(w, http.StatusNotFound, "context unavailable")
		return
	}
	dashboardJSON(w, node)
}

func dashboardReadFile(scoped vfs.FileSystem, target string) ([]byte, error) {
	info, err := scoped.Stat(target)
	if err != nil || info.Dir {
		return nil, fs.ErrNotExist
	}
	if info.Size() > dashboardMaxBytes {
		return nil, errDashboardSize
	}
	content, err := readFileBounded(scoped, target, dashboardMaxBytes)
	if errors.Is(err, errFileTooLarge) {
		return nil, errDashboardSize
	}
	return content, err
}

func (s *Server) dashboardFile(w http.ResponseWriter, r *http.Request) {
	_, scoped, target := s.dashboardPath(r)
	content, err := dashboardReadFile(scoped, target)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, errDashboardSize) {
			status = http.StatusRequestEntityTooLarge
		}
		dashboardError(w, status, "file unavailable or exceeds the 64 MiB viewer limit")
		return
	}
	compute := analytics.ComputeScalars
	if s.analytics != nil {
		compute = s.analytics.ComputeScalars
	}
	facts := compute(target, content).Scalars
	facts["characters"] = float64(utf8.RuneCount(content))
	binary := !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0
	result := map[string]any{"path": target, "name": path.Base(target), "binary": binary, "facts": facts, "content_type": http.DetectContentType(content)}
	if !binary {
		result["source"] = string(content)
		if ext := strings.ToLower(path.Ext(target)); ext == ".md" || ext == ".markdown" {
			fragment, err := passkeys.RenderMarkdownFragment(content)
			if err != nil {
				dashboardError(w, http.StatusInternalServerError, "markdown preview unavailable")
				return
			}
			result["html"] = fragment
		}
	}
	dashboardJSON(w, result)
}

// Raw responses cannot execute same-origin user HTML/SVG/scripts. Raster images
// may be embedded by the Markdown renderer; every other format is a download.
func (s *Server) dashboardRaw(w http.ResponseWriter, r *http.Request) {
	_, scoped, target := s.dashboardPath(r)
	content, err := dashboardReadFile(scoped, target)
	if err != nil {
		dashboardError(w, http.StatusNotFound, "file unavailable")
		return
	}
	contentType := http.DetectContentType(content)
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		w.Header().Set("Content-Type", contentType)
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(target)}))
	}
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	_, _ = w.Write(content)
}

func (s *Server) dashboardHistory(w http.ResponseWriter, r *http.Request) {
	id, scoped, target := s.dashboardPath(r)
	info, err := scoped.Stat(target)
	if err != nil || info.Dir {
		dashboardError(w, http.StatusNotFound, "file unavailable")
		return
	}
	entries := make([]passkeys.FileHistoryEntry, 0)
	result := map[string]any{"available": s.history != nil, "entries": entries}
	if s.history != nil {
		page, err := s.history.Query(r.Context(), HistoryQuery{FileKey: target, Roots: historyRoots(s.sessionDocsets(id)), Limit: 100, Cursor: r.URL.Query().Get("cursor")})
		if err != nil {
			dashboardError(w, http.StatusServiceUnavailable, "timeline unavailable")
			return
		}
		for _, record := range page.Records {
			entries = append(entries, passkeys.FileHistoryEntry{Time: record.Time, Attribution: record.Attribution.String(), Action: record.Action, Hash: record.ContentHash})
		}
		result["entries"], result["next_cursor"] = entries, page.NextCursor
	}
	dashboardJSON(w, result)
}

func (s *Server) dashboardUsage(w http.ResponseWriter, r *http.Request) {
	id, scoped, target := s.dashboardPath(r)
	if _, err := scoped.Stat(target); err != nil {
		dashboardError(w, http.StatusNotFound, "scope unavailable")
		return
	}
	if s.analytics == nil {
		dashboardError(w, http.StatusServiceUnavailable, "analytics is disabled")
		return
	}
	days, ratio := 30, 4
	var err error
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, err = strconv.Atoi(raw)
	}
	if err != nil || days < 1 || days > 365 {
		dashboardError(w, http.StatusBadRequest, "days must be between 1 and 365")
		return
	}
	if raw := r.URL.Query().Get("ratio"); raw != "" {
		ratio, err = strconv.Atoi(raw)
	}
	if err != nil || (ratio != 4 && ratio != 6) {
		dashboardError(w, http.StatusBadRequest, "ratio must be 4 or 6 characters per token")
		return
	}
	now := time.Now().UTC()
	params := analytics.Params{Since: now.Add(-time.Duration(days) * 24 * time.Hour), Until: now, Extra: map[string]string{"path": target}}
	summary, err := analytics.UsageSummary(r.Context(), s.DashboardEventSource(id, target), params, ratio)
	if err != nil {
		dashboardError(w, http.StatusServiceUnavailable, "usage unavailable")
		return
	}
	dashboardJSON(w, summary)
}

type dashboardRole struct {
	Role   string `json:"role"`
	Grant  string `json:"grant"`
	Denied bool   `json:"denied"`
}

type dashboardDocset struct {
	Name     string          `json:"name"`
	Roots    []string        `json:"roots"`
	Roles    []dashboardRole `json:"roles"`
	Readonly bool            `json:"readonly"`
}

type dashboardRuleLayer struct {
	Origin string                    `json:"origin"`
	Scope  string                    `json:"scope"`
	Rules  map[string]rules.RuleSpec `json:"rules"`
}

func (s *Server) dashboardAccess(w http.ResponseWriter, r *http.Request) {
	id := s.identityFromContext(r.Context())
	if !s.hasCurrentCapability(id, CapabilityAccessView) {
		dashboardError(w, http.StatusNotFound, "not found")
		return
	}
	_, scoped, target := s.dashboardPath(r)
	info, err := scoped.Stat(target)
	if err != nil {
		dashboardError(w, http.StatusNotFound, "scope unavailable")
		return
	}
	governing, _, _ := s.mostSpecificDocset(target)
	docsets := make([]dashboardDocset, 0)
	for name, ds := range s.currentAuth().Docsets {
		grants, allowed := s.effectiveGrantNames(id, name)
		if !allowed {
			continue
		}
		item := dashboardDocset{Name: name, Roots: []string{}, Roles: []dashboardRole{}, Readonly: s.config.Readonly || (ds.Readonly != nil && *ds.Readonly)}
		for _, mapping := range ds.Paths {
			root := displayPath(mapping)
			if !pathWithinRoot(target, root) && !(name == governing && pathWithinRoot(root, target)) {
				continue
			}
			for _, grantName := range grants {
				if grant, ok := s.grants.get(grantName); ok && grant.CanRead(ds, root) {
					item.Roots = append(item.Roots, root)
					break
				}
			}
		}
		if len(item.Roots) == 0 {
			continue
		}
		denied := map[string]bool{}
		for _, role := range ds.Access.Deny {
			denied[role] = true
		}
		for role, grant := range ds.Access.Allow {
			item.Roles = append(item.Roles, dashboardRole{Role: role, Grant: grant, Denied: denied[role]})
			delete(denied, role)
		}
		for role := range denied {
			item.Roles = append(item.Roles, dashboardRole{Role: role, Denied: true})
		}
		sort.Strings(item.Roots)
		sort.Slice(item.Roles, func(i, j int) bool { return item.Roles[i].Role < item.Roles[j].Role })
		docsets = append(docsets, item)
	}
	sort.Slice(docsets, func(i, j int) bool { return docsets[i].Name < docsets[j].Name })
	layers := make([]dashboardRuleLayer, 0)
	notes := []string{"Roles are configured by the operator. Denials override grants; delegated restrictions and token scopes may further narrow access.", "Folder rules are validation settings, not access control. This dashboard cannot change either."}
	if governing != "" {
		dir := target
		if !info.Dir {
			dir = path.Dir(target)
		}
		configLayers := &configRuleLayers{global: s.currentAuth().Rules, docsets: s.currentAuth().Docsets}
		base, baseErr := configLayers.LayersFor(r.Context(), path.Join(dir, "__dashboard_rules__.md"))
		folder, folderErr := newFolderRuleLayers(scoped, s.currentAuth().Docsets).LayersForDir(r.Context(), dir)
		if baseErr != nil || folderErr != nil {
			notes = append(notes, "Some rule layers are unavailable; no complete effective-policy claim is made.")
		}
		for _, layer := range append(base, folder...) {
			layers = append(layers, dashboardRuleLayer{Origin: layer.Origin, Scope: layer.Scope, Rules: layer.Rules})
		}
		if _, err := rules.Unify(append(base, folder...)); err != nil {
			notes = append(notes, fmt.Sprintf("Rule layers conflict; check the configured rules (%T).", err))
		}
	}
	dashboardJSON(w, map[string]any{"path": target, "docsets": docsets, "folder_rules": layers, "notes": notes})
}
