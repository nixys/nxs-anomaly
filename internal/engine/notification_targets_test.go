package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
)

func TestTargetWithoutAddressIsEmptyNotNil(t *testing.T) {
	got, err := sanitizeNotificationTargets([]any{map[string]any{"type": "telegram"}, "email"}, ChannelPolicy{})
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want two targets", got)
	}
	for _, raw := range got {
		target := raw.(map[string]any)["target"]
		if target != "" {
			t.Errorf("target = %q, want empty — %q was sent to Telegram as a chat id", target, "<nil>")
		}
	}
	if got[1].(map[string]any)["type"] != "email" {
		t.Errorf("bare channel name not accepted: %v", got[1])
	}
}

func TestTargetWithoutAddressUsesTheProfile(t *testing.T) {
	e := newEngine()
	s := store.NewState()
	user := map[string]any{"id": "u1", "telegram_id": "4242",
		"notification_targets": []any{map[string]any{"type": "telegram", "target": ""}}}
	got := e.notificationTargetsForUser(s, model.WrapAlertGroup(newGroup(0, 0)), user)
	if len(got) != 1 || got[0]["type"] != "telegram" || got[0]["target"] != "4242" {
		t.Errorf("targets = %v, want telegram to the profile's telegram_id", got)
	}
}

func TestTestNotificationFindsTheWebhookAddress(t *testing.T) {
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "u1", "username": "hook",
		"notification_targets": []any{map[string]any{"type": "webhook", "target": "http://127.0.0.1:1/hook"}}})
	e := New(ms)
	res, err := e.SendTestNotification(context.Background(), "u1", "webhook")
	if err != nil {
		t.Fatal(err)
	}
	if res["reason"] == "no_target" || res["target"] != "http://127.0.0.1:1/hook" {
		t.Errorf("result = %v, want an attempt to the configured webhook", res)
	}
	// Without a channel the test exercises the first configured one, not log.
	res, err = e.SendTestNotification(context.Background(), "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if res["channel"] != "webhook" {
		t.Errorf("channel = %v, want webhook", res["channel"])
	}
}
