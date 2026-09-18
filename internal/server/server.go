// Package server implements the nxs-anomaly HTTP API and background worker loop.
//
// File layout:
//   - server.go         — Server struct, lifecycle (New), worker loop, route registration
//   - server_api.go     — /api/v1 router, auth, middleware, HTTP I/O helpers
//   - handlers.go       — /live, /health, /metrics, /api/v1 entry point
//   - handlers_ingest.go — webhook ingestion handlers (6 sources)
//   - metrics.go        — Prometheus counters/gauges and worker-cycle update
//   - rate_limiter.go   — per-key token-bucket rate limiter
//   - config.go         — Config + ConfigFromEnv + API key scope parsing
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
)

// Version is injected at build time via -ldflags "-X github.com/nixys/nxs-anomaly/internal/server.Version=x.y.z".
var Version = "dev"

const maxRequestBody = 4 * 1024 * 1024 // 4 MiB

// errWebhookSigInvalid is returned by verifyWebhookSig when the signature doesn't match (400).
// Any other error from verifyWebhookSig is a DB/internal error (500).
var errWebhookSigInvalid = fmt.Errorf("webhook signature validation failed")

// Server is the HTTP server wrapping the engine.
type Server struct {
	eng     *engine.Engine
	store   store.PostgreSQLStore
	metrics *Metrics
	cfg     Config
	// webhookLimiter and apiLimiter stay per-process on purpose: they sit on the
	// hot path, where a database round-trip per request would cost more than the
	// protection is worth. The configured rate is therefore per replica — the
	// chart divides the operator's cluster-wide figure by replicaCount.
	webhookLimiter *rateLimiter
	apiLimiter     *rateLimiter
	// chatopsInbound holds the credentials that let a chat platform prove a
	// slash command really came from it. Empty means the inbound endpoints
	// refuse to run rather than accepting unsigned commands.
	chatopsInbound chatopsInboundConfig
	// loginLimiter throttles password attempts per IP. It is always present,
	// unlike the other two: a password endpoint without a rate limit is a
	// brute-force target, so this one is not optional. It is also the one
	// limiter backed by PostgreSQL rather than a per-process map, so the limit
	// stays what it says it is regardless of replica count — see rate_limiter.go.
	loginLimiter limiter
	// loginAccountLimiter throttles failed password attempts per account. The
	// per-IP budget cannot bound an attacker who chooses its own address; this
	// one bounds the guessing itself. Also backed by PostgreSQL, so the budget
	// is per deployment and not per replica.
	loginAccountLimiter limiter
	// oidc is nil when single sign-on is not configured.
	oidc           *oidcProvider
	startTime      time.Time
	trustedProxies []*net.IPNet
	cycleRunning   atomic.Bool // guards against overlapping worker cycles
	heartbeat      *heartbeatPinger
	reqIDCounter   atomic.Uint64
	// health ping cache (5s TTL)
	pingOK           atomic.Bool
	pingCheckedAt    atomic.Int64 // unix nanoseconds
	kafkaOutboxDepth atomic.Int64 // cached kafka outbox depth from ping check
	kafkaOK          atomic.Bool  // false if outbox depth > threshold
}

// New builds and starts the HTTP server. It blocks until the process receives SIGTERM or SIGINT.
func New(ctx context.Context, s store.PostgreSQLStore, eng *engine.Engine, cfg Config) error {
	// Reference-cache staleness only needs to hold until the next worker tick.
	eng.SetReferenceCacheTTL(cfg.PollInterval)
	var trustedProxies []*net.IPNet
	for _, cidr := range cfg.TrustedProxies {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			slog.Warn("invalid trusted proxy CIDR, skipping", "cidr", cidr, "err", err)
			continue
		}
		trustedProxies = append(trustedProxies, ipNet)
	}
	srv := &Server{
		eng:                 eng,
		store:               s,
		metrics:             newMetrics(),
		cfg:                 cfg,
		startTime:           time.Now(),
		trustedProxies:      trustedProxies,
		loginLimiter:        newDBRateLimiter(s, "login:", loginRatePerSecond, loginBurst),
		loginAccountLimiter: newDBRateLimiter(s, "login-account:", loginAccountRatePerSecond, loginAccountBurst),
		chatopsInbound:      chatopsInboundConfigFromEnv(),
	}
	// Wire engine-emitted metrics (delivery latency, dead-letters, breaker skips)
	// into this server's Prometheus registry.
	eng.SetMetricsSink(srv.metrics)
	if cfg.OIDC.Enabled() {
		srv.oidc = newOIDCProvider(cfg.OIDC)
		slog.Info("oidc_enabled", "issuer", cfg.OIDC.Issuer,
			"role_claim", cfg.OIDC.RoleClaim, "allow_signup", cfg.OIDC.AllowSignup,
			"default_role", cfg.OIDC.DefaultRole)
		if cfg.OIDC.DefaultRole == "" && len(cfg.OIDC.RoleMap) == 0 {
			// Every sign-in would authenticate and then be refused. Worth
			// saying plainly, because from the browser it looks like broken SSO.
			slog.Warn("oidc_grants_nothing",
				"reason", "neither NXS_ANOMALY_OIDC_ROLE_MAP nor NXS_ANOMALY_OIDC_DEFAULT_ROLE is set",
				"effect", "every single sign-on attempt will be refused for lack of a role")
		}
	}
	if err := srv.bootstrapAdmin(ctx); err != nil {
		// Deliberately fatal. An operator who asked for a break-glass admin and
		// did not get one would otherwise find out only when locked out.
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	// Make an unauthenticated deployment impossible to run by accident: it now
	// requires an explicit opt-in, and says so on every start.
	switch {
	case cfg.AllowAnonymous:
		slog.Warn("management_api_unauthenticated",
			"reason", "NXS_ANOMALY_ALLOW_ANONYMOUS=true",
			"effect", "every management API request is granted the admin role")
	case len(cfg.APIKeys) == 0 && !srv.hasPasswordUsers(ctx):
		slog.Warn("management_api_locked",
			"reason", "no API keys and no user can sign in",
			"effect", "management API rejects every request; set NXS_ANOMALY_API_KEYS "+
				"or NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME/_PASSWORD")
	}
	if cfg.BareAPIKeys > 0 {
		// These used to be admin keys. Saying so is the whole point: the change
		// is otherwise invisible until a write starts returning 403.
		slog.Warn("api_keys_without_a_role",
			"count", cfg.BareAPIKeys,
			"effect", "keys given without \":role\" in NXS_ANOMALY_API_KEYS now resolve to viewer, not admin",
			"remedy", "state the role explicitly, e.g. NXS_ANOMALY_API_KEYS=tok:admin")
	}
	if cfg.WebhookRate > 0 {
		srv.webhookLimiter = newRateLimiter(cfg.WebhookRate, cfg.WebhookRate)
	}
	if cfg.APIRate > 0 {
		srv.apiLimiter = newRateLimiter(cfg.APIRate, cfg.APIRate)
	}

	// The listener is normally already open — cmdServe opens it before the
	// store so /live answers while the database and migrations are awaited.
	fd := cfg.Frontdoor
	if fd == nil {
		var err error
		if fd, err = OpenFrontdoor(cfg.Addr, cfg, cfg.TLSCert, cfg.TLSKey); err != nil {
			return err
		}
	}
	fd.SetHandler(srv.handler())
	slog.Info("server started", "addr", cfg.Addr)

	shutdownTimeout := cfg.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 10 * time.Second
	}

	var workerCancel context.CancelFunc
	if !cfg.StartScheduler {
		// No worker cycle runs here, so nothing would ever refresh the process
		// gauges and nxs_anomaly_db_up would stay at its 0 zero-value while the
		// database is perfectly reachable. Refresh them on a ticker instead; the
		// cluster-wide backlog gauges stay the worker's job so API replicas do
		// not duplicate them.
		gaugeCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go srv.runGaugeRefreshLoop(gaugeCtx)
	}
	if cfg.StartScheduler {
		srv.heartbeat = newHeartbeatPinger(cfg.WorkerHeartbeatURL)
		workerCtx, cancel := context.WithCancel(ctx)
		workerCancel = cancel
		var workerWG sync.WaitGroup
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			srv.runWorkerLoop(workerCtx)
		}()
		defer func() {
			workerCancel()
			done := make(chan struct{})
			go func() {
				workerWG.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(shutdownTimeout):
				slog.Warn("worker shutdown timed out")
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-fd.Err():
		return err
	case <-stop:
	case <-ctx.Done():
	}

	if workerCancel != nil {
		workerCancel()
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return fd.Shutdown(shutCtx)
}

// runGaugeRefreshLoop keeps the process gauges current on a replica that runs no
// worker cycle. It only pings and reads pool stats, so it costs one round trip per
// tick regardless of how much data the deployment holds.
func (srv *Server) runGaugeRefreshLoop(ctx context.Context) {
	interval := srv.cfg.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	srv.metrics.updateProcessGauges(srv.store)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			srv.metrics.updateProcessGauges(srv.store)
		}
	}
}

func (srv *Server) runWorkerLoop(ctx context.Context) {
	for {
		// Block on a LISTEN/NOTIFY wake-up from ingest, falling back to the
		// poll interval, so the first notification goes out right after the
		// ingest commit instead of up to PollInterval later.
		srv.store.WaitForWake(ctx, srv.cfg.PollInterval)
		if ctx.Err() != nil {
			return
		}
		if !srv.cycleRunning.CompareAndSwap(false, true) {
			slog.Warn("worker_cycle_skipped", "reason", "previous cycle still running")
			srv.metrics.incWorkerCycleSkipped()
			continue
		}
		t0 := time.Now()
		result, err := srv.eng.RunWorkerCycle(context.WithoutCancel(ctx))
		srv.cycleRunning.Store(false)
		srv.metrics.recordCycle(time.Since(t0))
		srv.metrics.updateOperationalGauges(srv.store)
		applyCycleResult(srv.metrics, result)
		srv.heartbeat.cycleCompleted(err)
	}
}

// handler builds the routed, fully wrapped HTTP handler this server serves.
//
// Outermost first: recover catches panics from everything including the tracing
// middleware; tracing wraps the rest so the span covers the whole request and
// the access log can read the trace id out of the context.
func (srv *Server) handler() http.Handler {
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	return srv.withRecover(srv.withTracing(srv.withRequestID(srv.withObservability(mux))))
}

func (srv *Server) registerRoutes(mux *http.ServeMux) {
	// /live is a cheap liveness probe: 200 as long as the process can serve HTTP.
	// /health and /ready are readiness probes: they include a DB ping (and Kafka
	// outbox depth when configured). Use /live for k8s livenessProbe so a transient
	// DB blip doesn't restart pods.
	mux.HandleFunc("/live", srv.handleLive)
	mux.HandleFunc("/health", srv.handleHealth)
	mux.HandleFunc("/ready", srv.handleHealth)
	mux.HandleFunc("/metrics", srv.handleMetrics)

	// Webhook ingestion (no API key required; auth via integration key in URL)
	mux.HandleFunc("POST /integrations/v1/webhook/{key}", srv.handleWebhook)
	mux.HandleFunc("POST /integrations/v1/alertmanager/{key}", srv.handleAlertmanager)
	mux.HandleFunc("POST /integrations/v1/pagerduty/{key}", srv.handlePagerDuty)
	mux.HandleFunc("POST /integrations/v1/victorops/{key}", srv.handleVictorOps)
	mux.HandleFunc("POST /integrations/v1/grafana-alerting/{key}", srv.handleGrafanaAlerting)
	mux.HandleFunc("POST /integrations/v1/opensearch/{key}", srv.handleOpenSearch)
	mux.HandleFunc("POST /integrations/v1/elasticsearch/{key}", srv.handleElasticsearch)
	mux.HandleFunc("POST /v2/alert/pool", srv.handleLegacyPool)

	// Inbound ChatOps slash commands, authenticated by the platform's own
	// signature rather than by an nxs-anomaly credential.
	mux.HandleFunc("POST /integrations/v1/chatops/slack", srv.handleSlackCommand)
	mux.HandleFunc("POST /integrations/v1/chatops/telegram", srv.handleTelegramCommand)
	mux.HandleFunc("POST /integrations/v1/chatops/slack/interactive", srv.handleSlackInteractive)
	mux.HandleFunc("POST /integrations/v1/chatops/mattermost", srv.handleMattermostAction)
	mux.HandleFunc("POST /integrations/v1/chatops/mattermost/command", srv.handleMattermostCommand)

	// Management API (all require API key when configured)
	mux.HandleFunc("/api/v1/", srv.handleAPI)
}
