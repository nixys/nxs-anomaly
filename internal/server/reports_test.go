package server

import (
	"net/http"
	"strings"
	"testing"
)

// The reports routes are plain read-through to the generic collection
// helpers (see server_api.go): this pins that GET /api/v1/reports and GET
// /api/v1/reports/{id} are actually wired up, since the OpenAPI contract
// tests only check that a route dispatches, not that it returns what was
// seeded.
func TestRouteAPIReportsListAndGet(t *testing.T) {
	srv, st := newTestServer()
	st.Seed("reports", map[string]any{
		"id":           "rpt_team-a_20260901",
		"team_id":      "team-a",
		"generated_at": "2026-09-01T00:00:00Z",
		"summary_text": "On-call quality report — team-a",
	})

	w := srv.do(http.MethodGet, "/api/v1/reports", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "rpt_team-a_20260901") {
		t.Fatalf("list reports: code=%d body=%s", w.Code, w.Body.String())
	}

	w = srv.do(http.MethodGet, "/api/v1/reports/rpt_team-a_20260901", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "On-call quality report") {
		t.Fatalf("get report: code=%d body=%s", w.Code, w.Body.String())
	}

	w = srv.do(http.MethodGet, "/api/v1/reports/does-not-exist", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("get missing report: code=%d body=%s", w.Code, w.Body.String())
	}
}
