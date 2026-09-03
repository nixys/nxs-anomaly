package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

func TestDefaultWorkerStallTimeout(t *testing.T) {
	// Floored at 30s so a sub-second poll interval is not a hair-trigger.
	if got := defaultWorkerStallTimeout(time.Second); got != 30*time.Second {
		t.Errorf("1s poll: got %v, want 30s", got)
	}
	// Derived as 4× the poll interval once that clears the floor.
	if got := defaultWorkerStallTimeout(10 * time.Second); got != 40*time.Second {
		t.Errorf("10s poll: got %v, want 40s", got)
	}
}

func TestCycleStalled(t *testing.T) {
	newWR := func(poll time.Duration) *workerRuntime {
		return &workerRuntime{metrics: newMetrics(), cfg: Config{PollInterval: poll}, startTime: time.Now()}
	}

	t.Run("startup grace, no cycle yet", func(t *testing.T) {
		wr := newWR(5 * time.Second) // window 30s
		if stalled, _ := wr.cycleStalled(); stalled {
			t.Fatal("a just-started worker must not be reported stalled")
		}
	})

	t.Run("startup grace exceeded, still no cycle", func(t *testing.T) {
		wr := newWR(5 * time.Second)
		wr.startTime = time.Now().Add(-31 * time.Second)
		if stalled, _ := wr.cycleStalled(); !stalled {
			t.Fatal("no cycle completed within the window must be stalled")
		}
	})

	t.Run("recent cycle is healthy", func(t *testing.T) {
		wr := newWR(5 * time.Second)
		wr.metrics.cyclesCompleted = 3
		wr.metrics.lastCycleAt.Store(utils.ToISO(time.Now().UTC()))
		if stalled, _ := wr.cycleStalled(); stalled {
			t.Fatal("a worker that just completed a cycle is not stalled")
		}
	})

	t.Run("old cycle is stalled", func(t *testing.T) {
		wr := newWR(5 * time.Second)
		wr.metrics.cyclesCompleted = 3
		wr.metrics.lastCycleAt.Store(utils.ToISO(time.Now().UTC().Add(-31 * time.Second)))
		stalled, since := wr.cycleStalled()
		if !stalled {
			t.Fatal("a worker whose last cycle is older than the window is stalled")
		}
		if since < 30*time.Second {
			t.Errorf("since = %v, want >= 30s", since)
		}
	})
}

// TestApplyCycleResultProviderCounters verifies delivered/failed notifications
// are counted per provider so the exposition carries both the success and the
// error series that the per-provider SLI is built from.
func TestApplyCycleResultProviderCounters(t *testing.T) {
	m := newMetrics()
	result := map[string]any{
		"delivered_notification_items": []map[string]any{
			{"channel": "webhook", "status": "delivered"},
			{"channel": "webhook", "status": "delivered"},
			{"channel": "telegram", "status": "failed"},
		},
		"retried_notification_items": []map[string]any{
			{"channel": "email", "status": "delivered"},
		},
	}
	applyCycleResult(m, result)

	body := scrapeMetrics(t, m)
	for _, want := range []string{
		`nxs_anomaly_notifications_delivered_by_provider_total{provider="webhook"} 2`,
		`nxs_anomaly_notifications_delivered_by_provider_total{provider="email"} 1`,
		`nxs_anomaly_delivery_errors_by_provider_total{provider="telegram"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics exposition missing %q", want)
		}
	}
}

func scrapeMetrics(t *testing.T, m *Metrics) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", rec.Code)
	}
	return rec.Body.String()
}
