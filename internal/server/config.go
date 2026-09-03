package server

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Config holds server configuration.
type Config struct {
	Addr    string
	TLSCert string
	TLSKey  string
	APIKey  string
	// BareAPIKeys counts entries in NXS_ANOMALY_API_KEYS given without a role.
	// They resolve to viewer; the count exists so startup can say so, because
	// they used to mean admin and the change is otherwise silent.
	BareAPIKeys int
	// APIKeys maps a token to its role. Populated from NXS_ANOMALY_API_KEYS; the
	// legacy single APIKey is added here as "admin". The pre-role scope names are
	// still accepted: "readonly" is read as "viewer". An entry with no role is
	// viewer, not admin — see parseAPIKeys.
	APIKeys map[string]string
	// AllowAnonymous keeps the pre-RBAC behaviour where a deployment with no keys
	// configured served the management API to anyone. It is off by default: an
	// unconfigured deployment now refuses management calls instead of handing out
	// admin. Intended for local development only.
	AllowAnonymous bool
	// SessionTTL is how long a browser session stays valid after sign-in. It is
	// an absolute lifetime, not an idle timeout: refreshing on every request
	// would mean a database write per request, and a fixed ceiling is the
	// property that actually bounds a stolen cookie.
	SessionTTL time.Duration
	// SessionCookieSecure sets the Secure attribute on the session cookie. It
	// defaults to true; deployments serving plain HTTP on localhost turn it off
	// explicitly, which is the right way round for a security attribute.
	SessionCookieSecure bool
	// OIDC configures single sign-on. Zero value means disabled.
	OIDC OIDCConfig
	// TeamScoping limits a signed-in user's view to the objects owned by their
	// teams, plus the unassigned ones. It is off by default and is a
	// deployment decision, not a per-user one: switching it on changes what
	// every responder sees, and the safe moment to do that is chosen by an
	// operator. Because unassigned objects stay visible to everyone, turning it
	// on before any team owns anything changes nothing.
	TeamScoping bool
	// BootstrapAdminUsername/Password create or update a password admin at
	// startup. This is the break-glass path: without it, a fresh database has
	// no user anyone can sign in as, and roles could only be handed out by an
	// API key.
	BootstrapAdminUsername string
	BootstrapAdminPassword string
	PollInterval           time.Duration
	ShutdownTimeout        time.Duration // how long to wait for graceful shutdown; defaults to 10s
	// WorkerAddr is where the standalone run-worker serves /live, /ready and
	// /metrics. Separate from Addr so an API and a worker can run in one pod
	// without a port clash. Defaults to :8081.
	WorkerAddr string
	// WorkerStallTimeout is how long the worker may go without completing a
	// cycle before /ready reports it degraded. Zero means derive it from the
	// poll interval (see defaultWorkerStallTimeout).
	WorkerStallTimeout time.Duration
	WebhookRate        float64
	APIRate            float64
	StartScheduler     bool
	TrustedProxies     []string // CIDR ranges whose X-Forwarded-For headers are trusted
	// HTTP server timeouts. They bound how long a (possibly malicious or stuck)
	// client may tie up a connection: ReadHeaderTimeout is the slowloris guard.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// ConfigFromEnv builds a Config from environment variables.
func ConfigFromEnv() Config {
	addr := os.Getenv("NXS_ANOMALY_ADDR")
	if addr == "" {
		host := os.Getenv("NXS_ANOMALY_LISTEN_HOST")
		if host == "" {
			host = "0.0.0.0"
		}
		port := os.Getenv("NXS_ANOMALY_LISTEN_PORT")
		if port == "" {
			port = "8080"
		}
		addr = host + ":" + port
	}
	pollInterval := 5 * time.Second
	if s := os.Getenv("NXS_ANOMALY_POLL_INTERVAL"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			pollInterval = time.Duration(n) * time.Second
		}
	}
	workerAddr := os.Getenv("NXS_ANOMALY_WORKER_ADDR")
	if workerAddr == "" {
		workerAddr = ":8081"
	}
	// Rate limits are off by default but on in the production profile, where an
	// unlimited public ingest/API surface is a denial-of-service foothold. An
	// explicit env value (including "0" to force-disable) still wins.
	prod := utils.ProductionProfile()
	webhookRate := rateFromEnv("NXS_ANOMALY_WEBHOOK_RATE", prod, 50)
	apiRate := rateFromEnv("NXS_ANOMALY_API_RATE", prod, 20)
	shutdownTimeout := 10 * time.Second
	if s := os.Getenv("NXS_ANOMALY_SHUTDOWN_TIMEOUT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			shutdownTimeout = time.Duration(n) * time.Second
		}
	}
	var trustedProxies []string
	if raw := os.Getenv("NXS_ANOMALY_TRUSTED_PROXIES"); raw != "" {
		for _, cidr := range strings.Split(raw, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr != "" {
				trustedProxies = append(trustedProxies, cidr)
			}
		}
	}
	apiKey := os.Getenv("NXS_ANOMALY_API_KEY")
	apiKeys, bareKeys := parseAPIKeys(os.Getenv("NXS_ANOMALY_API_KEYS"))
	// The legacy single key is always an admin key.
	if apiKey != "" {
		if apiKeys == nil {
			apiKeys = map[string]string{}
		}
		apiKeys[apiKey] = scopeAdmin
	}

	return Config{
		Addr:               addr,
		TLSCert:            os.Getenv("NXS_ANOMALY_TLS_CERT"),
		TLSKey:             os.Getenv("NXS_ANOMALY_TLS_KEY"),
		APIKey:             apiKey,
		APIKeys:            apiKeys,
		BareAPIKeys:        bareKeys,
		PollInterval:       pollInterval,
		ShutdownTimeout:    shutdownTimeout,
		WorkerAddr:         workerAddr,
		WorkerStallTimeout: envDurationSeconds("NXS_ANOMALY_WORKER_STALL_TIMEOUT_SECONDS", 0),
		WebhookRate:        webhookRate,
		APIRate:            apiRate,
		AllowAnonymous:     os.Getenv("NXS_ANOMALY_ALLOW_ANONYMOUS") == "true",
		SessionTTL:         envDurationSeconds("NXS_ANOMALY_SESSION_TTL_SECONDS", 12*time.Hour),
		// Note the comparison: the attribute is dropped only on an explicit
		// "false", so a typo in the variable leaves the cookie Secure.
		SessionCookieSecure:    os.Getenv("NXS_ANOMALY_SESSION_COOKIE_SECURE") != "false",
		OIDC:                   oidcConfigFromEnv(),
		TeamScoping:            os.Getenv("NXS_ANOMALY_TEAM_SCOPING") == "true",
		BootstrapAdminUsername: os.Getenv("NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME"),
		BootstrapAdminPassword: os.Getenv("NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD"),
		StartScheduler:         os.Getenv("NXS_ANOMALY_START_SCHEDULER") != "false",
		TrustedProxies:         trustedProxies,
		ReadHeaderTimeout:      envDurationSeconds("NXS_ANOMALY_HTTP_READ_HEADER_TIMEOUT_SECONDS", 5*time.Second),
		ReadTimeout:            envDurationSeconds("NXS_ANOMALY_HTTP_READ_TIMEOUT_SECONDS", 15*time.Second),
		WriteTimeout:           envDurationSeconds("NXS_ANOMALY_HTTP_WRITE_TIMEOUT_SECONDS", 30*time.Second),
		IdleTimeout:            envDurationSeconds("NXS_ANOMALY_HTTP_IDLE_TIMEOUT_SECONDS", 60*time.Second),
	}
}

// rateFromEnv reads a per-second rate limit. An explicit env value wins,
// including "0" to force-disable; otherwise the production profile applies
// prodDefault and non-production disables the limit.
func rateFromEnv(key string, prod bool, prodDefault float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	if prod {
		return prodDefault
	}
	return 0
}

// envDurationSeconds reads an integer number of seconds from env, returning def
// when unset or non-positive.
func envDurationSeconds(key string, def time.Duration) time.Duration {
	if s := os.Getenv(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

// scopeAdmin is the role assigned to a bare key (one given with no role) and to
// the legacy single NXS_ANOMALY_API_KEY.
const scopeAdmin = string(authz.RoleAdmin)

// parseAPIKeys parses "key1:admin,key2:viewer" into a token→role map, and
// reports how many entries carried no role.
//
// A bare "key" used to mean admin. It no longer does: an automation credential
// must state what it may do, and inheriting the widest role from an omission is
// the opposite of that. Bare keys now resolve to viewer, and the caller warns
// about them at startup.
//
// Viewer rather than rejection, for the same reason an unrecognised role
// degrades instead of failing: a mistake in deployment config should narrow
// access, not widen it and not take the service down. The failure mode is a
// 403 on writes, which points at the key; the alternative was silent admin.
//
// NXS_ANOMALY_API_KEY is unaffected — that variable's entire meaning is "the
// single admin key", which is an explicit scope, just spelled in the name.
func parseAPIKeys(raw string) (map[string]string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, 0
	}
	keys := map[string]string{}
	bare := 0
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		token, role := part, ""
		if i := strings.LastIndex(part, ":"); i > 0 {
			token = strings.TrimSpace(part[:i])
			role = strings.TrimSpace(part[i+1:])
		}
		if token == "" {
			continue
		}
		if role == "" {
			bare++
		}
		parsed := authz.ParseRole(role)
		if !parsed.Valid() {
			parsed = authz.RoleViewer
		}
		keys[token] = parsed.String()
	}
	return keys, bare
}
