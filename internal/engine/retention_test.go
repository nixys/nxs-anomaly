package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// TestRetentionSweepSkipsUnconfiguredCategories pins the default nobody should
// have to discover the hard way: an upgrade that ships this code must not start
// deleting a customer's history because a horizon defaulted to a number.
func TestRetentionSweepSkipsUnconfiguredCategories(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	e := New(st)
	e.deliveryCfg.Retention = RetentionPolicy{}

	old := utils.ToISO(utils.UTCNow().AddDate(0, 0, -400))
	st.Seed("notifications", map[string]any{
		"id": "ntf-old", "user_id": "usr-1", "status": "delivered", "updated_at": old,
	})

	deleted := e.RetentionSweep(ctx)
	if len(deleted) != 0 {
		t.Fatalf("nothing may be deleted with no horizon set, got %v", deleted)
	}
	items, _ := st.ListItemsIn(ctx, "notifications", "user_id", []any{"usr-1"})
	if len(items) != 1 {
		t.Fatalf("the notification survived? got %d rows", len(items))
	}
}

// TestRetentionSweepDeletesOnlyTerminalNotifications is the boundary worth
// pinning: a notification still in flight is work the worker has not finished,
// and deleting it would silently drop a page somebody is waiting for.
func TestRetentionSweepDeletesOnlyTerminalNotifications(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	e := New(st)
	e.deliveryCfg.Retention = RetentionPolicy{NotificationDays: 30}
	sink := newRecordingSink()
	e.SetMetricsSink(sink)

	old := utils.ToISO(utils.UTCNow().AddDate(0, 0, -40))
	recent := utils.ToISO(utils.UTCNow().AddDate(0, 0, -2))
	st.Seed("notifications",
		map[string]any{"id": "old-delivered", "user_id": "u", "status": "delivered", "updated_at": old},
		map[string]any{"id": "old-pending", "user_id": "u", "status": "delivery_scheduled", "updated_at": old},
		map[string]any{"id": "recent-delivered", "user_id": "u", "status": "delivered", "updated_at": recent},
	)

	deleted := e.RetentionSweep(ctx)
	if deleted[RetentionNotifications] != 1 {
		t.Fatalf("deleted %v, want exactly one notification", deleted)
	}

	items, _ := st.ListItemsIn(ctx, "notifications", "user_id", []any{"u"})
	survived := map[string]bool{}
	for _, item := range items {
		survived[utils.StrVal(item, "id")] = true
	}
	if survived["old-delivered"] {
		t.Error("a terminal notification past the horizon should have gone")
	}
	if !survived["old-pending"] {
		t.Error("an in-flight notification is work, not history — it must survive its age")
	}
	if !survived["recent-delivered"] {
		t.Error("a notification inside the horizon must survive")
	}

	// The counter is what makes a sweep that stopped running distinguishable
	// from one that found nothing.
	if sink.retention[RetentionNotifications] != 1 {
		t.Errorf("metric = %d, want 1", sink.retention[RetentionNotifications])
	}
}

// TestRetentionSweepReportsPerCategory checks that one category's outcome does
// not stand in for another's — the sweep is reported per category because that
// is the granularity a retention policy is written at.
func TestRetentionSweepReportsPerCategory(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	e := New(st)
	e.deliveryCfg.Retention = RetentionPolicy{NotificationDays: 30, DeliveryAttemptDays: 7}

	old := utils.ToISO(utils.UTCNow().AddDate(0, 0, -40))
	st.Seed("notifications", map[string]any{
		"id": "n1", "user_id": "u", "status": "failed", "updated_at": old,
	})
	st.Seed("notification_delivery_attempts",
		map[string]any{"id": "a1", "notification_id": "n1", "created_at": old},
		map[string]any{"id": "a2", "notification_id": "n1", "created_at": old},
	)

	deleted := e.RetentionSweep(ctx)
	if deleted[RetentionNotifications] != 1 {
		t.Errorf("notifications = %d, want 1", deleted[RetentionNotifications])
	}
	if deleted[RetentionDeliveryAttempts] != 2 {
		t.Errorf("delivery_attempts = %d, want 2", deleted[RetentionDeliveryAttempts])
	}
	if _, present := deleted[RetentionAudit]; present {
		t.Error("a category with no horizon must be absent, not zero — the two mean different things")
	}
}

// TestRetentionPolicyUnset covers what the readiness warning keys off.
func TestRetentionPolicyUnset(t *testing.T) {
	if !(RetentionPolicy{AlertGroupDays: 30, ChatopsMessageDays: 30}).Unset() {
		t.Error("the two legacy defaults are not a decision about personal data")
	}
	if (RetentionPolicy{AuditDays: 365}).Unset() {
		t.Error("one stated horizon is a decision")
	}
}
