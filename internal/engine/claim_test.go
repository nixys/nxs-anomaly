package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// TestClaimIsExclusive verifies a claimed notification leaves the deliverable
// set: a second claim returns nothing, so no other worker re-delivers it.
func TestClaimIsExclusive(t *testing.T) {
	ms := newMemStore()
	ms.seed("notifications", makeNotification("n1", model.NotificationDeliveryScheduled, "webhook", "http://x", map[string]any{}))
	now := utils.ToISO(utils.UTCNow())

	first, err := ms.ClaimDeliverableNotifications(context.Background(), "wkr-a", now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first claim got %d rows, want 1", len(first))
	}
	if got := ms.row("notifications", "n1")["status"]; got != model.NotificationDelivering {
		t.Errorf("status after claim = %v, want delivering", got)
	}

	second, err := ms.ClaimDeliverableNotifications(context.Background(), "wkr-b", now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second claim got %d rows, want 0 (already claimed)", len(second))
	}
}

// TestReclaimStaleClaimsResetsCrashedWorker verifies the reaper returns claims
// older than ClaimTimeout to their pending status, leaving fresh claims alone.
func TestReclaimStaleClaimsResetsCrashedWorker(t *testing.T) {
	ms := newMemStore()
	stale := "2020-01-01T00:00:00+00:00"
	fresh := utils.ToISO(utils.UTCNow())

	staleDeliver := makeNotification("d1", model.NotificationDelivering, "webhook", "http://x", map[string]any{})
	staleDeliver["claimed_at"] = stale
	staleRetry := makeNotification("r1", model.NotificationRetrying, "webhook", "http://x", map[string]any{})
	staleRetry["claimed_at"] = stale
	freshDeliver := makeNotification("d2", model.NotificationDelivering, "webhook", "http://x", map[string]any{})
	freshDeliver["claimed_at"] = fresh
	ms.seed("notifications", staleDeliver, staleRetry, freshDeliver)

	e := deliveryEngine(ms)
	e.deliveryCfg.ClaimTimeout = time.Minute

	n, err := e.ReclaimStaleNotificationClaims(context.Background())
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if n != 2 {
		t.Errorf("reclaimed %d, want 2", n)
	}
	if got := ms.row("notifications", "d1")["status"]; got != model.NotificationDeliveryScheduled {
		t.Errorf("d1 status = %v, want delivery_scheduled", got)
	}
	if got := ms.row("notifications", "r1")["status"]; got != model.NotificationRetryScheduled {
		t.Errorf("r1 status = %v, want retry_scheduled", got)
	}
	if got := ms.row("notifications", "d2")["status"]; got != model.NotificationDelivering {
		t.Errorf("d2 (fresh) status = %v, want still delivering", got)
	}
}

// TestReclaimDisabledWhenTimeoutZero verifies the reaper is a no-op when
// ClaimTimeout is unset (default engine build).
func TestReclaimDisabledWhenTimeoutZero(t *testing.T) {
	ms := newMemStore()
	old := makeNotification("d1", model.NotificationDelivering, "webhook", "http://x", map[string]any{})
	old["claimed_at"] = "2020-01-01T00:00:00+00:00"
	ms.seed("notifications", old)

	e := deliveryEngine(ms) // ClaimTimeout == 0
	n, err := e.ReclaimStaleNotificationClaims(context.Background())
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if n != 0 {
		t.Errorf("reclaimed %d, want 0 (disabled)", n)
	}
}
