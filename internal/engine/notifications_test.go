package engine

import (
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
)

// --- notifyUsers tests ---

func TestNotifyUsersNonLogChannelSchedulesDelivery(t *testing.T) {
	e := newEngine()
	s := newState([]any{})
	s.Users["u1"]["notification_targets"] = []any{map[string]any{"type": "webhook", "target": "http://hook.test/x"}}
	integ, _ := s.Integrations["int1"]["notification_policy"].(map[string]any)
	integ["channels"] = []any{"webhook"}

	group := newGroup(0, 0)
	group["notification_channels"] = []any{"webhook"}

	e.notifyUsers(s, model.WrapAlertGroup(group), []string{"u1"}, "test", "2026-05-10T10:00:00+00:00")

	if len(s.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(s.Notifications))
	}
	for _, rec := range s.Notifications {
		n := notificationMap(rec)
		if n["status"] != "delivery_scheduled" {
			t.Errorf("expected status=delivery_scheduled, got %v", n["status"])
		}
		if n["payload"] == nil {
			t.Error("expected payload to be set")
		}
	}
}

// --- triggerWebhook tests ---

func TestTriggerWebhookSchedulesDelivery(t *testing.T) {
	e := newEngine()
	s := newState([]any{})
	group := newGroup(0, 0)

	g := model.WrapAlertGroup(group)
	e.triggerWebhook(s, g, "http://hook.test/escalation", nil, "2026-05-10T10:00:00+00:00")
	group = g.Raw()

	if len(s.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(s.Notifications))
	}
	for _, rec := range s.Notifications {
		n := notificationMap(rec)
		if n["status"] != "delivery_scheduled" {
			t.Errorf("expected status=delivery_scheduled, got %v", n["status"])
		}
		if n["channel"] != "webhook" {
			t.Errorf("expected channel=webhook, got %v", n["channel"])
		}
	}
	if !logsContainType(group, "webhook") {
		t.Error("expected 'webhook' log entry")
	}
}

// --- checkEpicThreshold tests ---

func TestCheckEpicThresholdByCount(t *testing.T) {
	e := newEngine()
	s := newState([]any{})
	s.Users["u_epic"] = map[string]any{
		"id":                   "u_epic",
		"username":             "epic",
		"name":                 "Epic",
		"notification_targets": []any{map[string]any{"type": "log", "target": ""}},
	}
	integration := map[string]any{
		"id": "int1",
		"notification_policy": map[string]any{
			"epic_user_id":           "u_epic",
			"epic_threshold_count":   3,
			"epic_threshold_seconds": 0,
		},
	}
	group := newGroup(0, 0)
	group["alert_count"] = 3

	g := model.WrapAlertGroup(group)
	e.checkEpicThreshold(s, g, integration, "2026-05-10T10:00:00+00:00")
	group = g.Raw()

	if group["epic_sent_at"] != "2026-05-10T10:00:00+00:00" {
		t.Errorf("expected epic_sent_at to be set, got %v", group["epic_sent_at"])
	}
	if !logsContainType(group, "epic_alert") {
		t.Error("expected 'epic_alert' log entry")
	}
}

func TestCheckEpicThresholdSkipsIfAlreadySent(t *testing.T) {
	e := newEngine()
	s := newState([]any{})
	s.Users["u_epic"] = map[string]any{
		"id":                   "u_epic",
		"notification_targets": []any{map[string]any{"type": "log", "target": ""}},
	}
	integration := map[string]any{
		"id": "int1",
		"notification_policy": map[string]any{
			"epic_user_id":           "u_epic",
			"epic_threshold_count":   1,
			"epic_threshold_seconds": 0,
		},
	}
	group := newGroup(0, 0)
	group["alert_count"] = 5
	group["epic_sent_at"] = "2026-05-10T09:00:00+00:00"

	e.checkEpicThreshold(s, model.WrapAlertGroup(group), integration, "2026-05-10T10:00:00+00:00")

	if group["epic_sent_at"] != "2026-05-10T09:00:00+00:00" {
		t.Error("epic_sent_at should not change when already sent")
	}
}

// --- sendEpicNotification webhook test ---

func TestSendEpicNotificationWebhookCreatesDeliveryScheduled(t *testing.T) {
	e := newEngine()
	s := newState([]any{})
	s.Users["u_epic"] = map[string]any{
		"id":                   "u_epic",
		"username":             "epic",
		"name":                 "Epic",
		"notification_targets": []any{map[string]any{"type": "webhook", "target": "http://epic.test/hook"}},
	}
	group := newGroup(0, 0)
	group["alert_count"] = 5

	e.sendEpicNotification(s, model.WrapAlertGroup(group), "u_epic", "2026-05-10T10:00:00+00:00")

	if len(s.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(s.Notifications))
	}
	for _, rec := range s.Notifications {
		n := notificationMap(rec)
		if n["status"] != "delivery_scheduled" {
			t.Errorf("expected status=delivery_scheduled, got %v", n["status"])
		}
		payload, _ := n["payload"].(map[string]any)
		if payload["epic_alert"] != true {
			t.Error("expected epic_alert=true in payload")
		}
	}
}

// --- renderNotificationText tests ---

func TestRenderNotificationTextFallback(t *testing.T) {
	notification := map[string]any{"reason": "escalation", "alert_group_id": "grp1"}
	payload := map[string]any{}
	text := renderNotificationText(notification, payload, "")
	if !strings.Contains(text, "Alert notification") {
		t.Errorf("expected fallback title, got %q", text)
	}
}

func TestRenderNotificationTextTemplate(t *testing.T) {
	notification := map[string]any{}
	payload := map[string]any{"title": "CPU High", "severity": "critical", "reason": "test", "alert_group_id": "g1", "status": "open"}
	text := renderNotificationText(notification, payload, "Alert: {{.title}} [{{.severity}}]")
	if !strings.Contains(text, "CPU High") || !strings.Contains(text, "critical") {
		t.Errorf("unexpected template output: %q", text)
	}
}
