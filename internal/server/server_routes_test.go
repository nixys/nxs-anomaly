package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// newTestServer builds a Server wired to an in-memory store, enough to drive the
// routeAPI switch and the lightweight handlers without PostgreSQL or listeners.
func newTestServer() (*Server, *storetest.Store) {
	st := storetest.New()
	return &Server{
		eng:                 engine.New(st),
		store:               st,
		metrics:             newMetrics(),
		startTime:           time.Now(),
		loginLimiter:        newDBRateLimiter(st, "login:", loginRatePerSecond, loginBurst),
		loginAccountLimiter: newDBRateLimiter(st, "login-account:", loginAccountRatePerSecond, loginAccountBurst),
	}, st
}

// do issues a request straight at routeAPI and returns the recorder.
func (srv *Server) do(method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	srv.routeAPI(w, r)
	return w
}

func TestRouteAPIUserLifecycle(t *testing.T) {
	srv, _ := newTestServer()

	// Create.
	w := srv.do(http.MethodPost, "/api/v1/users", `{"name":"Alice"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "\"username\":\"alice\"") {
		t.Fatalf("created user body unexpected: %s", body)
	}
	id := extractID(t, body)

	// List.
	w = srv.do(http.MethodGet, "/api/v1/users", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("list users: code=%d body=%s", w.Code, w.Body.String())
	}

	// Get by id.
	w = srv.do(http.MethodGet, "/api/v1/users/"+id, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("get user: code=%d body=%s", w.Code, w.Body.String())
	}

	// Duty on.
	w = srv.do(http.MethodPost, "/api/v1/users/"+id+"/duty-on", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "\"on_duty\":true") {
		t.Fatalf("duty-on: code=%d body=%s", w.Code, w.Body.String())
	}

	// Update.
	w = srv.do(http.MethodPatch, "/api/v1/users/"+id, `{"email":"a@x.io"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "a@x.io") {
		t.Fatalf("update user: code=%d body=%s", w.Code, w.Body.String())
	}

	// Delete.
	w = srv.do(http.MethodDelete, "/api/v1/users/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete user: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRouteAPIValidationAndNotFound(t *testing.T) {
	srv, _ := newTestServer()

	// Missing required field → 400 via engine validation error.
	w := srv.do(http.MethodPost, "/api/v1/users", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create without name: code=%d, want 400", w.Code)
	}

	// Malformed JSON → 400 via readJSON.
	w = srv.do(http.MethodPost, "/api/v1/users", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: code=%d, want 400", w.Code)
	}

	// Unknown entity id → 404.
	w = srv.do(http.MethodGet, "/api/v1/users/usr-missing", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("get missing user: code=%d, want 404", w.Code)
	}

	// Unknown route → 404 (default case).
	w = srv.do(http.MethodGet, "/api/v1/nonexistent", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown route: code=%d, want 404", w.Code)
	}
}

func TestRouteAPITeamAndIntegration(t *testing.T) {
	srv, _ := newTestServer()

	w := srv.do(http.MethodPost, "/api/v1/teams", `{"name":"Ops"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create team: code=%d body=%s", w.Code, w.Body.String())
	}

	w = srv.do(http.MethodPost, "/api/v1/integrations", `{"name":"Prometheus"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create integration: code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "\"type\":\"webhook\"") {
		t.Errorf("integration default type missing: %s", w.Body.String())
	}
	intID := extractID(t, w.Body.String())

	// Rotate key.
	w = srv.do(http.MethodPost, "/api/v1/integrations/"+intID+"/rotate-key", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rotate key: code=%d body=%s", w.Code, w.Body.String())
	}

	// List integrations.
	w = srv.do(http.MethodGet, "/api/v1/integrations", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), intID) {
		t.Fatalf("list integrations: code=%d body=%s", w.Code, w.Body.String())
	}
}

// TestRouteAPIAlertGroupActions guards the path-segment id extraction for the
// alert-group action routes (acknowledge/resolve/timeline). These regressed on a
// 0-vs-1-indexed off-by-one in segment(path, …) that passed the action word as
// the group id, making every such endpoint 404.
func TestRouteAPIAlertGroupActions(t *testing.T) {
	srv, st := newTestServer()
	st.Seed("alert_groups", map[string]any{
		"id":     "grp-1",
		"status": "open",
		"logs":   []any{},
	})

	// Acknowledge by id embedded mid-path.
	w := srv.do(http.MethodPost, "/api/v1/alert-groups/grp-1/acknowledge", "")
	if w.Code != http.StatusOK {
		t.Fatalf("acknowledge: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Row("alert_groups", "grp-1")["status"] != "acknowledged" {
		t.Errorf("group not acknowledged: %v", st.Row("alert_groups", "grp-1")["status"])
	}

	// Timeline reads the same id slot.
	w = srv.do(http.MethodGet, "/api/v1/alert-groups/grp-1/timeline", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "grp-1") {
		t.Fatalf("timeline: code=%d body=%s", w.Code, w.Body.String())
	}

	// Resolve.
	w = srv.do(http.MethodPost, "/api/v1/alert-groups/grp-1/resolve", "")
	if w.Code != http.StatusOK {
		t.Fatalf("resolve: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Row("alert_groups", "grp-1")["status"] != "resolved" {
		t.Errorf("group not resolved: %v", st.Row("alert_groups", "grp-1")["status"])
	}
}

func TestHandleHealthOK(t *testing.T) {
	srv, _ := newTestServer()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health: code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "\"status\":\"ok\"") || !strings.Contains(body, "\"db_ok\":true") {
		t.Errorf("health body unexpected: %s", body)
	}
}

func TestHandleHealthRejectsNonGet(t *testing.T) {
	srv, _ := newTestServer()
	r := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("health POST: code=%d, want 404", w.Code)
	}
}

// extractID pulls the "id" string value out of a JSON object body.
func extractID(t *testing.T, body string) string {
	t.Helper()
	const key = "\"id\":\""
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatalf("no id in body: %s", body)
	}
	rest := body[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("malformed id in body: %s", body)
	}
	return rest[:j]
}
