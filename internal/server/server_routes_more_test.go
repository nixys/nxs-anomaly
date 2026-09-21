package server

import (
	"context"
	"net/http"
	"testing"
)

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

	// A typo in a command is the caller's mistake, not a server fault: it used to
	// answer 500 "internal error" and log an ERROR for every mistyped command.
	if w := srv.do(http.MethodPost, "/api/v1/chatops/commands", `{"channel_id":"`+id+`","command":"frobnicate"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown command: code=%d body=%s, want 400", w.Code, w.Body.String())
	}

	if w := srv.do(http.MethodDelete, "/api/v1/chatops/channels/"+id, ""); w.Code != http.StatusOK {
		t.Errorf("delete channel: code=%d", w.Code)
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
