package server

import "net/http"

// oidc_community.go is the community half of the single sign-on seam. The
// settings themselves live in oidc_config.go and are read in every edition, so
// this build can tell an operator that SSO is configured and unavailable
// rather than behaving as though the configuration were a typo.
//
// The provider — discovery, PKCE, token verification — is what this build does
// not ship.

// oidcProvider keeps the configuration so callers can still report on it; it
// performs no protocol work.
type oidcProvider struct{ cfg OIDCConfig }

// newOIDCProvider returns nil, which is the same value server.New sees when no
// issuer is configured. Every caller already handles that case.
func newOIDCProvider(OIDCConfig) *oidcProvider { return nil }

// isOIDCPath claims the same two paths as the enterprise build. Claiming them
// is what makes the difference between the editions legible: an unrouted path
// would fall through to the authenticated router and answer 401 "authentication
// required", which tells a caller nothing about why signing in that way is
// impossible here.
func isOIDCPath(path string) bool {
	return path == "/api/v1/auth/oidc/login" || path == "/api/v1/auth/oidc/callback"
}

// handleOIDC answers 501, which is deliberately not the enterprise build's 404
// "single sign-on is not configured". The two answers separate the two
// situations a caller actually cares about: "this deployment has not set SSO
// up" and "this build does not have SSO". A sign-in screen, the Terraform
// provider or a script can act on the difference.
func (srv *Server) handleOIDC(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"error": "single sign-on is not available in this edition",
	})
}

// oidcLabel has no provider to name.
func oidcLabel(string) string { return "" }
