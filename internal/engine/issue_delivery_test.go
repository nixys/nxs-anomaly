package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
)

// TestIssueChannelDeliversViaTracker verifies CREATE_ISSUE work, now scheduled as
// an "issue" notification, performs its tracker HTTP call in the unlocked delivery
// stage (claim pipeline) rather than inside the escalation lock.
func TestIssueChannelDeliversViaTracker(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issues.json" {
			atomic.AddInt32(&hits, 1)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	ms := newMemStore()
	n := makeNotification("i1", model.NotificationDeliveryScheduled, "issue", srv.URL, map[string]any{
		"url":          srv.URL,
		"tracker_type": "redmine",
		"project":      "ops",
		"subject":      "[critical] CPU",
		"body":         "details",
	})
	ms.seed("notifications", n)
	e := deliveryEngine(ms)

	if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if got := ms.row("notifications", "i1")["status"]; got != model.NotificationDelivered {
		t.Errorf("status = %v, want delivered", got)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("tracker POST /issues.json hits = %d, want 1", n)
	}
}

// TestIssueChannelFailureSchedulesRetry verifies a failing tracker call flows
// through the standard retry path (so issue creation gets retries for free).
func TestIssueChannelFailureSchedulesRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ms := newMemStore()
	ms.seed("notifications", makeNotification("i1", model.NotificationDeliveryScheduled, "issue", srv.URL, map[string]any{
		"url":          srv.URL,
		"tracker_type": "redmine",
	}))
	e := deliveryEngine(ms) // MaxRetries 3

	if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if got := ms.row("notifications", "i1")["status"]; got != model.NotificationRetryScheduled {
		t.Errorf("status = %v, want retry_scheduled", got)
	}
}
