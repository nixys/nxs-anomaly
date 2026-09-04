package server

import (
	"net/http"
	"time"
)

// All webhook ingestion handlers share the same skeleton: per-source latency
// histogram via defer, per-key rate limit, JSON body, engine ingest call,
// metrics on outcome, JSON response. Only the engine method and the source
// label differ per handler.

func (srv *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	defer func() { srv.metrics.ingestDuration.WithLabelValues("webhook").Observe(time.Since(t0).Seconds()) }()
	key := r.PathValue("key")
	if srv.webhookLimiter != nil && !srv.webhookLimiter.allow(key) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	if err := srv.verifyWebhookSig(r, key, body); err != nil {
		if err == errWebhookSigInvalid {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
		}
		return
	}
	result, err := srv.eng.IngestAlert(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestAlertmanager(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestPagerDuty(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestVictorOps(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestGrafanaAlerting(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestOpenSearch(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestElasticsearch(r.Context(), key, body)
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
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	result, err := srv.eng.IngestLegacyPool(r.Context(), key, body)
	if err != nil {
		srv.metrics.incIngestError("legacy-pool")
		writeIngestError(w, err, "legacy-pool", key)
		return
	}
	srv.metrics.incAlerts(1)
	writeJSON(w, http.StatusOK, result)
}
