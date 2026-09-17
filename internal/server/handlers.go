package server

import (
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
)

// ---- health / metrics ----

// handleLive is a liveness probe: it never touches the database, so it stays 200
// as long as the process is able to serve HTTP. Use it for k8s livenessProbe.
func (srv *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        Version,
		"uptime_seconds": int64(time.Since(srv.startTime).Seconds()),
	})
}

func (srv *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	resp := map[string]any{
		"status":  "ok",
		"db_ok":   true,
		"version": Version,
		// The edition is here so that "which build is actually running?" can be
		// answered from outside the cluster, without reading the image tag of
		// whatever the deployment happens to point at now.
		"edition":        Edition,
		"uptime_seconds": int64(time.Since(srv.startTime).Seconds()),
	}
	const pingCacheTTL = 5 * time.Second
	const kafkaOutboxDegradedThreshold = 500
	now := time.Now().UnixNano()
	checkedAt := srv.pingCheckedAt.Load()
	if checkedAt == 0 || time.Duration(now-checkedAt) > pingCacheTTL {
		if err := srv.store.Ping(r.Context()); err != nil {
			srv.pingOK.Store(false)
			// Log the detail server-side; the unauthenticated /health response
			// only exposes db_ok=false so internal details can't leak.
			slog.Warn("health_db_ping_failed", "error", err)
		} else {
			srv.pingOK.Store(true)
		}
		// Check Kafka outbox depth if Kafka is configured.
		if os.Getenv("KAFKA_BROKERS") != "" {
			if n, err := srv.store.CountCollection(r.Context(), "kafka_outbox", map[string]any{}); err == nil {
				srv.kafkaOutboxDepth.Store(int64(n))
				srv.kafkaOK.Store(n <= kafkaOutboxDegradedThreshold)
			}
		}
		srv.pingCheckedAt.Store(now)
	}
	if !srv.pingOK.Load() {
		resp["status"] = "degraded"
		resp["db_ok"] = false
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}
	if os.Getenv("KAFKA_BROKERS") != "" && !srv.kafkaOK.Load() {
		depth := srv.kafkaOutboxDepth.Load()
		resp["status"] = "degraded"
		resp["kafka_ok"] = false
		resp["kafka_outbox_depth"] = depth
		writeJSON(w, http.StatusOK, resp)
		return
	}
	lastAt := srv.metrics.lastCycleAt.Load()
	if lastAt != nil {
		resp["last_worker_cycle_at"] = lastAt
	}
	resp["worker_cycles_completed"] = atomic.LoadInt64(&srv.metrics.cyclesCompleted)
	writeJSON(w, http.StatusOK, resp)
}

func (srv *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	srv.metrics.handler.ServeHTTP(w, r)
}

// ---- Management API ----

func (srv *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Sign-in, sign-out and the method list run before authentication: they are
	// how a caller acquires credentials in the first place.
	if isAuthPublicPath(r.URL.Path) {
		srv.handleAuthPublic(w, r)
		return
	}
	if isOIDCPath(r.URL.Path) {
		srv.handleOIDC(w, r)
		return
	}
	actor, ok := srv.authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required"})
		return
	}
	if err := checkSameOriginWrite(r); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	action := requiredAction(r.Method, r.URL.Path)
	if !actor.Can(action) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":          "role " + actor.Role.String() + " may not perform this operation",
			"required":       string(action),
			"effective_role": actor.Role.String(),
		})
		return
	}
	ip := srv.clientIP(r)
	if srv.apiLimiter != nil && !srv.apiLimiter.allow(ip) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	// Everything downstream — the engine's audit records and the attribution
	// written into alert group timelines — reads the actor from the context.
	// Which tool is acting, not who: it decides whether what this request
	// creates is marked as provisioned, and whether it may change objects that
	// already are.
	actor.Provisioner = detectProvisioner(r)
	ctx := authz.NewContext(r.Context(), actor)
	ctx = engine.NewRequestIPContext(ctx, ip)
	srv.routeAPI(w, r.WithContext(ctx))
}
