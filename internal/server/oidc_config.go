package server

import (
	"os"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// oidc_config.go holds the single sign-on settings and their parsing, kept
// apart from the protocol implementation in oidc.go on purpose: the settings
// are inert data that every edition can read and report on, while the provider
// that acts on them is not part of every edition. Splitting them here is what
// lets the community build answer "SSO is configured but unavailable" instead
// of pretending the configuration does not exist.

// OIDCConfig is the provider configuration, read from the environment.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// RoleClaim names the id_token claim carrying group or role membership.
	RoleClaim string
	// RoleMap maps a value of that claim to a local role, e.g.
	// "sre-admins:admin,sre:responder". The *highest* role among the matching
	// values wins: someone in both "sre" and "sre-admins" is an admin.
	RoleMap map[string]string
	// DefaultRole applies when no mapping matched. Empty means "no access",
	// which is the safe default: a provider that authenticates the whole
	// company must not thereby grant everyone a role here.
	DefaultRole string
	// AllowSignup provisions a local user on first sign-in. With it off, a
	// person must already exist locally, which is how a deployment keeps the
	// roster and the identity provider deliberately separate.
	AllowSignup bool
}

// Enabled reports whether OIDC is configured.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.RedirectURL != ""
}

// oidcConfigFromEnv reads the single sign-on settings.
func oidcConfigFromEnv() OIDCConfig {
	cfg := OIDCConfig{
		Issuer:       strings.TrimSpace(os.Getenv("NXS_ANOMALY_OIDC_ISSUER")),
		ClientID:     strings.TrimSpace(os.Getenv("NXS_ANOMALY_OIDC_CLIENT_ID")),
		ClientSecret: os.Getenv("NXS_ANOMALY_OIDC_CLIENT_SECRET"),
		RedirectURL:  strings.TrimSpace(os.Getenv("NXS_ANOMALY_OIDC_REDIRECT_URL")),
		RoleClaim:    strings.TrimSpace(os.Getenv("NXS_ANOMALY_OIDC_ROLE_CLAIM")),
		RoleMap:      parseRoleMap(os.Getenv("NXS_ANOMALY_OIDC_ROLE_MAP")),
		// Empty by default, meaning "no access": a provider that authenticates
		// the whole company must not thereby grant everyone a role here.
		DefaultRole: strings.TrimSpace(os.Getenv("NXS_ANOMALY_OIDC_DEFAULT_ROLE")),
		AllowSignup: os.Getenv("NXS_ANOMALY_OIDC_ALLOW_SIGNUP") == "true",
	}
	if raw := strings.TrimSpace(os.Getenv("NXS_ANOMALY_OIDC_SCOPES")); raw != "" {
		cfg.Scopes = append(cfg.Scopes, strings.Fields(strings.ReplaceAll(raw, ",", " "))...)
	}
	if cfg.RoleClaim == "" {
		cfg.RoleClaim = "groups"
	}
	return cfg
}

// parseRoleMap parses "group1:admin,group2:responder" into a claim-value→role
// map. Unlike API keys, an unrecognised role here is dropped rather than
// degraded to viewer: a mapping that does not name a real role should grant
// nothing, and the DefaultRole (also possibly nothing) then applies.
func parseRoleMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		i := strings.LastIndex(part, ":")
		if i <= 0 {
			continue
		}
		group := strings.TrimSpace(part[:i])
		role := authz.ParseRole(strings.TrimSpace(part[i+1:]))
		if group != "" && role.Valid() {
			out[group] = role.String()
		}
	}
	return out
}
