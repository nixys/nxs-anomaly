package utils

import (
	"os"
	"strings"
)

// ProductionProfile reports whether NXS_ANOMALY_PROFILE selects the hardened
// production defaults (SSRF guard on, delivery circuit breaker on, request rate
// limits on, inline secrets refused). Explicit per-setting env vars still
// override the profile, so an operator can opt back out of any single default.
func ProductionProfile() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("NXS_ANOMALY_PROFILE")), "production")
}

// secretRefPrefix marks a value that points at an environment variable rather
// than carrying the secret inline, e.g. "env:NXS_ANOMALY_INTEGRATION_ACME_HMAC".
const secretRefPrefix = "env:"

// IsSecretRef reports whether a stored secret value is an env reference rather
// than an inline plaintext secret.
func IsSecretRef(value string) bool {
	return strings.HasPrefix(value, secretRefPrefix)
}

// ResolveSecretRef returns the effective secret for a stored value. An
// "env:VAR" reference is resolved from the environment at use time (so the
// secret never sits in the database); any other value is returned as-is, which
// keeps legacy inline secrets working after an upgrade.
func ResolveSecretRef(value string) string {
	if ref, ok := strings.CutPrefix(value, secretRefPrefix); ok {
		return os.Getenv(strings.TrimSpace(ref))
	}
	return value
}
