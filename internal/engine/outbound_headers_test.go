package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Custom headers on outbound webhooks (issue #27): a gateway that takes its
// credential in Authorization or X-API-Key could only be reached with the key
// in the URL, which every proxy and access log along the way keeps.

func TestSanitizeOutboundHeaders(t *testing.T) {
	got, err := sanitizeOutboundHeaders(map[string]any{
		"authorization": "env:NXS_TEST_GATEWAY_AUTH",
		"X-Api-Key":     "k-123",
	})
	if err != nil {
		t.Fatalf("valid headers refused: %v", err)
	}
	if got["Authorization"] != "env:NXS_TEST_GATEWAY_AUTH" || got["X-Api-Key"] != "k-123" {
		t.Errorf("headers = %#v, want canonical names and values kept", got)
	}
	if got, err := sanitizeOutboundHeaders(nil); got != nil || err != nil {
		t.Errorf("nil = (%#v, %v), want (nil, nil)", got, err)
	}

	for name, raw := range map[string]any{
		"not an object":  []any{"Authorization: x"},
		"non-string":     map[string]any{"X-Api-Key": 5},
		"invalid name":   map[string]any{"X Api Key": "v"},
		"CRLF injection": map[string]any{"X-Api-Key": "v\r\nX-Evil: 1"},
		"empty value":    map[string]any{"X-Api-Key": ""},
		"reserved":       map[string]any{"content-type": "text/plain"},
		"host":           map[string]any{"Host": "internal"},
		"two spellings":  map[string]any{"x-api-key": "a", "X-API-KEY": "b"},
		"too many":       manyHeaders(maxOutboundHeaders + 1),
	} {
		if _, err := sanitizeOutboundHeaders(raw); err == nil {
			t.Errorf("%s: accepted, want a validation error", name)
		}
	}
}

func stringify(v any) string { return utils.JSONDumps(v) }

func manyHeaders(n int) map[string]any {
	m := map[string]any{}
	for i := 0; i < n; i++ {
		m["X-H"+strings.Repeat("a", i+1)] = "v"
	}
	return m
}

func TestProductionProfileRefusesInlineHeaderValue(t *testing.T) {
	t.Setenv("NXS_ANOMALY_PROFILE", "production")
	if _, err := sanitizeOutboundHeaders(map[string]any{"Authorization": "Bearer abc"}); err == nil {
		t.Error("inline header value accepted under the production profile")
	}
	if _, err := sanitizeOutboundHeaders(map[string]any{"Authorization": "env:NXS_TEST_GATEWAY_AUTH"}); err != nil {
		t.Errorf("env: reference refused: %v", err)
	}
}

// headerCapture is a receiver that records what one request carried.
func headerCapture(t *testing.T) (*httptest.Server, *http.Header, *string) {
	t.Helper()
	var got http.Header
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &body
}

func TestChatopsDeliverySendsChannelHeaders(t *testing.T) {
	t.Setenv("NXS_TEST_GATEWAY_AUTH", "Bearer gw-secret")
	srv, got, _ := headerCapture(t)

	ms := newMemStore()
	e := crudEngine(ms)
	ch, err := e.CreateChatopsChannel(context.Background(), map[string]any{
		"platform": "mattermost", "name": "#sre", "webhook_url": srv.URL,
		"headers": map[string]any{"Authorization": "env:NXS_TEST_GATEWAY_AUTH", "X-Source": "nxs"},
	})
	if err != nil {
		t.Fatalf("CreateChatopsChannel: %v", err)
	}
	e = honestyEngine(ms, DeliveryConfig{})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("chatops", ch["id"].(string), map[string]any{"title": "Boom"}))
	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s), want delivered", res.Status, res.Err)
	}
	if a := got.Get("Authorization"); a != "Bearer gw-secret" {
		t.Errorf("Authorization = %q, want the env value", a)
	}
	if s := got.Get("X-Source"); s != "nxs" {
		t.Errorf("X-Source = %q", s)
	}
	if ct := got.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, the transport's own header was lost", ct)
	}
}

func TestUpdateChatopsChannelHeadersReplaceAndClear(t *testing.T) {
	e := crudEngine(newMemStore())
	ch, err := e.CreateChatopsChannel(context.Background(), map[string]any{
		"platform": "slack", "name": "#sre", "headers": map[string]any{"X-Api-Key": "a"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := ch["id"].(string)
	if _, err := e.UpdateChatopsChannel(context.Background(), id, map[string]any{
		"headers": map[string]any{"X-Api-Key": "bad\nvalue"},
	}); err == nil {
		t.Error("update accepted an invalid header value")
	}
	updated, err := e.UpdateChatopsChannel(context.Background(), id, map[string]any{"name": "#ops"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !hasOutboundHeaders(updated["headers"]) {
		t.Error("an update that did not mention headers dropped them")
	}
	cleared, err := e.UpdateChatopsChannel(context.Background(), id, map[string]any{"headers": nil})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if hasOutboundHeaders(cleared["headers"]) {
		t.Errorf("headers = %#v after clearing", cleared["headers"])
	}
}

// An env: reference to an unset variable must not go out as a request without
// the credential — the receiver answers 401 and the timeline says nothing
// useful. It is a configuration gap, reported as one.
func TestUnsetHeaderReferenceSkipsWithoutSending(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()

	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "name": "#sre", "platform": "slack", "webhook_url": srv.URL,
		"headers": map[string]any{"Authorization": "env:NXS_TEST_UNSET_HEADER_VAR"},
	})
	e := honestyEngine(ms, DeliveryConfig{})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("chatops", "chat_1", map[string]any{"title": "Boom"}))
	if res.Status != deliverySkipped || res.ProviderStatus != skipNotConfigured {
		t.Fatalf("outcome = %#v, want skipped/not_configured", res)
	}
	if !strings.Contains(res.Err, "NXS_TEST_UNSET_HEADER_VAR") {
		t.Errorf("detail does not name the variable: %q", res.Err)
	}
	if calls != 0 {
		t.Errorf("receiver was called %d times", calls)
	}
}

func TestTriggerWebhookStepSendsHeaders(t *testing.T) {
	t.Setenv("NXS_TEST_GATEWAY_KEY", "k-live-123")
	srv, got, body := headerCapture(t)

	e := crudEngine(newMemStore())
	chain, err := e.CreateEscalationChain(context.Background(), map[string]any{
		"name": "gw",
		"steps": []any{map[string]any{
			"kind": StepTriggerWebhook, "webhook_url": srv.URL,
			"headers": map[string]any{"x-api-key": "env:NXS_TEST_GATEWAY_KEY"},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	// Walk the stored step through the escalation executor, as the worker does.
	s := newState(chain["steps"].([]any))
	g := model.WrapAlertGroup(newGroup(0, 0))
	e.advanceGroupLocked(s, g, "2026-09-22T10:00:00+00:00")
	if len(s.Notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(s.Notifications))
	}
	var ntf map[string]any
	for _, rec := range s.Notifications {
		ntf = notificationMap(rec)
	}
	if logs := stringify(g.Raw()["logs"]); !strings.Contains(logs, "X-Api-Key") || strings.Contains(logs, "NXS_TEST_GATEWAY_KEY") {
		t.Errorf("group log should name the header and nothing more: %s", logs)
	}

	res := honestyEngine(newMemStore(), DeliveryConfig{}).deliverNotificationViaAdapter(context.Background(), ntf)
	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s), want delivered", res.Status, res.Err)
	}
	if k := got.Get("X-Api-Key"); k != "k-live-123" {
		t.Errorf("X-Api-Key = %q, want the env value", k)
	}
	if strings.Contains(*body, "k-live-123") || strings.Contains(*body, "NXS_TEST_GATEWAY_KEY") {
		t.Errorf("the header leaked into the request body: %s", *body)
	}
}

func TestHeaderValuesAreMaskedOnReads(t *testing.T) {
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "name": "#sre", "platform": "slack",
		"headers": map[string]any{"Authorization": "Bearer inline-secret"},
	})
	ms.seed("escalation_chains", map[string]any{
		"id": "esc_1", "name": "gw", "steps": []any{
			map[string]any{"kind": StepTriggerWebhook, "webhook_url": "https://gw.test",
				"headers": map[string]any{"Authorization": "Bearer inline-secret"}},
		},
	})
	ms.seed("notifications", map[string]any{
		"id": "ntf_1", "channel": "webhook", "target": "https://gw.test",
		"payload": map[string]any{"title": "Boom",
			"headers": map[string]any{"Authorization": "Bearer inline-secret"}},
	})
	e := crudEngine(ms)

	for _, role := range []authz.Role{authz.RoleViewer, authz.RoleResponder, authz.RoleEditor, authz.RoleAdmin} {
		ctx := actorCtx(role)
		editor := authz.FromContext(ctx).Can(authz.ActionEdit)
		for _, c := range []struct{ collection, id string }{
			{"chatops_channels", "chat_1"}, {"escalation_chains", "esc_1"}, {"notifications", "ntf_1"},
		} {
			item, err := e.GetItem(ctx, c.collection, c.id)
			if err != nil {
				t.Fatalf("%s %s: %v", role, c.collection, err)
			}
			leaked := strings.Contains(stringify(item), "inline-secret")
			// Editors round-trip configuration they read; nobody edits a notification.
			wantVisible := editor && c.collection != "notifications"
			if leaked != wantVisible {
				t.Errorf("%s reading %s: value visible = %v, want %v", role, c.collection, leaked, wantVisible)
			}
			if !strings.Contains(stringify(item), "Authorization") {
				t.Errorf("%s reading %s: header name was dropped", role, c.collection)
			}
		}
	}
	// The stored row is untouched: delivery reads it through the store.
	raw, _ := ms.GetItem(context.Background(), "notifications", "ntf_1")
	if !strings.Contains(stringify(raw), "inline-secret") {
		t.Error("redaction modified the stored notification")
	}
}
