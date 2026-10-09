package server

import (
	"context"
	"net/http"
	"time"

	"github.com/nixys/nxs-anomaly/internal/engine"
)

// All webhook ingestion handlers share the same skeleton: per-source latency
// histogram via defer, per-key rate limit, JSON body, engine ingest call,
// metrics on outcome, JSON response. Only the engine method and the source
// label differ per handler.

// ingestContext tells the engine when the answer to this ingest is due: the
// handler's start plus the HTTP write timeout, past which the server can no
// longer answer. An ingest waiting for its integration's turn gives up in time
// to answer 503 + Retry-After instead of having its answer cut off. Without a
// write timeout nothing is due, and the engine's own bound applies.
func (srv *Server) ingestContext(r *http.Request, start time.Time) context.Context {
	if srv.cfg.WriteTimeout <= 0 {
		return r.Context()
	}
	return engine.WithAnswerBy(r.Context(), start.Add(srv.cfg.WriteTimeout))
}

func (srv *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("webhook").Observe(time.Since(t0).Seconds()) }()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, raw, ok := readJSONWithRaw(w, r)
	if !ok {
		return
	}
	if err := srv.verifyWebhookSig(r, key, raw); err != nil {
		switch err {
		case errWebhookSigInvalid:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		case errWebhookSecretUnresolved:
			// Our configuration, not the sender's request: 503 so the alert is
			// retried once the secret is back rather than dropped as malformed.
			srv.metrics.incIngestError("webhook")
			w.Header().Set("Retry-After", "30")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		default:
			// The integration lookup failed. Through writeIngestError so it is
			// logged and a database restart answers 503 + Retry-After, which the
			// sender retries, rather than a silent 500, which it drops.
			srv.metrics.incIngestError("webhook")
			writeIngestError(w, err, "webhook", key)
		}
		return
	}
	result, err := srv.eng.IngestAlert(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("webhook")
		writeIngestError(w, err, "webhook", key)
		return
	}
	srv.metrics.incAlerts(1)
	writeJSON(w, http.StatusAccepted, result)
}

func (srv *Server) handleAlertmanager(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("alertmanager").Observe(time.Since(t0).Seconds()) }()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestAlertmanager(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("alertmanager")
		writeIngestError(w, err, "alertmanager", key)
		return
	}
	if n, ok := result["processed"].(int); ok && n > 0 {
		srv.metrics.incAlerts(n)
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (srv *Server) handlePagerDuty(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("pagerduty").Observe(time.Since(t0).Seconds()) }()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestPagerDuty(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("pagerduty")
		writeIngestError(w, err, "pagerduty", key)
		return
	}
	srv.metrics.incAlerts(1)
	writeJSON(w, http.StatusAccepted, result)
}

func (srv *Server) handleVictorOps(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("victorops").Observe(time.Since(t0).Seconds()) }()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestVictorOps(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("victorops")
		writeIngestError(w, err, "victorops", key)
		return
	}
	srv.metrics.incAlerts(1)
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleGrafanaAlerting(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() {
		srv.metrics.ingestDuration.WithLabelValues("grafana-alerting").Observe(time.Since(t0).Seconds())
	}()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestGrafanaAlerting(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("grafana-alerting")
		writeIngestError(w, err, "grafana-alerting", key)
		return
	}
	if n, ok := result["processed"].(int); ok && n > 0 {
		srv.metrics.incAlerts(n)
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (srv *Server) handleOpenSearch(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("opensearch").Observe(time.Since(t0).Seconds()) }()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestOpenSearch(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("opensearch")
		writeIngestError(w, err, "opensearch", key)
		return
	}
	if n, ok := result["processed"].(int); ok && n > 0 {
		srv.metrics.incAlerts(n)
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (srv *Server) handleElasticsearch(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() {
		srv.metrics.ingestDuration.WithLabelValues("elasticsearch").Observe(time.Since(t0).Seconds())
	}()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestElasticsearch(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("elasticsearch")
		writeIngestError(w, err, "elasticsearch", key)
		return
	}
	srv.metrics.incAlerts(1)
	writeJSON(w, http.StatusAccepted, result)
}

func (srv *Server) handleLegacyPool(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("legacy-pool").Observe(time.Since(t0).Seconds()) }()
	key := r.Header.Get("X-Auth-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "X-Auth-Key header is required"})
		return
	}
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeRateLimited(w, 1, "rate limit exceeded")
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestLegacyPool(srv.ingestContext(r, t0), key, body)
	if err != nil {
		srv.metrics.incIngestError("legacy-pool")
		writeIngestError(w, err, "legacy-pool", key)
		return
	}
	srv.metrics.incAlerts(1)
	writeJSON(w, http.StatusOK, result)
}
