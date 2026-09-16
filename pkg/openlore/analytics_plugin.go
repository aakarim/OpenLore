package openlore

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/webstyle"
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
		writer := analytics.WriterUnknown
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
			fields := map[string]any{"path": vfs.CleanPath(leaf.Target), "docset": p.docsetForPath(leaf.Target), "action": action, "writer": string(writer), "actor_kind": string(writer), "commit_id": info.ID, "commit_hash": info.Hash}
			if leaf.Write != nil {
				fields["content_hash"] = leafAfterHash(info.Leaves, leaf.Target)
				if fields["content_hash"] == "" {
					fields["content_hash"] = hashContent(leaf.Write.Bytes)
				}
			} else {
				fields["content_hash"] = ""
			}
			event := analytics.Event{ID: analytics.NewID(), Time: time.Now().UTC(), Type: "doc.write", Principal: info.Attribution.Principal, Actor: info.Attribution.Actor, Transport: info.Attribution.Extra["transport"], SessionID: info.Attribution.Extra["session_id"], ClientSessionID: info.Attribution.Extra["client_session_id"], RemoteAddr: info.Attribution.Extra["remote_addr"], InvocationID: info.Attribution.Extra["invocation_id"], ParentID: info.Attribution.Extra["parent_id"], Fields: fields}
			p.service.Record(ctx, event)
		}
		return next(ctx, info)
	}
}
func leafAfterHash(leaves []LeafRecord, target string) string {
	for _, leaf := range leaves {
		if vfs.CleanPath(leaf.Target) == vfs.CleanPath(target) {
			return leaf.AfterHash
		}
	}
	return ""
}
func (p *analyticsPlugin) docsetForPath(target string) string {
	if p.server != nil && p.server.auth != nil {
		target = p.server.canonicalPath(target)
		if _, name, _, ok := owningDocset(p.server.currentAuth().Docsets, target); ok {
			return name
		}
	}
	return docsetFromPath(target)
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
	if !s.authEnforced {
		return func(*http.ServeMux) {}, nil
	}
	p.server = s
	return func(mux *http.ServeMux) {
		auth := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				setPrivateAnalyticsHeaders(w)
				id, ok := p.requestIdentity(r)
				if !ok {
					// Let dashboard clients recover expired sessions; keep resource
					// permission failures indistinguishable from missing resources.
					http.Error(w, "authentication required", http.StatusUnauthorized)
					return
				}
				if !s.hasReadableAnalyticsDocset(id) {
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

func setPrivateAnalyticsHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Vary", "Cookie, Authorization")
}

func (p *analyticsPlugin) requestIdentity(r *http.Request) (Identity, bool) {
	if p.server == nil {
		return Identity{}, false
	}
	return p.server.dashboardIdentity(r)
}

func (s *Server) hasReadableAnalyticsDocset(id Identity) bool {
	for _, docset := range s.currentAuth().Docsets {
		for _, mapping := range docset.Paths {
			candidate := displayPath(mapping)
			governing, grants, ok := s.grantsForPath(id, candidate)
			if !ok {
				continue
			}
			for _, grant := range grants {
				if grant.CanRead(governing, candidate) {
					return true
				}
			}
		}
	}
	return false
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
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n >= 0 && r.URL.Query().Has("limit") {
		p.Limit = n
	}
	for k, v := range r.URL.Query() {
		if len(v) > 0 && k != "since" && k != "until" && k != "limit" && k != "fresh" && k != "page" && k != "format" {
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
	if r.URL.Query().Get("format") == "csv" {
		request := r.Clone(r.Context())
		copyURL := *r.URL
		query := copyURL.Query()
		query.Set("limit", "0")
		query.Del("page")
		copyURL.RawQuery = query.Encode()
		request.URL = &copyURL
		m, err := p.runAggregation(request, r.PathValue("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.writeAggregationCSV(w, r.PathValue("name"), m)
		return
	}
	m, err := p.runPaginatedAggregation(r, r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

func (p *analyticsPlugin) writeAggregationCSV(w http.ResponseWriter, name string, m analytics.Materialized) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".csv"))
	writer := csv.NewWriter(w)
	_ = writer.Write(m.Table.Columns)
	for _, row := range m.Table.Rows {
		values := make([]string, len(row))
		for i, value := range row {
			values[i] = fmt.Sprint(value)
		}
		_ = writer.Write(values)
	}
	writer.Flush()
}
func (p *analyticsPlugin) facts(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	if p.server != nil {
		if _, ok := r.Context().Value(identityCtxKey{}).(Identity); !ok {
			http.NotFound(w, r)
			return
		}
		path = p.server.canonicalPath(path)
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
			return p.service.NewContentFacts(p.server.buildCanonicalSessionFS(id))
		}
	}
	return p.service.Facts()
}

func (p *analyticsPlugin) runAggregation(r *http.Request, name string) (analytics.Materialized, error) {
	params := queryParams(r)
	if p.server != nil {
		id, ok := r.Context().Value(identityCtxKey{}).(Identity)
		if !ok {
			return analytics.Materialized{}, fmt.Errorf("authenticated analytics identity required")
		}
		prefix := params.Extra["path"]
		if prefix == "" {
			prefix = "/"
		}
		prefix = p.server.canonicalPath(prefix)
		params.Extra["path"] = prefix
		return p.service.Registry().RunWithSource(r.Context(), name, params, p.server.DashboardEventSource(id, prefix), p.scopedFacts(r))
	}
	return p.service.Registry().Run(r.Context(), name, params, analytics.RunOptions{Fresh: r.URL.Query().Get("fresh") == "true"})
}

func (p *analyticsPlugin) runPaginatedAggregation(r *http.Request, name string) (analytics.Materialized, error) {
	params := queryParams(r)
	if params.Limit <= 0 {
		return p.runAggregation(r, name)
	}
	request := r.Clone(r.Context())
	copyURL := *r.URL
	query := copyURL.Query()
	query.Set("limit", "0")
	query.Del("page")
	copyURL.RawQuery = query.Encode()
	request.URL = &copyURL
	m, err := p.runAggregation(request, name)
	if err != nil {
		return m, err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page-1 > len(m.Table.Rows)/params.Limit {
		m.Table.Rows = nil
		m.Window = params
		return m, nil
	}
	offset := (page - 1) * params.Limit
	if offset >= len(m.Table.Rows) {
		m.Table.Rows = nil
	} else {
		m.Table.Rows = m.Table.Rows[offset:min(offset+params.Limit, len(m.Table.Rows))]
	}
	m.Window = params
	return m, nil
}

func (p *analyticsPlugin) dashboard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>OpenLore analytics</title>`+webstyle.Link+`</head><body><main class="analytics"><header><div><p class="eyebrow">OpenLore</p><h1>Analytics</h1><p class="subtitle">Usage, knowledge quality, and system health.</p></div><a href="/analytics/" class="analytics-home">Overview</a></header>`)
	if name == "" {
		// Global log sizes, cursors and usage counters are operator metrics,
		// not facts a reader of one docset is entitled to inspect.
		fmt.Fprintln(w, `<section><div class="section-head"><div><p class="eyebrow">Explore</p><h2>Aggregations</h2></div></div><div class="analytics-list">`)
		for _, a := range p.service.Registry().List() {
			fmt.Fprintf(w, `<article class="analytics-panel"><div class="panel-title"><div><h3><a href="/analytics/%s">%s</a></h3><p>%s</p></div><span class="badge">%s</span></div>`, url.PathEscape(a.Name), html.EscapeString(a.Title), html.EscapeString(a.Description), p.service.Registry().Status(a.Name))
			if missingRequiredAnalyticsParam(a, r) {
				fmt.Fprintln(w, `<p class="parameter-note">Open this view to choose its required parameters.</p>`)
			} else {
				m, err := p.runAggregation(r, a.Name)
				if err != nil {
					fmt.Fprintf(w, "<p>%s</p>", html.EscapeString(err.Error()))
				} else {
					renderAnalyticsTable(w, m)
				}
			}
			fmt.Fprintf(w, `<p class="panel-actions"><a href="/analytics/%s">Open view</a>`, url.PathEscape(a.Name))
			if !missingRequiredAnalyticsParam(a, r) {
				fmt.Fprintf(w, ` · <a href="/analytics/aggregations/%s?format=csv">Download CSV</a>`, url.PathEscape(a.Name))
			}
			fmt.Fprintln(w, `</p></article>`)
		}
		fmt.Fprintln(w, `</div></section>`)
	} else {
		var selected analytics.Aggregation
		for _, aggregation := range p.service.Registry().List() {
			if aggregation.Name == name {
				selected = aggregation
				break
			}
		}
		fmt.Fprintf(w, `<section><div class="section-head"><div><p class="eyebrow">Aggregation</p><h2>%s</h2></div><a class="download" href="%s">Download CSV</a></div>`, html.EscapeString(name), html.EscapeString(analyticsPageURL(r, "/analytics/aggregations/"+url.PathEscape(name), map[string]string{"format": "csv", "page": ""})))
		if selected.Name == "" {
			fmt.Fprintln(w, `<p class="error">Unknown aggregation.</p>`)
		} else {
			renderWindowLinks(w, r)
			renderAnalyticsFilters(w, r, selected)
			if missingRequiredAnalyticsParam(selected, r) {
				fmt.Fprintln(w, `<p class="parameter-note">Set the required parameters to run this aggregation.</p>`)
			} else if m, err := p.runPaginatedAggregation(r, name); err != nil {
				fmt.Fprintf(w, `<p class="error">%s</p>`, html.EscapeString(err.Error()))
			} else {
				renderAnalyticsTable(w, m)
				renderAnalyticsPagination(w, r, m.Table.Total)
			}
		}
		fmt.Fprintln(w, `</section>`)
	}
	fmt.Fprintln(w, "</main></body></html>")
}

func missingRequiredAnalyticsParam(aggregation analytics.Aggregation, r *http.Request) bool {
	for _, parameter := range aggregation.Params {
		if parameter.Required && r.URL.Query().Get(parameter.Name) == "" {
			return true
		}
	}
	return false
}

func renderAnalyticsFilters(w io.Writer, r *http.Request, aggregation analytics.Aggregation) {
	fmt.Fprint(w, `<form class="analytics-filters" method="get"><label>Since<input type="text" name="since" value="`+html.EscapeString(r.URL.Query().Get("since"))+`" placeholder="30d"></label><label>Rows<input type="number" min="1" max="1000" name="limit" value="`)
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "100"
	}
	fmt.Fprint(w, html.EscapeString(limit)+`"></label>`)
	for _, parameter := range aggregation.Params {
		value := r.URL.Query().Get(parameter.Name)
		if value == "" {
			value = parameter.Default
		}
		fmt.Fprintf(w, `<label>%s<input type="text" name="%s" value="%s"%s></label>`, html.EscapeString(parameter.Name), html.EscapeString(parameter.Name), html.EscapeString(value), map[bool]string{true: " required", false: ""}[parameter.Required])
	}
	fmt.Fprintln(w, `<label class="fresh"><input type="checkbox" name="fresh" value="true"> Refresh now</label><button type="submit">Apply</button></form>`)
}

func analyticsPageURL(r *http.Request, path string, changes map[string]string) string {
	query := r.URL.Query()
	for key, value := range changes {
		if value == "" {
			query.Del(key)
		} else {
			query.Set(key, value)
		}
	}
	return path + "?" + query.Encode()
}

func renderWindowLinks(w io.Writer, r *http.Request) {
	fmt.Fprint(w, `<nav class="window-links" aria-label="Saved windows"><span>Window</span>`)
	for _, window := range []string{"24h", "7d", "30d", "90d"} {
		fmt.Fprintf(w, `<a href="%s">%s</a>`, html.EscapeString(analyticsPageURL(r, r.URL.Path, map[string]string{"since": window, "page": ""})), window)
	}
	fmt.Fprintln(w, `</nav>`)
}

func renderAnalyticsPagination(w io.Writer, r *http.Request, total int) {
	limit := queryParams(r).Limit
	if limit <= 0 {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if total <= limit && page == 1 {
		return
	}
	pages := 1
	if total > 0 {
		pages = 1 + (total-1)/limit
	}
	fmt.Fprint(w, `<nav class="pagination" aria-label="Table pages">`)
	if page > 1 {
		fmt.Fprintf(w, `<a href="%s">← Previous</a>`, html.EscapeString(analyticsPageURL(r, r.URL.Path, map[string]string{"page": strconv.Itoa(page - 1)})))
	}
	fmt.Fprintf(w, `<span>Page %d of %d</span>`, page, pages)
	if page < pages {
		fmt.Fprintf(w, `<a href="%s">Next →</a>`, html.EscapeString(analyticsPageURL(r, r.URL.Path, map[string]string{"page": strconv.Itoa(page + 1)})))
	}
	fmt.Fprintln(w, `</nav>`)
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
