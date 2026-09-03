package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doHdr is do() with request headers (mobile session, etc.).
func (srv *Server) doHdr(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.routeAPI(w, r)
	return w
}

func TestRouteAPIScheduleLifecycle(t *testing.T) {
	srv, _ := newTestServer()

	// Create.
	w := srv.do(http.MethodPost, "/api/v1/schedules", `{"name":"Primary"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create schedule: code=%d body=%s", w.Code, w.Body.String())
	}
	id := extractID(t, w.Body.String())

	// On-call (segment(path,3) extraction) — empty schedule still resolves 200.
	w = srv.do(http.MethodGet, "/api/v1/schedules/"+id+"/on-call", "")
	if w.Code != http.StatusOK {
		t.Fatalf("on-call: code=%d body=%s", w.Code, w.Body.String())
	}

	// Override needs an existing user.
	created, _ := srv.eng.CreateUser(context.Background(), map[string]any{"name": "Op"})
	uid := created["id"].(string)
	w = srv.do(http.MethodPost, "/api/v1/schedules/"+id+"/override",
		`{"user_id":"`+uid+`","until":"2026-12-31T00:00:00+00:00"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("override: code=%d body=%s", w.Code, w.Body.String())
	}

	// Update + delete.
	w = srv.do(http.MethodPut, "/api/v1/schedules/"+id, `{"name":"Renamed"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update schedule: code=%d body=%s", w.Code, w.Body.String())
	}
	w = srv.do(http.MethodDelete, "/api/v1/schedules/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete schedule: code=%d", w.Code)
	}
}

func TestRouteAPIEscalationChainLifecycle(t *testing.T) {
	srv, _ := newTestServer()
	w := srv.do(http.MethodPost, "/api/v1/escalation-chains", `{"name":"Default"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create chain: code=%d body=%s", w.Code, w.Body.String())
	}
	id := extractID(t, w.Body.String())

	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/api/v1/escalation-chains", "", http.StatusOK},
		{http.MethodGet, "/api/v1/escalation-chains/" + id, "", http.StatusOK},
		{http.MethodPut, "/api/v1/escalation-chains/" + id, `{"name":"X"}`, http.StatusOK},
		{http.MethodDelete, "/api/v1/escalation-chains/" + id, "", http.StatusOK},
	} {
		w := srv.do(tc.method, tc.path, tc.body)
		if w.Code != tc.want {
			t.Errorf("%s %s: code=%d, want %d (%s)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestRouteAPIChatops(t *testing.T) {
	srv, _ := newTestServer()

	// Create channel.
	w := srv.do(http.MethodPost, "/api/v1/chatops/channels", `{"platform":"telegram","name":"alerts"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create channel: code=%d body=%s", w.Code, w.Body.String())
	}
	id := extractID(t, w.Body.String())

	// Get / update / delete.
	if w := srv.do(http.MethodGet, "/api/v1/chatops/channels/"+id, ""); w.Code != http.StatusOK {
		t.Errorf("get channel: code=%d", w.Code)
	}
	if w := srv.do(http.MethodPut, "/api/v1/chatops/channels/"+id, `{"name":"renamed"}`); w.Code != http.StatusOK {
		t.Errorf("update channel: code=%d", w.Code)
	}

	// Messages list (empty → 200).
	if w := srv.do(http.MethodGet, "/api/v1/chatops/messages", ""); w.Code != http.StatusOK {
		t.Errorf("messages list: code=%d", w.Code)
	}

	// Command requires channel_id + command → missing both is a 400 (route is wired).
	if w := srv.do(http.MethodPost, "/api/v1/chatops/commands", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("command validation: code=%d, want 400", w.Code)
	}

	if w := srv.do(http.MethodDelete, "/api/v1/chatops/channels/"+id, ""); w.Code != http.StatusOK {
		t.Errorf("delete channel: code=%d", w.Code)
	}
}

func TestRouteAPIMobileFlow(t *testing.T) {
	srv, st := newTestServer()
	ctx := context.Background()

	// Build a user → device → session chain through the engine.
	user, _ := srv.eng.CreateUser(ctx, map[string]any{"name": "Mob"})
	uid := user["id"].(string)
	dev, err := srv.eng.RegisterMobileDevice(ctx, map[string]any{"user_id": uid, "platform": "ios", "push_token": "tok"})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	sess, err := srv.eng.CreateMobileSession(ctx, map[string]any{"user_id": uid, "device_id": dev["id"]})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := sess["token"].(string)
	st.Seed("alert_groups", map[string]any{"id": "mg1", "status": "open", "logs": []any{}})

	// Dashboard requires the session header.
	if w := srv.do(http.MethodGet, "/api/v1/mobile/dashboard", ""); w.Code != http.StatusBadRequest {
		t.Errorf("dashboard without header: code=%d, want 400", w.Code)
	}
	if w := srv.doHdr(http.MethodGet, "/api/v1/mobile/dashboard", "", map[string]string{"X-Mobile-Session": token}); w.Code != http.StatusOK {
		t.Errorf("dashboard: code=%d", w.Code)
	}

	// Acknowledge via mobile (segment(path,4) id extraction + session header).
	w := srv.doHdr(http.MethodPost, "/api/v1/mobile/alert-groups/mg1/acknowledge", "", map[string]string{"X-Mobile-Session": token})
	if w.Code != http.StatusOK {
		t.Fatalf("mobile ack: code=%d body=%s", w.Code, w.Body.String())
	}
	if st.Row("alert_groups", "mg1")["status"] != "acknowledged" {
		t.Errorf("group not acknowledged via mobile")
	}

	// Missing session header → 400.
	if w := srv.do(http.MethodPost, "/api/v1/mobile/alert-groups/mg1/resolve", ""); w.Code != http.StatusBadRequest {
		t.Errorf("mobile resolve without header: code=%d, want 400", w.Code)
	}
}

func TestRouteAPIBulkActions(t *testing.T) {
	srv, st := newTestServer()
	st.Seed("alert_groups",
		map[string]any{"id": "b1", "status": "open", "logs": []any{}},
		map[string]any{"id": "b2", "status": "open", "logs": []any{}},
	)

	// Bulk acknowledge.
	w := srv.do(http.MethodPost, "/api/v1/alert-groups/bulk-acknowledge", `{"group_ids":["b1","b2"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk ack: code=%d body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{"b1", "b2"} {
		if st.Row("alert_groups", id)["status"] != "acknowledged" {
			t.Errorf("group %s not acknowledged", id)
		}
	}

	// Bulk silence.
	w = srv.do(http.MethodPost, "/api/v1/alert-groups/bulk-silence?duration_minutes=30", `{"group_ids":["b1"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk silence: code=%d body=%s", w.Code, w.Body.String())
	}
}
