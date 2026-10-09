package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func TestMessageIDAt(t *testing.T) {
	cases := []struct {
		body, path, want string
	}{
		{`{"id": "m-1"}`, "id", "m-1"},
		{`{"id": 1234567890123456789}`, "id", "1234567890123456789"},
		{`{"ok": true, "result": {"message_id": 7}}`, "result.message_id", "7"},
		{`{"messages": [{"id": "first"}, {"id": "second"}]}`, "messages.1.id", "second"},
		{`{"id": {"nested": true}}`, "id", ""},
		{`{"other": "x"}`, "id", ""},
		{`ok`, "id", ""},
	}
	for _, tc := range cases {
		if got := messageIDAt([]byte(tc.body), tc.path); got != tc.want {
			t.Errorf("messageIDAt(%s, %q) = %q, want %q", tc.body, tc.path, got, tc.want)
		}
	}
}

func TestSanitizeChatopsMessageUpdate(t *testing.T) {
	got, err := sanitizeChatopsMessageUpdate(map[string]any{"url": "https://chat.example.com/api/messages/{message_id}"})
	if err != nil {
		t.Fatal(err)
	}
	if got["method"] != http.MethodPatch || got["message_id_path"] != "id" {
		t.Errorf("defaults = %v, want PATCH and id", got)
	}
	for _, cleared := range []any{nil, map[string]any{}} {
		if got, err := sanitizeChatopsMessageUpdate(cleared); err != nil || got != nil {
			t.Errorf("%v: got %v, %v; want it removed", cleared, got, err)
		}
	}
	for _, bad := range []map[string]any{
		{"url": "https://chat.example.com/x", "method": "DELETE"},
		{"method": "PUT"},
		{"url": "chat.example.com/messages"},
		{"url": "ftp://chat.example.com/messages"},
	} {
		if _, err := sanitizeChatopsMessageUpdate(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// chatAPI is a chat platform that answers a post with the message it created
// and accepts edits by id.
type chatAPI struct {
	mu        sync.Mutex
	requests  []string
	bodies    []map[string]any
	editCode  int
	postReply string
}

func newChatAPI(t *testing.T) (*chatAPI, *httptest.Server) {
	t.Helper()
	api := &chatAPI{editCode: http.StatusOK, postReply: `{"ok": true, "result": {"id": "m-42"}}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		api.requests = append(api.requests, r.Method+" "+r.URL.Path)
		api.bodies = append(api.bodies, body)
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(api.postReply))
			return
		}
		w.WriteHeader(api.editCode)
	}))
	t.Cleanup(srv.Close)
	return api, srv
}

func (a *chatAPI) log() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func editingChannel(srvURL string) map[string]any {
	return map[string]any{
		"id": "chat_1", "platform": "mattermost", "name": "#sre", "user_id": "usr-1",
		"notifications_enabled": true, "commands_enabled": true,
		"webhook_url": srvURL + "/hooks/1",
		"message_update": map[string]any{
			"method": "PUT", "url": srvURL + "/api/messages/{message_id}", "message_id_path": "result.id",
		},
	}
}

// The whole path through the worker: the alert is posted and its id kept, the
// acknowledgement is queued by the API call, and the next delivery cycle
// edits the message instead of posting another.
func TestAcknowledgeEditsTheAlertMessageThroughTheQueue(t *testing.T) {
	api, srv := newChatAPI(t)
	ms := ingestStore()
	ms.seed("chatops_channels", editingChannel(srv.URL))
	e := deliveryEngine(ms)
	e.deliveryCfg.ChatopsStatusUpdates = true
	ctx := context.Background()

	id := ingestOne(t, e, "disk full")["id"].(string)
	if _, err := e.ProcessNotificationDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	// The API call only queued it.
	if got := api.log(); len(got) != 1 {
		t.Fatalf("requests before the delivery cycle = %v, want only the alert post", got)
	}
	if _, err := e.ProcessNotificationDeliveries(ctx); err != nil {
		t.Fatal(err)
	}

	got := api.log()
	want := []string{"POST /hooks/1", "PUT /api/messages/m-42"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	edit := api.bodies[1]
	if edit["message_id"] != "m-42" {
		t.Errorf("edit body message_id = %v", edit["message_id"])
	}
	if text, _ := edit["text"].(string); !strings.Contains(text, "acknowledged by usr-admin") || !strings.Contains(text, "disk full") {
		t.Errorf("edit text = %q, want the alert with its new status", text)
	}
	for _, row := range ms.data["notifications"] {
		n := model.WrapNotification(row)
		if n.Channel() == "chatops" && n.Status() != model.NotificationDelivered {
			t.Errorf("chatops notification %s is %s, want delivered", n.Reason(), n.Status())
		}
	}
}

// deliverStatus runs one status message for chat_1 against the alert
// notification seeded as alert.
func deliverStatus(t *testing.T, e *Engine, ms *memStore, alert map[string]any, retryCount int) deliveryOutcome {
	t.Helper()
	ms.seed("notifications", alert)
	ntf := notificationFor("chatops", "chat_1", map[string]any{
		"alert_group_id": "grp_1", "title": "Disk full", "reason": "acknowledged by alice",
		"chatops_channel_id": "chat_1", "chatops_event": chatopsEventAcknowledged,
		"chatops_alert_notification_id": utils.StrVal(alert, "id"),
	})
	ntf["retry_count"] = retryCount
	return e.deliverNotificationViaAdapter(context.Background(), ntf)
}

func alertNotification(status, messageID string) map[string]any {
	n := model.NewNotification("grp_1", "int_1", "usr-1", "chatops", "chat_1", "escalation", utils.ToISO(utils.UTCNow()), "alert-1")
	n.ScheduleDelivery(map[string]any{"title": "Disk full"})
	n.SetProviderMessageID(messageID)
	raw := n.Raw()
	raw["status"] = status
	return raw
}

func updateEngine(t *testing.T) (*Engine, *memStore, *chatAPI) {
	t.Helper()
	api, srv := newChatAPI(t)
	ms := newMemStore()
	ms.seed("chatops_channels", editingChannel(srv.URL))
	return deliveryEngine(ms), ms, api
}

func TestStatusFallsBackToANewMessage(t *testing.T) {
	cases := []struct {
		name       string
		alert      map[string]any
		retryCount int
		editCode   int
		want       []string
		wantStatus string
	}{
		{"no id was kept for the alert", alertNotification(model.NotificationDelivered, ""), 0, 200,
			[]string{"POST /hooks/1"}, deliveryDelivered},
		{"the alert failed", alertNotification(model.NotificationFailed, ""), 0, 200,
			[]string{"POST /hooks/1"}, deliveryDelivered},
		{"the alert is still on its way", alertNotification(model.NotificationRetryScheduled, ""), 0, 200,
			nil, deliveryFailed},
		{"the alert is still on its way, last attempt", alertNotification(model.NotificationRetryScheduled, ""), 2, 200,
			[]string{"POST /hooks/1"}, deliveryDelivered},
		{"the platform refuses the edit", alertNotification(model.NotificationDelivered, "m-42"), 0, 404,
			[]string{"PUT /api/messages/m-42", "POST /hooks/1"}, deliveryDelivered},
		{"the platform is down", alertNotification(model.NotificationDelivered, "m-42"), 0, 503,
			[]string{"PUT /api/messages/m-42"}, deliveryFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ms, api := updateEngine(t)
			api.editCode = tc.editCode
			res := deliverStatus(t, e, ms, tc.alert, tc.retryCount)
			if res.Status != tc.wantStatus {
				t.Errorf("status = %q (%s), want %q", res.Status, res.Err, tc.wantStatus)
			}
			if got := api.log(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("requests = %v, want %v", got, tc.want)
			}
		})
	}
}

// Without message_update nothing changes: the alert's response is not read and
// the status is a new message.
func TestChannelWithoutMessageUpdatePostsStatusAsNewMessage(t *testing.T) {
	api, srv := newChatAPI(t)
	ms := newMemStore()
	ch := editingChannel(srv.URL)
	delete(ch, "message_update")
	ms.seed("chatops_channels", ch)
	e := deliveryEngine(ms)

	alert := e.deliverNotificationViaAdapter(context.Background(), notificationFor("chatops", "chat_1",
		map[string]any{"alert_group_id": "grp_1", "title": "Disk full"}))
	if alert.Status != deliveryDelivered || alert.MessageID != "" {
		t.Fatalf("alert outcome = %+v, want delivered without a kept id", alert)
	}
	if res := deliverStatus(t, e, ms, alertNotification(model.NotificationDelivered, "m-42"), 0); res.Status != deliveryDelivered {
		t.Fatalf("status outcome = %+v", res)
	}
	if got := api.log(); strings.Join(got, ",") != "POST /hooks/1,POST /hooks/1" {
		t.Errorf("requests = %v, want two posts", got)
	}
}

// A message id the platform did not return is not fatal: the alert is
// delivered, and only the later edit becomes a new message.
func TestAlertDeliveredWhenTheIDIsMissing(t *testing.T) {
	e, _, api := updateEngine(t)
	api.postReply = `ok`
	res := e.deliverNotificationViaAdapter(context.Background(), notificationFor("chatops", "chat_1",
		map[string]any{"alert_group_id": "grp_1", "title": "Disk full"}))
	if res.Status != deliveryDelivered || res.MessageID != "" {
		t.Errorf("outcome = %+v, want delivered with no id", res)
	}
}

// The edit URL can carry the platform's credential, like the webhook URL.
func TestMessageUpdateURLIsMaskedForReaders(t *testing.T) {
	viewer := authz.NewContext(context.Background(), authz.Actor{ID: "u_v", Kind: authz.KindUser, Role: authz.RoleViewer})
	item := redactForReader(viewer, "chatops_channels", editingChannel("https://chat.example.com"))
	update, _ := item["message_update"].(map[string]any)
	if u := utils.StrVal(update, "url"); strings.Contains(u, "chat.example.com") {
		t.Errorf("message_update.url = %q, want masked", u)
	}
}

// An edit replaces the message, so an older status retried after a newer one
// must not be sent: it would turn "resolved" back into "acknowledged".
func TestOutdatedStatusEditIsNotSent(t *testing.T) {
	e, ms, api := updateEngine(t)
	ms.seed("alert_groups", map[string]any{"id": "grp_1", "chatops_status_seq": 2})
	alert := alertNotification(model.NotificationDelivered, "m-42")

	ms.seed("notifications", alert)
	older := notificationFor("chatops", "chat_1", map[string]any{
		"alert_group_id": "grp_1", "title": "Disk full", "chatops_channel_id": "chat_1",
		"chatops_event": chatopsEventAcknowledged, "chatops_status_seq": 1,
		"chatops_alert_notification_id": utils.StrVal(alert, "id"),
	})
	res := e.deliverNotificationViaAdapter(context.Background(), older)
	if res.Status != deliverySkipped || res.ProviderStatus != skipSuperseded {
		t.Fatalf("outdated edit = %+v, want skipped as superseded", res)
	}
	if got := api.log(); len(got) != 0 {
		t.Fatalf("outdated edit reached the platform: %v", got)
	}

	newer := notificationFor("chatops", "chat_1", map[string]any{
		"alert_group_id": "grp_1", "title": "Disk full", "chatops_channel_id": "chat_1",
		"chatops_event": chatopsEventResolved, "chatops_status_seq": 2,
		"chatops_alert_notification_id": utils.StrVal(alert, "id"),
	})
	if res := e.deliverNotificationViaAdapter(context.Background(), newer); res.Status != deliveryDelivered {
		t.Fatalf("current edit = %+v", res)
	}
	if got := api.log(); strings.Join(got, ",") != "PUT /api/messages/m-42" {
		t.Errorf("requests = %v", got)
	}
}

// A superseded edit is not "nobody was told": it must not reach the skipped
// metric behind the NotificationsSkippedNoTransport alert.
func TestSupersededEditIsNotCountedAsSkipped(t *testing.T) {
	sink := newRecordingSink()
	e := &Engine{metrics: sink}
	n := model.WrapNotification(notificationFor("chatops", "chat_1", nil))
	e.applyOutcome(n, skipped(skipSuperseded, "newer"), utils.ToISO(utils.UTCNow()))
	if n.Status() != model.NotificationSkipped {
		t.Errorf("status = %s, want skipped", n.Status())
	}
	if len(sink.skipped) != 0 {
		t.Errorf("superseded edit counted as skipped: %v", sink.skipped)
	}
}

// Waiting for the alert is not a provider failure: a burst of waiting edits
// must not open the channel's breaker and hold back the alert itself.
func TestWaitingEditDoesNotTripTheBreaker(t *testing.T) {
	e, ms, api := updateEngine(t)
	e.breaker = newCircuitBreaker(1, time.Hour)
	ms.seed("alert_groups", map[string]any{"id": "grp_1"})
	pending := alertNotification(model.NotificationRetryScheduled, "")
	ms.seed("notifications", pending)
	status := makeNotification("ntf_status", model.NotificationDeliveryScheduled, "chatops", "chat_1", map[string]any{
		"alert_group_id": "grp_1", "title": "Disk full", "chatops_channel_id": "chat_1",
		"chatops_event": chatopsEventAcknowledged, "chatops_status_seq": 1,
		"chatops_alert_notification_id": utils.StrVal(pending, "id"),
	})
	ms.seed("notifications", status)

	if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := model.WrapNotification(ms.row("notifications", "ntf_status")).Status(); got != model.NotificationRetryScheduled {
		t.Fatalf("waiting edit is %s, want retry_scheduled", got)
	}
	if !e.breaker.Allow("chatops:chat_1", utils.UTCNow()) {
		t.Error("the breaker opened on a delivery that never reached the provider")
	}
	if got := api.log(); len(got) != 0 {
		t.Errorf("requests = %v, want none while the alert is pending", got)
	}
}

// The edit URL often carries a credential, and a transport error quotes the
// URL whole; the attempt row and last_error are readable by anyone who can see
// the group.
func TestEditErrorDoesNotQuoteTheURL(t *testing.T) {
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "platform": "generic", "name": "#sre", "webhook_url": "http://127.0.0.1:1/hooks/1",
		"message_update": map[string]any{"url": "http://127.0.0.1:1/bot-s3cret-token/messages/{message_id}?key=k3y"},
	})
	e := deliveryEngine(ms)
	alert := alertNotification(model.NotificationDelivered, "m-42")
	res := deliverStatus(t, e, ms, alert, 0)
	if res.Status != deliveryFailed {
		t.Fatalf("outcome = %+v, want a transport failure", res)
	}
	for _, leak := range []string{"s3cret", "k3y"} {
		if strings.Contains(res.Err, leak) || strings.Contains(res.Response, leak) {
			t.Errorf("stored diagnostics quote the edit URL: err=%q response=%q", res.Err, res.Response)
		}
	}
	if !strings.Contains(res.Err, "http://127.0.0.1:1/…") {
		t.Errorf("err = %q, want the endpoint's scheme and host kept", res.Err)
	}
}
