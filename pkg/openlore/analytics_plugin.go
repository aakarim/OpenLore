package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type analyticsPlugin struct {
	service *analytics.Service
	server  *Server
}

func (p *analyticsPlugin) PostCommitMiddleware() []PostCommitMiddleware {
	return []PostCommitMiddleware{p.observeWrites}
}
func (p *analyticsPlugin) observeWrites(next PostCommitHandler) PostCommitHandler {
	return func(ctx context.Context, info CommitInfo) error {
		writer := analytics.WriterAgent
		if p.server != nil {
			writer = IdentityStoreClassifier(p.server.identityStore).Classify(ctx, info.Attribution)
		}
		for _, leaf := range info.ChangeSet.Leaves() {
			if leaf.Action != vfs.ChangeActionWrite && leaf.Action != vfs.ChangeActionRemove && leaf.Action != vfs.ChangeActionRemoveAll {
				continue
			}
			action := string(leaf.Action)
			if leaf.Write != nil {
				action = "create"
				for _, record := range info.Leaves {
					if vfs.CleanPath(record.Target) == vfs.CleanPath(leaf.Target) && (record.BeforeExists || record.BeforeHash != "") {
						action = "update"
						break
					}
				}
			} else {
				action = "delete"
			}
			fields := map[string]any{"path": vfs.CleanPath(leaf.Target), "docset": docsetFromPath(leaf.Target), "action": action, "writer": string(writer), "commit_id": info.ID, "commit_hash": info.Hash}
			if leaf.Write != nil {
				facts := analytics.ComputeScalars(leaf.Target, leaf.Write.Bytes)
				fields["content_hash"] = facts.ContentHash
			} else {
				fields["content_hash"] = ""
			}
			event := analytics.Event{ID: analytics.NewID(), Time: time.Now().UTC(), Type: "doc.write", Principal: info.Attribution.Principal, Actor: info.Attribution.Actor, InvocationID: info.ID, Fields: fields}
			p.service.Record(ctx, event)
		}
		return next(ctx, info)
	}
}
func docsetFromPath(p string) string {
	p = strings.Trim(vfs.CleanPath(p), "/")
	if p == "" {
		return "public"
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

func (p *analyticsPlugin) PrepareHTTPRoutes(s *Server) (HTTPRouteRegistrar, error) {
	return func(mux *http.ServeMux) {
		auth := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var id Identity
				var ok bool
				if s.passkeys != nil {
					if session, valid := s.passkeys.Session(r); valid {
						id, ok = s.identityForName(session.Identity)
					}
				}
				if !ok && bearerToken(r) != "" && s.issuer != nil {
					if claims, err := s.issuer.Verify(bearerToken(r)); err == nil {
						id, err = s.identityStore.Resolve(r.Context(), claims)
						ok = err == nil
					}
				}
				if !ok {
					id = s.identityFromContext(r.Context())
					ok = id.IdentityName != "" && id.IdentityName != "guest"
				}
				if !ok || s.authEnforced && !s.hasCurrentCapability(id, "lore:analytics:view") {
					http.NotFound(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
			})
		}
		mux.Handle("GET /analytics/aggregations", auth(http.HandlerFunc(p.aggregations)))
		mux.Handle("GET /analytics/aggregations/{name}", auth(http.HandlerFunc(p.aggregation)))
		mux.Handle("GET /analytics/facts", auth(http.HandlerFunc(p.facts)))
		mux.Handle("GET /analytics/", auth(http.HandlerFunc(p.dashboard)))
		mux.Handle("GET /analytics/{name}", auth(http.HandlerFunc(p.dashboard)))
	}, nil
}
func (p *analyticsPlugin) aggregations(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		Name, Title, Description string
		Status                   analytics.Status
	}
	var out []item
	for _, a := range p.service.Registry().List() {
		out = append(out, item{a.Name, a.Title, a.Description, p.service.Registry().Status(a.Name)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
func queryParams(r *http.Request) analytics.Params {
	p := analytics.Params{Since: time.Now().Add(-30 * 24 * time.Hour), Until: time.Now(), Limit: 100, Extra: map[string]string{}}
	if since, ok := analyticsQueryTime(r.URL.Query().Get("since"), p.Until); ok {
		p.Since = since
	}
	if until, ok := analyticsQueryTime(r.URL.Query().Get("until"), p.Until); ok {
		p.Until = until
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		p.Limit = n
	}
	for k, v := range r.URL.Query() {
		if len(v) > 0 && k != "since" && k != "until" && k != "limit" && k != "fresh" {
			p.Extra[k] = v[0]
		}
	}
	return p
}

func analyticsQueryTime(value string, now time.Time) (time.Time, bool) {
	if value == "" || value == "now" {
		return now, value == "now"
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, true
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil && days >= 0 {
			return now.Add(-time.Duration(days) * 24 * time.Hour), true
		}
	}
	if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return now.Add(-d), true
	}
	return time.Time{}, false
}
func (p *analyticsPlugin) aggregation(w http.ResponseWriter, r *http.Request) {
	m, err := p.runAggregation(r, r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}
func (p *analyticsPlugin) facts(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	facts, err := p.scopedFacts(r).Stat(r.Context(), path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(facts)
}

func (p *analyticsPlugin) scopedFacts(r *http.Request) analytics.ContentFacts {
	if p.server != nil {
		if id, ok := r.Context().Value(identityCtxKey{}).(Identity); ok {
			return analytics.NewContentFacts(p.server.buildSessionFS(id))
		}
	}
	return p.service.Facts()
}

func (p *analyticsPlugin) runAggregation(r *http.Request, name string) (analytics.Materialized, error) {
	for _, a := range p.service.Registry().List() {
		if a.Name != name {
			continue
		}
		for _, requirement := range a.Requires {
			if requirement == "facts" {
				return p.service.Registry().RunWithFacts(r.Context(), name, queryParams(r), p.scopedFacts(r))
			}
		}
		break
	}
	return p.service.Registry().Run(r.Context(), name, queryParams(r), analytics.RunOptions{Fresh: r.URL.Query().Get("fresh") == "true"})
}

func (p *analyticsPlugin) dashboard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, "<!doctype html><html><head><meta charset=utf-8><title>OpenLore analytics</title></head><body><h1>OpenLore analytics (experimental)</h1>")
	if name == "" {
		fmt.Fprintln(w, "<h2>Health</h2><pre>")
		b, _ := json.MarshalIndent(p.service.Health(), "", "  ")
		fmt.Fprintln(w, html.EscapeString(string(b)), "</pre><h2>Aggregations</h2>")
		for _, a := range p.service.Registry().List() {
			fmt.Fprintf(w, "<section><h3><a href=\"/analytics/%s\">%s</a></h3>", a.Name, html.EscapeString(a.Title))
			m, err := p.runAggregation(r, a.Name)
			if err != nil {
				fmt.Fprintf(w, "<p>%s</p>", html.EscapeString(err.Error()))
			} else {
				renderAnalyticsTable(w, m)
			}
			fmt.Fprintln(w, "</section>")
		}
	} else {
		m, err := p.runAggregation(r, name)
		if err != nil {
			fmt.Fprintln(w, html.EscapeString(err.Error()))
		} else {
			fmt.Fprintf(w, "<h2>%s</h2>", html.EscapeString(name))
			renderAnalyticsTable(w, m)
		}
	}
	fmt.Fprintln(w, "</body></html>")
}

func renderAnalyticsTable(w io.Writer, m analytics.Materialized) {
	fmt.Fprintf(w, "<p>Status: %s</p>", m.Status)
	if m.Note != "" {
		fmt.Fprintf(w, "<p>%s</p>", html.EscapeString(m.Note))
	}
	if len(m.Table.Columns) == 0 {
		return
	}
	fmt.Fprintln(w, "<table><thead><tr>")
	for _, c := range m.Table.Columns {
		fmt.Fprintf(w, "<th>%s</th>", html.EscapeString(c))
	}
	fmt.Fprintln(w, "</tr></thead><tbody>")
	for _, row := range m.Table.Rows {
		fmt.Fprintln(w, "<tr>")
		for _, v := range row {
			fmt.Fprintf(w, "<td>%s</td>", html.EscapeString(fmt.Sprint(v)))
		}
		fmt.Fprintln(w, "</tr>")
	}
	fmt.Fprintln(w, "</tbody></table>")
}
