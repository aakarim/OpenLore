package openlore

import (
	"errors"
	"net/http"
	"strings"
)

// authMiddleware wraps an HTTP handler with posture-aware bearer verification.
// The caller supplies whether a token is required so transports may override
// the SSH-derived default.
//
//   - No issuer configured → no-op; callers resolve to anonymous (Phase 0).
//   - Optional-token posture → verify a token if present (reject if invalid);
//     if absent, proceed anonymously.
//   - Required-token posture → a valid token is required; missing or invalid
//     returns 401. Unknown identity under `unknown_identity: deny` returns 403.
//
// A verified token is resolved to an Identity and stored on the request context
// via contextWithIdentity, so the shared shellForContext scopes the tool call
// exactly as an SSH session (docs/mcp-bearer-auth.md §4, §6).
func (s *Server) authMiddleware(next http.Handler, required bool) http.Handler {
	if s.issuer == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)

		if token == "" {
			if required {
				w.Header().Set("WWW-Authenticate", s.bearerChallenge(""))
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			// Optional posture: proceed; identityFromContext yields anonymous.
			next.ServeHTTP(w, r)
			return
		}

		claims, err := s.issuer.Verify(token)
		if err != nil {
			// A present-but-invalid token is always rejected, even keyless (§4).
			w.Header().Set("WWW-Authenticate", s.bearerChallenge(`error="invalid_token"`))
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		id, err := s.identityStore.Resolve(r.Context(), claims)
		if err != nil {
			if errors.Is(err, ErrUnknownIdentity) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			http.Error(w, "identity resolution failed", http.StatusInternalServerError)
			return
		}

		next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
	})
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	fields := strings.Fields(h)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return ""
	}
	return fields[1]
}
