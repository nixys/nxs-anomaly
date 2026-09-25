package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// The event stream is how a phone learns about an alert in seconds instead of
// the fifteen minutes Android allows a periodic job.

func TestMobileStreamPayloadReportsWhatChanged(t *testing.T) {
	first := map[string]mobileGroupBrief{
		"grp-1": {ID: "grp-1", Title: "Disk full", Severity: "critical", Status: "open"},
	}
	baseline := mobileStreamPayload(nil, first)
	if baseline["baseline"] != true {
		t.Error("the first tick must be a baseline")
	}
	if got := baseline["added"].([]string); len(got) != 0 {
		// Otherwise every reconnect — after a dead network, after the process
		// was killed — would ring for groups the phone already knows.
		t.Errorf("baseline announced %v as new", got)
	}

	second := map[string]mobileGroupBrief{
		"grp-1": {ID: "grp-1", Title: "Disk full", Severity: "critical", Status: "acknowledged"},
		"grp-2": {ID: "grp-2", Title: "Latency", Severity: "warning", Status: "open"},
	}
	tick := mobileStreamPayload(first, second)
	if tick["baseline"] != false {
		t.Error("a later tick is not a baseline")
	}
	if got := tick["added"].([]string); len(got) != 1 || got[0] != "grp-2" {
		t.Errorf("added = %v, want [grp-2]", got)
	}
	if got := tick["changed"].([]string); len(got) != 1 || got[0] != "grp-1" {
		t.Errorf("changed = %v, want [grp-1] — its status moved", got)
	}
	if got := len(tick["groups"].([]mobileGroupBrief)); got != 2 {
		t.Errorf("groups = %d, want the whole current set", got)
	}

	// A resolved group leaves the set, and that is not an "added" or a
	// "changed" anywhere: the phone sees it gone from groups.
	third := map[string]mobileGroupBrief{"grp-2": second["grp-2"]}
	tick = mobileStreamPayload(second, third)
	if len(tick["added"].([]string))+len(tick["changed"].([]string)) != 0 {
		t.Errorf("a group leaving produced %v / %v", tick["added"], tick["changed"])
	}
	if got := tick["groups"].([]mobileGroupBrief); len(got) != 1 || got[0].ID != "grp-2" {
		t.Errorf("groups = %v", got)
	}
}

// streamEvents parses an SSE body into (event, data) pairs.
func streamEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		var name, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name == "" || data == "" {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("event %q carries invalid JSON: %v", name, err)
		}
		payload["_event"] = name
		out = append(out, payload)
	}
	return out
}

func TestMobileStreamAnnouncesANewGroup(t *testing.T) {
	t.Setenv("NXS_ANOMALY_MOBILE_STREAM_INTERVAL_SECONDS", "1")
	srv, st := newSessionServer(t)
	// A real session: the stream re-checks it on every tick.
	token := pairPhone(t, srv, "ada")
	st.Seed("alert_groups", map[string]any{
		"id": "grp-known", "status": "open", "title": "Known", "severity": "warning",
		"integration_id": "int-1", "escalation_chain_id": "chain-1",
	})
	// What makes a group concern this person: a notification addressed to them.
	st.Seed("notifications", map[string]any{
		"id": "ntf-1", "user_id": "usr-admin", "alert_group_id": "grp-known", "channel": "log",
	})

	ctx, cancel := context.WithCancel(authz.NewContext(context.Background(),
		authz.Actor{ID: "usr-admin", Kind: authz.KindUser, Role: authz.RoleResponder}))
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/events", nil).WithContext(ctx)
	withBearer(token)(r)
	w := httptest.NewRecorder()
	// Wrapped exactly as the access-log middleware wraps it in production. A
	// bare recorder is an http.Flusher and hid the bug this guards: through the
	// wrapper the handler could not flush, and every phone got a 500.
	wrapped := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	done := make(chan struct{})
	go func() {
		srv.handleMobileEvents(wrapped, r)
		close(done)
	}()

	// The group the phone is not supposed to hear about: it concerns nobody.
	time.Sleep(300 * time.Millisecond)
	st.Seed("alert_groups", map[string]any{
		"id": "grp-other", "status": "open", "title": "Someone else's", "severity": "critical",
		"integration_id": "int-1", "escalation_chain_id": "chain-1",
	})
	// And the one that does.
	st.Seed("alert_groups", map[string]any{
		"id": "grp-new", "status": "open", "title": "Woken by this", "severity": "critical",
		"integration_id": "int-1", "escalation_chain_id": "chain-1",
	})
	st.Seed("notifications", map[string]any{
		"id": "ntf-2", "user_id": "usr-admin", "alert_group_id": "grp-new", "channel": "log",
	})

	time.Sleep(1500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream did not stop when the request was cancelled")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if b := w.Header().Get("X-Accel-Buffering"); b != "no" {
		t.Errorf("X-Accel-Buffering = %q: nginx would hold the events back", b)
	}
	events := streamEvents(t, w.Body.String())
	if len(events) < 2 || events[0]["_event"] != "hello" {
		t.Fatalf("stream did not open with hello: %v", events)
	}
	var sawBaseline, sawAdded bool
	for _, e := range events[1:] {
		if e["_event"] != "groups" {
			continue
		}
		if e["baseline"] == true {
			sawBaseline = true
			continue
		}
		for _, id := range e["added"].([]any) {
			if id == "grp-new" {
				sawAdded = true
			}
			if id == "grp-other" {
				t.Error("announced a group that concerns somebody else")
			}
		}
	}
	if !sawBaseline {
		t.Error("no baseline tick")
	}
	if !sawAdded {
		t.Errorf("the new group was never announced: %v", w.Body.String())
	}
}

func TestMobileStreamRefusesAnAPIKey(t *testing.T) {
	srv, _ := newTestServer()
	ctx := authz.NewContext(context.Background(),
		authz.Actor{ID: "key-1", Kind: authz.KindService, Role: authz.RoleAdmin})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	srv.handleMobileEvents(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — there is no 'concerns me' for an API key", w.Code)
	}
	if n := MobileStreamCount(); n != 0 {
		t.Errorf("open streams = %d after a refusal", n)
	}
}

// TestMobileStreamEndsWhenThePhoneIsSignedOut: the session was checked when the
// stream opened; revoking it from the web must stop the groups arriving, not
// wait for the connection to drop on its own.
func TestMobileStreamEndsWhenThePhoneIsSignedOut(t *testing.T) {
	t.Setenv("NXS_ANOMALY_MOBILE_STREAM_INTERVAL_SECONDS", "1")
	srv, _ := newSessionServer(t)
	token := pairPhone(t, srv, "ada")

	ctx, cancel := context.WithCancel(authz.NewContext(context.Background(),
		authz.Actor{ID: "usr-admin", Kind: authz.KindUser, Role: authz.RoleResponder}))
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/events", nil).WithContext(ctx)
	withBearer(token)(r)
	w := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}

	done := make(chan struct{})
	go func() {
		srv.handleMobileEvents(w, r)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	if err := srv.eng.RevokeMobileSession(context.Background(), token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream kept running for a phone that was signed out")
	}
}
