package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// These tests pin the delivery contract: a channel is reported delivered only
// when a provider accepted it. Channels with no transport on this deployment
// report skipped — never delivered, and never failed either, because nothing
// broke and no retry can help.

// decodeJSONBody reads a request body posted by an adapter under test.
func decodeJSONBody(r *http.Request) map[string]any {
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	return m
}

func honestyEngine(ms *memStore, cfg DeliveryConfig) *Engine {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Engine{store: ms, deliveryCfg: cfg, metrics: noopMetrics{}}
}

func notificationFor(channel, target string, payload map[string]any) map[string]any {
	n := model.NewNotification("grp_1", "int_1", "u_a", channel, target, "test", utils.ToISO(utils.UTCNow()), "")
	n.ScheduleDelivery(payload)
	return n.Raw()
}

func TestMobileWithoutPushRelayIsSkippedNotDelivered(t *testing.T) {
	ms := newMemStore()
	ms.seed("mobile_devices", map[string]any{
		"id": "dev_1", "user_id": "u_a", "platform": "ios", "push_token": "tok", "active": true,
	})
	e := honestyEngine(ms, DeliveryConfig{})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("mobile", "dev_1", map[string]any{"title": "Boom"}))

	if res.Status != deliverySkipped {
		t.Fatalf("status = %q, want skipped", res.Status)
	}
	if res.ProviderStatus != skipNotConfigured {
		t.Errorf("reason = %q, want %q", res.ProviderStatus, skipNotConfigured)
	}
	if !strings.Contains(res.Err, "NXS_ANOMALY_MOBILE_PUSH_URL") {
		t.Errorf("detail does not say what to configure: %q", res.Err)
	}
}

func TestMobileWithPushRelayDeliversAndReportsProviderCode(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decodeJSONBody(r)
		if r.Header.Get("Authorization") != "Bearer relay-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":true}`))
	}))
	defer srv.Close()

	ms := newMemStore()
	ms.seed("mobile_devices", map[string]any{
		"id": "dev_1", "user_id": "u_a", "platform": "android", "push_token": "tok-123", "active": true,
	})
	e := honestyEngine(ms, DeliveryConfig{
		MobilePushURL: srv.URL, MobilePushToken: "relay-secret",
	})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("mobile", "dev_1", map[string]any{"title": "Boom", "severity": "critical"}))

	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s), want delivered", res.Status, res.Err)
	}
	if res.Code != http.StatusAccepted {
		t.Errorf("provider code = %d, want 202", res.Code)
	}
	if res.ProviderStatus != "mobile_push" {
		t.Errorf("provider status = %q", res.ProviderStatus)
	}
	if !strings.Contains(res.Response, "queued") {
		t.Errorf("provider response not captured: %q", res.Response)
	}
	if got["push_token"] != "tok-123" || got["platform"] != "android" {
		t.Errorf("relay payload = %#v", got)
	}
}

func TestMobileRelayFailureIsRetryableFailureNotSkip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()

	ms := newMemStore()
	ms.seed("mobile_devices", map[string]any{"id": "dev_1", "push_token": "tok", "active": true})
	e := honestyEngine(ms, DeliveryConfig{MobilePushURL: srv.URL})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("mobile", "dev_1", map[string]any{"title": "Boom"}))

	if res.Status != deliveryFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Code != http.StatusBadGateway {
		t.Errorf("provider code = %d, want 502", res.Code)
	}
}

func TestMobileWithoutPushTokenIsSkipped(t *testing.T) {
	ms := newMemStore()
	ms.seed("mobile_devices", map[string]any{"id": "dev_1", "push_token": "", "active": true})
	e := honestyEngine(ms, DeliveryConfig{MobilePushURL: "https://relay.example/push"})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("mobile", "dev_1", nil))
	if res.Status != deliverySkipped || res.ProviderStatus != skipNotConfigured {
		t.Fatalf("outcome = %#v, want skipped/not_configured", res)
	}
}

func TestChatopsWithoutWebhookIsSkipped(t *testing.T) {
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "name": "#sre", "platform": "slack", "webhook_url": "",
	})
	e := honestyEngine(ms, DeliveryConfig{})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("chatops", "chat_1", map[string]any{"title": "Boom"}))

	if res.Status != deliverySkipped {
		t.Fatalf("status = %q, want skipped", res.Status)
	}
	if !strings.Contains(res.Err, "#sre") {
		t.Errorf("detail does not name the channel: %q", res.Err)
	}
}

func TestChatopsWithWebhookDelivers(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeJSONBody(r)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "name": "#sre", "platform": "mattermost", "webhook_url": srv.URL,
	})
	e := honestyEngine(ms, DeliveryConfig{})

	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("chatops", "chat_1", map[string]any{"title": "Boom", "severity": "high"}))

	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s), want delivered", res.Status, res.Err)
	}
	if res.ProviderStatus != "mattermost_chatops" {
		t.Errorf("provider status = %q", res.ProviderStatus)
	}
	if text, _ := body["text"].(string); !strings.Contains(text, "Boom") {
		t.Errorf("posted text = %q", text)
	}
}

// A missing provider credential is a configuration gap, not a transport
// failure: it must not burn retries and then dead-letter.
func TestMissingProviderCredentialsSkipRatherThanFail(t *testing.T) {
	e := honestyEngine(newMemStore(), DeliveryConfig{})
	cases := []struct{ channel, target string }{
		{"telegram", "12345"},
		{"email", "a@b.c"},
		{"call", "+70000000000"},
	}
	for _, tc := range cases {
		res := e.deliverNotificationViaAdapter(context.Background(),
			notificationFor(tc.channel, tc.target, map[string]any{"title": "Boom"}))
		if res.Status != deliverySkipped {
			t.Errorf("%s: status = %q (%s), want skipped", tc.channel, res.Status, res.Err)
		}
		if res.ProviderStatus != skipNotConfigured {
			t.Errorf("%s: reason = %q", tc.channel, res.ProviderStatus)
		}
	}
}

// The log channel's contract is the log line; it may report delivered because
// it actually writes one.
func TestLogChannelStillDelivers(t *testing.T) {
	e := honestyEngine(newMemStore(), DeliveryConfig{})
	res := e.deliverNotificationViaAdapter(context.Background(),
		notificationFor("log", "", map[string]any{"title": "Boom"}))
	if res.Status != deliveryDelivered || res.ProviderStatus != "log" {
		t.Fatalf("outcome = %#v, want delivered/log", res)
	}
}

// applyOutcome is the state machine: skipped is terminal and counted; failed
// goes to retry; delivered records the provider status.
func TestApplyOutcomeStates(t *testing.T) {
	sink := newRecordingSink()
	e := honestyEngine(newMemStore(), DeliveryConfig{MaxRetries: 3, RetryDelays: []int{1, 5}})
	e.SetMetricsSink(sink)
	ts := utils.ToISO(utils.UTCNow())

	skippedNtf := model.WrapNotification(notificationFor("mobile", "dev_1", nil))
	e.applyOutcome(skippedNtf, skipped(skipNotConfigured, "no relay"), ts)
	if skippedNtf.Status() != model.NotificationSkipped {
		t.Errorf("skip status = %q", skippedNtf.Status())
	}
	if !skippedNtf.IsSkipped() {
		t.Error("IsSkipped false after a skip")
	}
	if raw := skippedNtf.Raw(); raw["provider_status"] != skipNotConfigured || raw["next_retry_at"] != nil {
		t.Errorf("skipped row = %#v", raw)
	}
	if sink.get(sink.skipped, "mobile:"+skipNotConfigured) != 1 {
		t.Error("skip was not counted for the operator")
	}

	failedNtf := model.WrapNotification(notificationFor("webhook", "https://x", nil))
	e.applyOutcome(failedNtf, failed("http_post", "HTTP 500", 500, "boom"), ts)
	if failedNtf.Status() != model.NotificationRetryScheduled {
		t.Errorf("failure status = %q, want retry_scheduled", failedNtf.Status())
	}

	okNtf := model.WrapNotification(notificationFor("webhook", "https://x", nil))
	e.applyOutcome(okNtf, delivered("http_post", 200, "ok"), ts)
	if okNtf.Status() != model.NotificationDelivered {
		t.Errorf("delivered status = %q", okNtf.Status())
	}
	if okNtf.Raw()["provider_status"] != "http_post" {
		t.Errorf("provider status not recorded: %#v", okNtf.Raw())
	}
}

func TestDeliveryAttemptRowCarriesDiagnostics(t *testing.T) {
	n := model.WrapNotification(notificationFor("webhook", "https://x", nil))
	row := deliveryAttemptRow(n, 2, failed("http_post", "HTTP 403", 403, `{"error":"forbidden"}`),
		"2026-06-01T00:00:00Z", "2026-06-01T00:00:01Z", 1500*1e6)

	if row["provider_code"] != 403 {
		t.Errorf("provider_code = %v, want 403", row["provider_code"])
	}
	if row["duration_ms"] != int64(1500) {
		t.Errorf("duration_ms = %v, want 1500", row["duration_ms"])
	}
	if row["provider_status"] != "http_post" || row["status"] != deliveryFailed {
		t.Errorf("row = %#v", row)
	}
	if resp, _ := row["provider_response"].(string); !strings.Contains(resp, "forbidden") {
		t.Errorf("provider_response = %q", resp)
	}
}

func TestRedactProviderResponse(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"token":"abc123","ok":false}`, `{"token":"***","ok":false}`},
		{`bot4242:AAH-secret failed`, `bot*** failed`},
		{"line\nbreak", "line break"},
	}
	for _, tc := range cases {
		if got := redactProviderResponse(tc.in); got != tc.want {
			t.Errorf("redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("x", maxProviderResponse*3)
	if got := redactProviderResponse(long); len(got) > maxProviderResponse+4 {
		t.Errorf("response not bounded: %d chars", len(got))
	}
	if redactProviderResponse("") != "" {
		t.Error("empty response should stay empty")
	}
}

// --- test push and chatops message history ---

func TestSendTestPushReportsTheTruth(t *testing.T) {
	ctx := context.Background()
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "u_a", "name": "A", "username": "a"})
	ms.seed("mobile_devices",
		map[string]any{"id": "dev_1", "user_id": "u_a", "platform": "ios", "push_token": "tok", "active": true},
		map[string]any{"id": "dev_off", "user_id": "u_a", "platform": "ios", "push_token": "tok", "active": false},
	)

	// No relay: the answer must say so rather than report a silent success.
	e := honestyEngine(ms, DeliveryConfig{})
	res, err := e.SendTestPush(ctx, "u_a")
	if err != nil {
		t.Fatalf("SendTestPush: %v", err)
	}
	if res["push_configured"] != false {
		t.Errorf("push_configured = %v, want false", res["push_configured"])
	}
	if res["delivered"] != 0 {
		t.Errorf("delivered = %v, want 0", res["delivered"])
	}
	devices := res["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1 (inactive device must be skipped)", len(devices))
	}
	if got := devices[0].(map[string]any)["status"]; got != deliverySkipped {
		t.Errorf("device status = %v, want skipped", got)
	}

	// With a relay: a real call, a real verdict.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	e = honestyEngine(ms, DeliveryConfig{MobilePushURL: srv.URL})
	res, err = e.SendTestPush(ctx, "u_a")
	if err != nil {
		t.Fatalf("SendTestPush with relay: %v", err)
	}
	if res["push_configured"] != true || res["delivered"] != 1 {
		t.Errorf("result = %#v, want configured + 1 delivered", res)
	}

	if _, err := e.SendTestPush(ctx, "u_ghost"); err == nil {
		t.Error("expected not-found for an unknown user")
	}
}

// The ChatOps message history is a second place that used to claim an outbound
// message regardless of whether anything could be sent.
func TestChatopsMessageHistoryRecordsWhetherItCouldBeSent(t *testing.T) {
	state := store.NewState()
	state.ChatopsChannels["chat_mute"] = map[string]any{
		"id": "chat_mute", "platform": "slack", "name": "#no-webhook",
		"user_id": "u_a", "notifications_enabled": true,
	}
	state.ChatopsChannels["chat_live"] = map[string]any{
		"id": "chat_live", "platform": "slack", "name": "#wired",
		"user_id": "u_a", "notifications_enabled": true,
		"webhook_url": "https://hooks.example/T/B/X",
	}
	group := model.WrapAlertGroup(map[string]any{
		"id": "grp_1", "integration_id": "int_1", "title": "Boom",
		"severity": "critical", "status": "open", "log": []any{},
	})
	user := map[string]any{"id": "u_a", "name": "A", "username": "a"}

	e := honestyEngine(newMemStore(), DeliveryConfig{})
	e.fanoutChatopsNotifications(state, group, user, "test", utils.ToISO(utils.UTCNow()), map[string]struct{}{})

	byChannel := map[string]map[string]any{}
	for _, raw := range state.ChatopsMessages {
		msg := raw
		byChannel[utils.StrVal(msg, "channel_id")] = msg
	}
	if got := utils.StrVal(byChannel["chat_mute"], "delivery_status"); got != "skipped_no_transport" {
		t.Errorf("channel without a webhook recorded %q, want skipped_no_transport", got)
	}
	if got := utils.StrVal(byChannel["chat_live"], "delivery_status"); got != "queued" {
		t.Errorf("wired channel recorded %q, want queued", got)
	}
	// Each history row points at the notification whose delivery carries the
	// final answer, so "was it actually sent" is one hop away.
	for id, msg := range byChannel {
		if utils.StrVal(msg, "notification_id") == "" {
			t.Errorf("%s message has no notification_id", id)
		}
	}
}

// --- read-path redaction ---

// A push token is what reaches someone's phone and a ChatOps webhook URL is the
// permission to post in a channel. Both used to come back verbatim from the
// ordinary read endpoints.
func TestReadPathRedactsCredentials(t *testing.T) {
	ctx := context.Background()
	ms := newMemStore()
	ms.seed("mobile_devices", map[string]any{
		"id": "dev_1", "user_id": "u_a", "platform": "ios",
		"push_token": "abcdefgh-token-9f2c", "active": true,
	})
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "platform": "slack", "name": "#sre",
		"webhook_url": "https://hooks.slack.com/services/T/B/xyz123",
	})
	e := honestyEngine(ms, DeliveryConfig{})

	viewer := authz.NewContext(ctx, authz.Actor{
		ID: "u_v", Kind: authz.KindUser, Role: authz.RoleViewer,
	})
	page, err := e.ListCollectionPage(viewer, "mobile_devices", map[string]any{})
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	device := page["items"].([]map[string]any)[0]
	if got := utils.StrVal(device, "push_token"); got != "***9f2c" {
		t.Errorf("push_token = %q, want masked", got)
	}
	item, err := e.GetItem(viewer, "mobile_devices", "dev_1")
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if got := utils.StrVal(item, "push_token"); got != "***9f2c" {
		t.Errorf("push_token from GetItem = %q, want masked", got)
	}

	// The channel's webhook stays hidden from a reader who cannot configure it…
	page, _ = e.ListCollectionPage(viewer, "chatops_channels", map[string]any{})
	channel := page["items"].([]map[string]any)[0]
	if got := utils.StrVal(channel, "webhook_url"); got != "***z123" {
		t.Errorf("webhook_url for viewer = %q, want masked", got)
	}
	// …and visible to an editor, who owns the settings form that sets it.
	editor := authz.NewContext(ctx, authz.Actor{
		ID: "u_e", Kind: authz.KindUser, Role: authz.RoleEditor,
	})
	page, _ = e.ListCollectionPage(editor, "chatops_channels", map[string]any{})
	channel = page["items"].([]map[string]any)[0]
	if got := utils.StrVal(channel, "webhook_url"); got != "https://hooks.slack.com/services/T/B/xyz123" {
		t.Errorf("webhook_url for editor = %q, want the real value", got)
	}

	// Delivery reads through the store, so masking must not reach it.
	res := e.deliverNotificationViaAdapter(ctx, notificationFor("mobile", "dev_1", nil))
	if res.Status != deliverySkipped { // no relay configured in this engine
		t.Fatalf("unexpected outcome %#v", res)
	}
	stored, _ := ms.GetItem(ctx, "mobile_devices", "dev_1")
	if utils.StrVal(stored, "push_token") != "abcdefgh-token-9f2c" {
		t.Errorf("stored token was mutated: %q", utils.StrVal(stored, "push_token"))
	}
}

func TestMaskKeepsOnlyATail(t *testing.T) {
	cases := map[string]string{"": "", "abc": "***", "abcd": "***", "abcde": "***bcde"}
	for in, want := range cases {
		if got := mask(in); got != want {
			t.Errorf("mask(%q) = %q, want %q", in, got, want)
		}
	}
}

// A phone paired through the app has no push token — it hears about groups
// from the event stream. Paging its owner created a mobile notification for it
// anyway, which could only be skipped and fired the chart's
// NotificationsSkippedNoTransport alert for a page the phone had rung for.
func TestMobileFanoutSkipsPairedPhonesWithoutPushToken(t *testing.T) {
	state := store.NewState()
	state.MobileDevices["dev_paired"] = map[string]any{"id": "dev_paired", "user_id": "u_a", "platform": "android", "active": true}
	state.MobileDevices["dev_paired_again"] = map[string]any{"id": "dev_paired_again", "user_id": "u_a", "platform": "android", "active": true, "push_token": ""}
	state.MobileDevices["dev_push"] = map[string]any{"id": "dev_push", "user_id": "u_a", "platform": "ios", "active": true, "push_token": "env:PUSH_TOKEN_A"}
	state.MobileDevices["dev_other"] = map[string]any{"id": "dev_other", "user_id": "u_b", "platform": "ios", "active": true, "push_token": "tok"}
	group := model.WrapAlertGroup(map[string]any{
		"id": "grp_1", "integration_id": "int_1", "title": "Boom",
		"severity": "critical", "status": "open", "log": []any{},
	})
	user := map[string]any{"id": "u_a", "name": "A", "username": "a"}

	e := honestyEngine(newMemStore(), DeliveryConfig{MobilePushURL: "https://relay.example/push"})
	e.fanoutMobileNotifications(state, group, user, "test", utils.ToISO(utils.UTCNow()), map[string]struct{}{})

	var targets []string
	for _, rec := range state.Notifications {
		if n, ok := rec.(model.Notification); ok && n.Channel() == "mobile" {
			targets = append(targets, n.Target())
		}
	}
	if len(targets) != 1 || targets[0] != "dev_push" {
		t.Errorf("mobile notifications went to %v, want only the device with a push token", targets)
	}
}
