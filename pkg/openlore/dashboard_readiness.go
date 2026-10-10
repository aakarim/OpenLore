package openlore

import (
	"fmt"
	"strings"

	"github.com/aakarim/go-openlore/assets"
)

// DashboardReadiness reports whether a browser can load dashboard data on this
// instance and, when it cannot, the steps that unblock it. It is derived from
// the same server state dashboardAuth enforces, so the startup banner and the
// dashboard's own error page never disagree about what is missing.
type DashboardReadiness struct {
	// Ready is true when a person can open URL, sign in with a passkey and
	// see data.
	Ready bool
	// URL is the dashboard entry point. Empty when HTTP is disabled.
	URL string
	// Reason says why dashboard data is unavailable. Empty when Ready.
	Reason string
	// NextSteps are the operator actions, in order, that resolve Reason.
	// Usually one; registering a passkey is a short guide. Empty when Ready.
	NextSteps []string
}

// DashboardReadiness evaluates the running server, including the embedded
// frontend of this build.
func (s *Server) DashboardReadiness() DashboardReadiness {
	return s.dashboardReadiness(assets.Dashboard() != nil)
}

func (s *Server) dashboardReadiness(frontendBuilt bool) DashboardReadiness {
	if s.config.HTTPPort <= 0 {
		return DashboardReadiness{
			Reason:    "HTTP is disabled",
			NextSteps: []string{"set `http_port: 8080` in openlore.yml and restart"},
		}
	}
	r := DashboardReadiness{URL: s.config.HTTPBaseURL() + "/dashboard/"}
	if !frontendBuilt {
		r.Reason = "not included in this build (go install/go build are backend-only)"
		r.NextSteps = []string{"install a release binary, or build one with `make distribution` (see docs/dashboard-build.md)"}
		return r
	}
	r.Reason, r.NextSteps = s.dashboardSignInBlocker()
	r.Ready = r.Reason == ""
	return r
}

// dashboardSignInBlocker is the part of readiness an HTTP request can still
// lack: the request itself proves HTTP is on and a client exists, so only
// sign-in configuration can be missing. Both results are empty when a browser
// can sign in.
func (s *Server) dashboardSignInBlocker() (reason string, nextSteps []string) {
	switch {
	case !s.authEnforced:
		return "no authentication is configured; dashboard data needs identities to sign in as",
			[]string{"set `auth_file: ./lore.json` in openlore.yml and restart (see docs/configuration-and-identity.md)"}
	case s.passkeys == nil:
		// Passkeys are on by default, so reaching this state means someone
		// turned them off; say so rather than implying extra setup is needed.
		return "passkey sign-in is turned off (`passkeys.enabled: false` in openlore.yml)",
			[]string{"remove `passkeys.enabled: false` from openlore.yml (passkeys are on by default) and restart"}
	case s.passkeys.CredentialCount() == 0:
		return "no passkey is registered yet", s.passkeyRegistrationSteps()
	}
	return "", nil
}

// passkeyRegistrationSteps is the whole path from an empty passkey store to a
// signed-in browser. It names an identity from lore.json when one exists so
// the printed command can be run as-is.
func (s *Server) passkeyRegistrationSteps() []string {
	var steps []string
	identity := "<identity>"
	if auth := s.currentAuth(); auth != nil && len(auth.Identities) > 0 {
		identity = auth.Identities[0].Name
	} else {
		steps = append(steps, "add an identity to sign in as: `openlore identity add --name <identity> --auth ./lore.json`, then restart")
	}
	origins := strings.Join(s.config.Passkeys.RPOrigins, ", ")
	if origins == "" {
		origins = s.config.HTTPBaseURL()
	}
	return append(steps,
		fmt.Sprintf("from any SSH shell, run `ssh %s passkey register --identity %s` (any identity in lore.json, with or without an SSH key)", s.config.SSHTarget(), identity),
		fmt.Sprintf("within 5 minutes, open the printed link in a browser on the device whose passkey you want to use; the link must be opened at an origin listed in `passkeys.rp_origins` (currently %s)", origins),
		fmt.Sprintf("open %s and sign in with that passkey; the session lasts `passkeys.session_ttl`", s.config.HTTPBaseURL()+"/dashboard/"),
	)
}
