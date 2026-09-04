package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedIntegration creates a webhook integration under key "k1" through the engine
// so it has the canonical shape (including a default route selectRoute requires).
func seedIntegration(srv *Server) {
	if _, err := srv.eng.CreateIntegration(context.Background(), map[string]any{
		"name": "Webhook",
		"key":  "k1",
	}); err != nil {
		panic(err)
	}
}

// ingest builds a request carrying the integration key as a path value (set by
// the router pattern in production) and invokes the handler.
func ingest(h http.HandlerFunc, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/x/"+key, strings.NewReader(body))
	if key != "" {
		r.SetPathValue("key", key)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestHandleWebhook(t *testing.T) {
	srv, st := newTestServer()
	seedIntegration(srv)

	// Success → 202 and an alert group is created.
	w := ingest(srv.handleWebhook, `{"title":"DB down","labels":{"alertname":"DBDown","severity":"critical"}}`, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Count("alert_groups") != 1 {
		t.Errorf("alert group not created: count=%d", st.Count("alert_groups"))
	}

	// Unknown key → 404.
	w = ingest(srv.handleWebhook, `{"title":"x"}`, "nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}

	// Malformed JSON → 400.
	w = ingest(srv.handleWebhook, `{bad`, "k1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: code=%d, want 400", w.Code)
	}
}

func TestHandleWebhookRateLimited(t *testing.T) {
	srv, _ := newTestServer()
	seedIntegration(srv)
	srv.webhookLimiter = newRateLimiter(0, 0) // zero capacity → always deny

	w := ingest(srv.handleWebhook, `{"title":"x"}`, "k1")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("rate limited: code=%d, want 429", w.Code)
	}
}

func TestHandleAlertmanager(t *testing.T) {
	srv, _ := newTestServer()
	seedIntegration(srv)

	// Valid envelope → 202.
	body := `{"alerts":[{"status":"firing","labels":{"alertname":"DBDown","severity":"critical"}}]}`
	w := ingest(srv.handleAlertmanager, body, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}

	// Empty alerts → 400 (validation, before key lookup).
	w = ingest(srv.handleAlertmanager, `{"alerts":[]}`, "k1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty alerts: code=%d, want 400", w.Code)
	}

	// Unknown key with valid envelope → 404.
	w = ingest(srv.handleAlertmanager, body, "nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}
}

func TestHandleGrafanaAlerting(t *testing.T) {
	srv, _ := newTestServer()
	seedIntegration(srv)

	w := ingest(srv.handleGrafanaAlerting, `{"alerts":[{"labels":{"alertname":"X"}}]}`, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}

	w = ingest(srv.handleGrafanaAlerting, `{"alerts":[]}`, "k1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty alerts: code=%d, want 400", w.Code)
	}
}

func TestHandleOpenSearch(t *testing.T) {
	srv, st := newTestServer()
	seedIntegration(srv)

	body := `{"status":"firing","monitor":{"id":"m1","name":"5xx rate"},` +
		`"trigger":{"id":"t1","name":"too many 5xx","severity":"1"}}`
	w := ingest(srv.handleOpenSearch, body, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Count("alert_groups") != 1 {
		t.Errorf("alert group not created: count=%d", st.Count("alert_groups"))
	}

	// A bucket-level envelope is one event: two buckets, two alerts, two groups.
	envelope := `{"monitor":{"id":"m2","name":"errors by host"},` +
		`"trigger":{"id":"t2","name":"per host","severity":"2"},` +
		`"alerts":[{"bucket_keys":"host-a"},{"bucket_keys":"host-b"}]}`
	w = ingest(srv.handleOpenSearch, envelope, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("envelope: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Count("alert_groups") != 3 {
		t.Errorf("bucket alerts did not open a group each: count=%d", st.Count("alert_groups"))
	}

	// Empty alerts[] → 400 (validation, before the key lookup).
	w = ingest(srv.handleOpenSearch, `{"alerts":[]}`, "k1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty alerts: code=%d, want 400", w.Code)
	}

	// The default channel message is plain text, not JSON: it must not be ingested
	// as an alert with no monitor, trigger or dedupe key.
	w = ingest(srv.handleOpenSearch, `Monitor 5xx rate just entered alert status.`, "k1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("plain text body: code=%d, want 400", w.Code)
	}

	w = ingest(srv.handleOpenSearch, body, "nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}
}

func TestHandleElasticsearch(t *testing.T) {
	srv, st := newTestServer()
	seedIntegration(srv)

	body := `{"status":"active","severity":"critical","rule":{"id":"r1","name":"disk full"},` +
		`"alert":{"id":"a1","actionGroup":"default"},"message":"disk 95%"}`
	w := ingest(srv.handleElasticsearch, body, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Count("alert_groups") != 1 {
		t.Errorf("alert group not created: count=%d", st.Count("alert_groups"))
	}

	// The recovery action group closes the group it opened rather than opening a
	// second one.
	recovery := `{"rule":{"id":"r1","name":"disk full"},` +
		`"alert":{"id":"a1","actionGroup":"recovered"}}`
	w = ingest(srv.handleElasticsearch, recovery, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("recovery: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Count("alert_groups") != 1 {
		t.Errorf("recovery opened a new group: count=%d", st.Count("alert_groups"))
	}

	w = ingest(srv.handleElasticsearch, body, "nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}
}

func TestHandlePagerDuty(t *testing.T) {
	srv, _ := newTestServer()
	seedIntegration(srv)

	w := ingest(srv.handlePagerDuty, `{"payload":{"summary":"DB down","severity":"critical"}}`, "k1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}

	w = ingest(srv.handlePagerDuty, `{"payload":{"summary":"x"}}`, "nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}
}

func TestHandleVictorOps(t *testing.T) {
	srv, _ := newTestServer()
	seedIntegration(srv)

	w := ingest(srv.handleVictorOps, `{"entity_id":"e1","state_message":"DB down"}`, "k1")
	if w.Code != http.StatusOK {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}

	w = ingest(srv.handleVictorOps, `{"entity_id":"e1"}`, "nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}
}

func TestHandleLegacyPool(t *testing.T) {
	srv, _ := newTestServer()
	seedIntegration(srv)

	body := `{"triggerMessage":"DB down","monitoringURL":"http://mon/x"}`

	// Missing X-Auth-Key → 400.
	r := httptest.NewRequest(http.MethodPost, "/v2/alert/", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleLegacyPool(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing auth key: code=%d, want 400", w.Code)
	}

	// Valid key → 200.
	r = httptest.NewRequest(http.MethodPost, "/v2/alert/", strings.NewReader(body))
	r.Header.Set("X-Auth-Key", "k1")
	w = httptest.NewRecorder()
	srv.handleLegacyPool(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("success: code=%d body=%s", w.Code, w.Body.String())
	}

	// Unknown key → 404.
	r = httptest.NewRequest(http.MethodPost, "/v2/alert/", strings.NewReader(body))
	r.Header.Set("X-Auth-Key", "nope")
	w = httptest.NewRecorder()
	srv.handleLegacyPool(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown key: code=%d, want 404", w.Code)
	}
}
