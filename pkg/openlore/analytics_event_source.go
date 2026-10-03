package openlore

import (
	"context"

	"github.com/aakarim/go-openlore/internal/analytics"
)

// DashboardEventSource returns analytics restricted to the caller's current,
// readable canonical docsets and prefix. Authorization is evaluated on every
// Scan, so retained events follow live ACLs and deleted paths remain visible.
// Events whose resource scope cannot be proved (including legacy unscoped
// commands and mixed-scope searches) are deliberately omitted.
func (s *Server) DashboardEventSource(id Identity, prefix string) analytics.EventSource {
	if s == nil || s.analytics == nil {
		return deniedAnalyticsSource{}
	}
	return &dashboardEventSource{server: s, identity: id, prefix: prefix, source: s.analytics.IndexedEventSource()}
}

type deniedAnalyticsSource struct{}

func (deniedAnalyticsSource) Scan(context.Context, analytics.EventFilter, func(analytics.Event) error) error {
	return nil
}

type dashboardEventSource struct {
	server   *Server
	identity Identity
	prefix   string
	source   analytics.EventSource
}

type analyticsAccess struct {
	proved  bool
	allowed bool
}

func (a *analyticsAccess) add(allowed bool) {
	if !a.proved {
		a.allowed = true
	}
	a.proved = true
	a.allowed = a.allowed && allowed
}

func mergeAnalyticsAccess(values ...analyticsAccess) analyticsAccess {
	var merged analyticsAccess
	for _, value := range values {
		if !value.proved {
			continue
		}
		if !merged.proved {
			merged.allowed = true
		}
		merged.proved = true
		merged.allowed = merged.allowed && value.allowed
	}
	return merged
}

func (d *dashboardEventSource) Scan(ctx context.Context, filter analytics.EventFilter, fn func(analytics.Event) error) error {
	if d.source == nil || d.server == nil || d.identity.IdentityName == "" || d.identity.IdentityName == "guest" {
		return nil
	}
	// A source may outlive the HTTP request that created it. Discard any
	// session snapshot, and resolve policy once for this scan without mutating
	// a source concurrently used by another aggregation.
	current := *d
	current.identity.policySnapshot = nil
	policy, err := d.server.currentPolicy(current.identity)
	if err != nil {
		return nil
	}
	current.identity.policySnapshot = &policy
	d = &current
	prefix := d.server.canonicalPath(d.prefix)
	if d.prefix == "" {
		prefix = "/"
	}
	// Resource attribution can follow an event in the append-only log. Rather
	// than buffering the window, stream it in passes: first prove command
	// correlations, then session scope, then emit authorized events. Memory is
	// bounded by correlation IDs, not by the number of events in the window.
	//
	// Every pass must observe the same events: an event appended between
	// passes could otherwise be emitted using correlations computed without
	// it. A time cutoff is not enough, because events are timestamped before
	// they are written. Sources that cannot pin a snapshot are buffered.
	source, release, err := snapshotEventSource(ctx, d.source, analytics.EventFilter{From: filter.From, To: filter.To})
	if err != nil {
		return err
	}
	defer release()
	to := filter.To
	window := analytics.EventFilter{From: filter.From, To: to}
	byParent := map[string]analyticsAccess{}
	byInvocation := map[string]analyticsAccess{}
	if err := source.Scan(ctx, window, func(event analytics.Event) error {
		access := d.directAccess(event, prefix)
		if access.proved {
			if event.ParentID != "" {
				current := byParent[event.ParentID]
				current.add(access.allowed)
				byParent[event.ParentID] = current
			}
			if event.InvocationID != "" {
				current := byInvocation[event.InvocationID]
				current.add(access.allowed)
				byInvocation[event.InvocationID] = current
			}
		}
		return nil
	}); err != nil {
		return err
	}
	eventAccess := func(event analytics.Event) analyticsAccess {
		access := d.directAccess(event, prefix)
		if !access.proved && analyticsCommandEvent(event.Type) {
			access = mergeAnalyticsAccess(byParent[event.ID], byInvocation[event.InvocationID])
		}
		return access
	}
	types := make(map[string]bool, len(filter.Types))
	for _, eventType := range filter.Types {
		types[eventType] = true
	}
	sessionEvent := func(eventType string) bool {
		return eventType == "session.start" || eventType == "session.end" || eventType == "auth.login"
	}
	bySession := map[string]analyticsAccess{}
	if len(types) == 0 || types["session.start"] || types["session.end"] || types["auth.login"] {
		if err := source.Scan(ctx, window, func(event analytics.Event) error {
			if event.SessionID == "" || sessionEvent(event.Type) {
				return nil
			}
			access := eventAccess(event)
			current := bySession[event.SessionID]
			// An unproved command or event makes the session ambiguous.
			current.add(access.proved && access.allowed)
			bySession[event.SessionID] = current
			return nil
		}); err != nil {
			return err
		}
	}
	principals := make(map[string]bool, len(filter.Principals))
	for _, principal := range filter.Principals {
		principals[principal] = true
	}
	return source.Scan(ctx, window, func(event analytics.Event) error {
		if !filter.From.IsZero() && event.Time.Before(filter.From) || !to.IsZero() && event.Time.After(to) {
			return nil
		}
		if len(types) > 0 && !types[event.Type] {
			return nil
		}
		if len(principals) > 0 && !principals[event.Principal] {
			return nil
		}
		var access analyticsAccess
		if sessionEvent(event.Type) {
			access = bySession[event.SessionID]
		} else {
			access = eventAccess(event)
		}
		if access.proved && access.allowed {
			return fn(d.canonicalEvent(event))
		}
		return nil
	})
}

// snapshotEventSource returns a source whose scans all observe the same
// events. Sources without native snapshots are buffered once; they are only
// used by embedders without durable SQLite analytics.
func snapshotEventSource(ctx context.Context, source analytics.EventSource, window analytics.EventFilter) (analytics.EventSource, func(), error) {
	if snapshots, ok := source.(analytics.SnapshotEventSource); ok {
		snapshot, err := snapshots.Snapshot(ctx)
		if err != nil {
			return nil, nil, err
		}
		return snapshot, func() { _ = snapshot.Close() }, nil
	}
	var events bufferedEventSource
	if err := source.Scan(ctx, window, func(event analytics.Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return events, func() {}, nil
}

type bufferedEventSource []analytics.Event

func (s bufferedEventSource) Scan(ctx context.Context, filter analytics.EventFilter, fn func(analytics.Event) error) error {
	for _, event := range s {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !filter.From.IsZero() && event.Time.Before(filter.From) || !filter.To.IsZero() && event.Time.After(filter.To) {
			continue
		}
		if err := fn(event); err != nil {
			return err
		}
	}
	return nil
}

func analyticsCommandEvent(eventType string) bool {
	switch eventType {
	case "command.exec", "command.unknown", "syntax.unknown":
		return true
	default:
		return false
	}
}

func (d *dashboardEventSource) canonicalEvent(event analytics.Event) analytics.Event {
	fields := make(map[string]any, len(event.Fields))
	for key, value := range event.Fields {
		fields[key] = value
	}
	if value, ok := fields["path"].(string); ok {
		fields["path"] = d.server.canonicalPath(value)
	}
	if values, ok := analyticsStringSlice(fields["scope"]); ok {
		scope := make([]string, len(values))
		for i, value := range values {
			scope[i] = d.server.canonicalPath(value)
		}
		fields["scope"] = scope
	}
	event.Fields = fields
	return event
}

func (d *dashboardEventSource) directAccess(event analytics.Event, prefix string) analyticsAccess {
	var access analyticsAccess
	if rawPath, exists := event.Fields["path"]; exists {
		path, ok := rawPath.(string)
		access.add(ok && path != "" && d.pathReadable(path, prefix))
	}
	if rawScope, exists := event.Fields["scope"]; exists {
		scope, ok := analyticsStringSlice(rawScope)
		if !ok || len(scope) == 0 {
			access.add(false)
		} else {
			for _, target := range scope {
				access.add(d.subtreeReadable(target, prefix))
			}
		}
	}
	return access
}

func analyticsStringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return values, true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func (d *dashboardEventSource) pathReadable(candidate, prefix string) bool {
	candidate = d.server.canonicalPath(candidate)
	if !pathWithinRoot(prefix, candidate) {
		return false
	}
	docset, grants, ok := d.server.grantsForPath(d.identity, candidate)
	if !ok {
		return false
	}
	for _, grant := range grants {
		if grant.CanRead(docset, candidate) {
			return true
		}
	}
	return false
}

func (d *dashboardEventSource) subtreeReadable(candidate, prefix string) bool {
	candidate = d.server.canonicalPath(candidate)
	// A scope broader than the selected prefix can describe sibling resources
	// and therefore cannot be safely represented by this view.
	if !pathWithinRoot(prefix, candidate) || !d.wholeDocsetGrant(candidate) {
		return false
	}
	for _, root := range d.server.allDocsetRoots() {
		root = d.server.canonicalPath(root)
		if root != candidate && pathWithinRoot(candidate, root) && (!pathWithinRoot(prefix, root) || !d.wholeDocsetGrant(root)) {
			return false
		}
	}
	return true
}

func (d *dashboardEventSource) wholeDocsetGrant(candidate string) bool {
	docset, grants, ok := d.server.grantsForPath(d.identity, candidate)
	if !ok {
		return false
	}
	for _, grant := range grants {
		// Core ro/rw grants cover the complete governing docset. A plugin grant
		// can be path-sensitive and exposes no enumerable boundary, so it cannot
		// prove a search target's entire subtree and is omitted fail-closed.
		if (grant.Name() == "ro" || grant.Name() == "rw") && grant.CanRead(docset, candidate) {
			return true
		}
	}
	return false
}

var _ analytics.EventSource = (*dashboardEventSource)(nil)
var _ analytics.EventSource = deniedAnalyticsSource{}
