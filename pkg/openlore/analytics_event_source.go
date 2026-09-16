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
	return &dashboardEventSource{server: s, identity: id, prefix: prefix, source: s.analytics.EventSource()}
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
	// Resource attribution can follow an event in the append-only log, so retain
	// the source snapshot long enough to establish complete invocation/session
	// scope before streaming authorized requested events from that snapshot.
	byParent := map[string]analyticsAccess{}
	byInvocation := map[string]analyticsAccess{}
	var events []analytics.Event
	var direct []analyticsAccess
	if err := d.source.Scan(ctx, analytics.EventFilter{}, func(event analytics.Event) error {
		events = append(events, event)
		access := d.directAccess(event, prefix)
		direct = append(direct, access)
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

	eventAccess := make([]analyticsAccess, len(events))
	for i, event := range events {
		access := direct[i]
		if !access.proved && analyticsCommandEvent(event.Type) {
			access = mergeAnalyticsAccess(byParent[event.ID], byInvocation[event.InvocationID])
		}
		eventAccess[i] = access
	}
	bySession := map[string]analyticsAccess{}
	for i, event := range events {
		if event.SessionID == "" || event.Type == "session.start" || event.Type == "session.end" || event.Type == "auth.login" {
			continue
		}
		access := eventAccess[i]
		current := bySession[event.SessionID]
		// An unproved command or event makes the session ambiguous.
		current.add(access.proved && access.allowed)
		bySession[event.SessionID] = current
	}
	types := make(map[string]bool, len(filter.Types))
	for _, eventType := range filter.Types {
		types[eventType] = true
	}
	principals := make(map[string]bool, len(filter.Principals))
	for _, principal := range filter.Principals {
		principals[principal] = true
	}
	for i, event := range events {
		if !filter.From.IsZero() && event.Time.Before(filter.From) || !filter.To.IsZero() && event.Time.After(filter.To) {
			continue
		}
		if len(types) > 0 && !types[event.Type] {
			continue
		}
		if len(principals) > 0 && !principals[event.Principal] {
			continue
		}
		access := eventAccess[i]
		if event.Type == "session.start" || event.Type == "session.end" || event.Type == "auth.login" {
			access = bySession[event.SessionID]
		}
		if access.proved && access.allowed {
			if err := fn(d.canonicalEvent(event)); err != nil {
				return err
			}
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
