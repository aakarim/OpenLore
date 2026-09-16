package openlore

import (
	"net/http"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
)

func (s *Server) analyticsEvent(id Identity, eventType string, fields map[string]any) analytics.Event {
	principal := id.attribution().Principal
	if principal == "" {
		principal = "anonymous"
	}
	if fields == nil {
		fields = map[string]any{}
	}
	if _, exists := fields["actor_kind"]; !exists {
		fields["actor_kind"] = string(classifyAttribution(id.attribution()))
	}
	return analytics.Event{ID: analytics.NewID(), Time: time.Now().UTC(), Type: eventType, Principal: principal, Actor: id.attribution().Actor, Transport: id.Transport, SessionID: id.SessionID, ClientSessionID: id.ClientSessionID, InvocationID: analytics.NewID(), RemoteAddr: id.RemoteAddr, Fields: fields}
}

func (s *Server) transportMiddleware(next http.Handler, transport string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.identityFromContext(r.Context())
		id.Transport = transport
		id.RemoteAddr = r.RemoteAddr
		id.SessionID = generateSessionID()
		id.ClientSessionID = ""
		if transport == "mcp" {
			id.ClientSessionID = r.Header.Get("Mcp-Session-Id")
		} else if strings.Contains(r.URL.Path, "/sessions/") {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			for i, part := range parts {
				if part == "sessions" && i+1 < len(parts) {
					id.ClientSessionID = parts[i+1]
					id.SessionID = parts[i+1]
					break
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
	})
}
