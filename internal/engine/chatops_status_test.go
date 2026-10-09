package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// statusStore is ingestStore plus a ChatOps channel bound to the paged user,
// so an ingested alert reaches it.
func statusStore(webhookURL string) *memStore {
	ms := ingestStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "platform": "mattermost", "name": "#sre", "user_id": "usr-1",
		"notifications_enabled": true, "commands_enabled": true, "webhook_url": webhookURL,
	})
	return ms
}

func statusEngine(ms *memStore) *Engine {
	e := crudEngine(ms)
	e.deliveryCfg.ChatopsStatusUpdates = true
	return e
}

// statusMessages returns the channel status notifications, by event.
func statusMessages(ms *memStore) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	for _, row := range ms.data["notifications"] {
		payload, _ := row["payload"].(map[string]any)
		if ev := utils.StrVal(payload, "chatops_event"); ev != "" {
			out[ev] = append(out[ev], row)
		}
	}
	return out
}

func pagedGroup(t *testing.T, e *Engine, ms *memStore) string {
	t.Helper()
	id := ingestOne(t, e, "disk full")["id"].(string)
	refs := model.WrapAlertGroup(ms.data["alert_groups"][id]).NotifiedChatChannels()
	if len(refs) != 1 || refs[0].ID != "chat_1" || refs[0].Channel != "chatops" {
		t.Fatalf("group did not record the channel it was posted to: %+v", refs)
	}
	return id
}

func TestAcknowledgeTellsTheChannelOnce(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	id := pagedGroup(t, e, ms)

	for range 2 {
		if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
			t.Fatalf("AcknowledgeGroup: %v", err)
		}
	}

	acks := statusMessages(ms)[chatopsEventAcknowledged]
	if len(acks) != 1 {
		t.Fatalf("%d acknowledged messages for one acknowledgement, want 1", len(acks))
	}
	n := model.WrapNotification(acks[0])
	if n.Channel() != "chatops" || n.Target() != "chat_1" || n.Status() != model.NotificationDeliveryScheduled {
		t.Errorf("status message = %s/%s/%s, want a queued chatops message to chat_1", n.Channel(), n.Target(), n.Status())
	}
	if !strings.Contains(n.Reason(), "acknowledged by usr-admin") {
		t.Errorf("reason = %q, want who acknowledged", n.Reason())
	}
	var history int
	for _, msg := range ms.data["chatops_messages"] {
		if utils.StrVal(msg, "notification_id") == n.ID() {
			history++
		}
	}
	if history != 1 {
		t.Errorf("%d history rows for the status message, want 1", history)
	}
}

func TestUnacknowledgeAndResolveTellTheChannel(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	id := pagedGroup(t, e, ms)

	if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.UnacknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResolveGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResolveGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}

	got := statusMessages(ms)
	for _, ev := range []string{chatopsEventAcknowledged, chatopsEventUnacknowledged, chatopsEventResolved} {
		if len(got[ev]) != 1 {
			t.Errorf("%d %s messages, want 1", len(got[ev]), ev)
		}
	}
	// The personal resolve notice is a separate switch and stays off.
	if n := len(resolvedNotifications(ms)); n != 0 {
		t.Errorf("%d personal resolve notices with NXS_ANOMALY_NOTIFY_ON_RESOLVE off", n)
	}
}

func TestBulkTransitionsTellTheChannel(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	id := pagedGroup(t, e, ms)

	if _, err := e.BulkAcknowledgeGroups(adminCtx(), []string{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.BulkResolveGroups(adminCtx(), []string{id}); err != nil {
		t.Fatal(err)
	}
	got := statusMessages(ms)
	if len(got[chatopsEventAcknowledged]) != 1 || len(got[chatopsEventResolved]) != 1 {
		t.Errorf("bulk ack/resolve posted %d/%d status messages, want 1/1",
			len(got[chatopsEventAcknowledged]), len(got[chatopsEventResolved]))
	}
}

func TestSourceResolveTellsTheChannel(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	pagedGroup(t, e, ms)

	for range 2 {
		if _, err := e.IngestAlert(context.Background(), "key-1", map[string]any{
			"title": "disk full", "status": "resolved", "labels": map[string]any{"alertname": "disk full"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	resolved := statusMessages(ms)[chatopsEventResolved]
	if len(resolved) != 1 {
		t.Fatalf("%d resolved messages after the source closed the group, want 1", len(resolved))
	}
	if reason := model.WrapNotification(resolved[0]).Reason(); reason != "resolved by the source" {
		t.Errorf("reason = %q", reason)
	}
}

// A command typed in the chat — and a button, which runs the same command —
// is a transition like any other.
func TestChatCommandsTellTheChannel(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	id := pagedGroup(t, e, ms)

	for _, cmd := range []string{"ack " + id, "ack " + id, "unack " + id, "resolve " + id} {
		if _, err := e.PostChatopsCommand(adminCtx(), map[string]any{
			"channel_id": "chat_1", "command": cmd, "actor": "alice",
		}); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	got := statusMessages(ms)
	for _, ev := range []string{chatopsEventAcknowledged, chatopsEventUnacknowledged, chatopsEventResolved} {
		if len(got[ev]) != 1 {
			t.Errorf("%d %s messages from chat commands, want 1", len(got[ev]), ev)
		}
	}
}

func TestStatusUpdatesCanBeTurnedOff(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	e.deliveryCfg.ChatopsStatusUpdates = false
	id := pagedGroup(t, e, ms)

	if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if got := statusMessages(ms); len(got) != 0 {
		t.Errorf("status messages with the switch off: %v", got)
	}
}

// A channel with no transport never showed the alert, so it is not told that
// the alert changed either.
func TestChannelWithoutTransportGetsNoStatus(t *testing.T) {
	ms := statusStore("")
	e := statusEngine(ms)
	id := ingestOne(t, e, "disk full")["id"].(string)

	if refs := model.WrapAlertGroup(ms.data["alert_groups"][id]).NotifiedChatChannels(); len(refs) != 0 {
		t.Fatalf("a channel without a webhook was recorded: %+v", refs)
	}
	if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if got := statusMessages(ms); len(got) != 0 {
		t.Errorf("status messages for a channel without transport: %v", got)
	}
}

// The status message is queued without reading the channel, so a channel
// deleted or muted in the meantime is found out at delivery.
func TestStatusForAGoneChannelIsSkipped(t *testing.T) {
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_muted", "name": "#muted", "platform": "slack",
		"webhook_url": "https://chat.example.com/hooks/1", "notifications_enabled": false,
	})
	e := honestyEngine(ms, DeliveryConfig{})
	for _, id := range []string{"chat_muted", "chat_deleted"} {
		res := e.deliverNotificationViaAdapter(context.Background(), notificationFor("chatops", id, map[string]any{
			"alert_group_id": "grp_1", "chatops_channel_id": id, "chatops_event": chatopsEventAcknowledged,
		}))
		if res.Status != deliverySkipped {
			t.Errorf("%s: status = %q (%s), want skipped", id, res.Status, res.Err)
		}
	}
}

// A status message to a Telegram channel carries no buttons: they belong under
// the alert, not under the news that somebody already took it.
func TestTelegramStatusMessageHasNoKeyboard(t *testing.T) {
	payload := telegramMessageWithActions("-100123", "acknowledged", "", telegramShiftOptions{}, "https://anomaly.example.com")
	if _, has := payload["reply_markup"]; has {
		t.Errorf("status message carries a keyboard: %v", payload["reply_markup"])
	}
}

// The template context knows which status change it is rendering, and has a
// user_name even when the message has no recipient person.
func TestTemplateContextHasEvent(t *testing.T) {
	tmpl := "{{ if .event }}{{ .title }} {{ .event }}{{ else }}{{ .title }} fired{{ end }}|{{ .user_name }}"
	n := notificationFor("chatops", "chat_1", nil)
	got := renderNotificationText(n, map[string]any{"title": "Disk full", "chatops_event": "resolved"}, tmpl)
	if got != "Disk full resolved|" {
		t.Errorf("status text = %q", got)
	}
	got = renderNotificationText(n, map[string]any{"title": "Disk full", "user": map[string]any{"name": "Alice"}}, tmpl)
	if got != "Disk full fired|Alice" {
		t.Errorf("alert text = %q", got)
	}
}

// An acknowledge, its undo and a second acknowledge are three messages even
// inside one second — the keys must not be built from a timestamp kept to the
// second, or the database's unique key drops the second acknowledge.
func TestStatusKeysAreDistinctWithinASecond(t *testing.T) {
	ms := statusStore("https://chat.example.com/hooks/1")
	e := statusEngine(ms)
	id := pagedGroup(t, e, ms)

	if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.UnacknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AcknowledgeGroup(adminCtx(), id); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	var count int
	for _, rows := range statusMessages(ms) {
		for _, row := range rows {
			keys[model.WrapNotification(row).IdempotencyKey()] = true
			count++
		}
	}
	if count != 3 || len(keys) != 3 {
		t.Errorf("%d status messages with %d distinct keys, want 3 and 3", count, len(keys))
	}
}

// Status messages are a new class of message in every existing channel, so an
// upgrade leaves them off until the operator turns them on.
func TestStatusUpdatesAreOffUnlessAskedFor(t *testing.T) {
	t.Setenv("NXS_ANOMALY_CHATOPS_STATUS_UPDATES", "")
	if DeliveryConfigFromEnv().ChatopsStatusUpdates {
		t.Error("status updates are on without NXS_ANOMALY_CHATOPS_STATUS_UPDATES")
	}
	t.Setenv("NXS_ANOMALY_CHATOPS_STATUS_UPDATES", "true")
	if !DeliveryConfigFromEnv().ChatopsStatusUpdates {
		t.Error("NXS_ANOMALY_CHATOPS_STATUS_UPDATES=true did not turn them on")
	}
}
