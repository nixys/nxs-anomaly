package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
)

// A page that never arrived used to leave the group saying "Notified users".
func TestPermanentDeliveryFailureIsInTheGroupTimeline(t *testing.T) {
	ms := newMemStore()
	ms.seed("alert_groups", newGroup(1, 0))
	e := crudEngine(ms)

	n := model.WrapNotification(map[string]any{
		"id": "ntf-1", "alert_group_id": "grp1", "user_id": "u1", "channel": "webhook",
		"target": "http://down:9999", "status": model.NotificationFailed,
		"last_error": "connect: connection refused",
		"payload":    map[string]any{"user": map[string]any{"username": "foxtrot"}},
	})
	e.fireDeadLetters(context.Background(), []map[string]any{n.Raw()})

	logs, _ := ms.data["alert_groups"]["grp1"]["logs"].([]any)
	for _, raw := range logs {
		entry, _ := raw.(map[string]any)
		if entry["type"] == "delivery_failed" {
			msg, _ := entry["message"].(string)
			if !strings.Contains(msg, "foxtrot") || !strings.Contains(msg, "webhook") || !strings.Contains(msg, "connection refused") {
				t.Errorf("delivery_failed message %q does not say who, how and why", msg)
			}
			return
		}
	}
	t.Errorf("no delivery_failed entry in the group timeline: %v", logs)
}
