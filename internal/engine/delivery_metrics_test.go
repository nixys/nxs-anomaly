package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// recordingSink captures MetricsSink calls for assertions. Concurrency-safe
// because ObserveDeliveryLatency fires from the bounded delivery goroutines.
type recordingSink struct {
	mu             sync.Mutex
	latency        map[string]int
	firstAttempt   map[string][]float64
	deadLetter     map[string]int
	shortCircuited map[string]int
	panics         map[string]int
	skipped        map[string]int
	dutyMissing    int
	dutyOnCall     int
	sourcesSilent  int
	retention      map[string]int
}

func newRecordingSink() *recordingSink {
	return &recordingSink{
		latency:        map[string]int{},
		firstAttempt:   map[string][]float64{},
		deadLetter:     map[string]int{},
		shortCircuited: map[string]int{},
		panics:         map[string]int{},
		skipped:        map[string]int{},
		retention:      map[string]int{},
	}
}

func (s *recordingSink) ObserveDeliveryLatency(channel string, _ float64) {
	s.mu.Lock()
	s.latency[channel]++
	s.mu.Unlock()
}
func (s *recordingSink) ObserveFirstAttemptDelay(channel string, seconds float64) {
	s.mu.Lock()
	s.firstAttempt[channel] = append(s.firstAttempt[channel], seconds)
	s.mu.Unlock()
}
func (s *recordingSink) IncDeadLetter(channel string) {
	s.mu.Lock()
	s.deadLetter[channel]++
	s.mu.Unlock()
}
func (s *recordingSink) IncDeliveryShortCircuited(channel string) {
	s.mu.Lock()
	s.shortCircuited[channel]++
	s.mu.Unlock()
}
func (s *recordingSink) IncShiftNotifications(int) {}
func (s *recordingSink) IncRetentionDeleted(category string, n int) {
	s.mu.Lock()
	s.retention[category] += n
	s.mu.Unlock()
}

// IncNotificationSkipped records skips by "channel:reason" so tests can assert
// that a channel with no transport is counted, not silently swallowed.
func (s *recordingSink) IncNotificationSkipped(channel, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped[channel+":"+reason]++
}
func (s *recordingSink) SetScheduleCoverage(int, int) {}

func (s *recordingSink) IncWorkerPanic(stage string) {
	s.mu.Lock()
	s.panics[stage]++
	s.mu.Unlock()
}

func (s *recordingSink) SetSourcesSilent(n int) {
	s.mu.Lock()
	s.sourcesSilent = n
	s.mu.Unlock()
}

func (s *recordingSink) SetDutyAttendance(missing, onCall int) {
	s.mu.Lock()
	s.dutyMissing, s.dutyOnCall = missing, onCall
	s.mu.Unlock()
}

func (s *recordingSink) get(m map[string]int, k string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return m[k]
}

func TestDeliveryObservesLatencyAndDeadLetter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ms := newMemStore()
	ms.seed("notifications", makeNotification("n1", model.NotificationDeliveryScheduled, "webhook", srv.URL, map[string]any{}))
	e := deliveryEngine(ms)
	e.deliveryCfg.MaxRetries = 1 // first failure is terminal → dead-letter
	sink := newRecordingSink()
	e.SetMetricsSink(sink)

	if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if got := sink.get(sink.latency, "webhook"); got != 1 {
		t.Errorf("latency observations for webhook = %d, want 1", got)
	}
	if got := sink.get(sink.deadLetter, "webhook"); got != 1 {
		t.Errorf("dead-letter count for webhook = %d, want 1", got)
	}
}

func TestDeliveryShortCircuitedWhenBreakerOpen(t *testing.T) {
	ms := newMemStore()
	// "log" channel would otherwise always deliver successfully.
	ms.seed("notifications", makeNotification("n1", model.NotificationDeliveryScheduled, "log", "", nil))
	e := deliveryEngine(ms)
	sink := newRecordingSink()
	e.SetMetricsSink(sink)
	// Open the breaker for this channel:target ("log:") before the cycle.
	e.breaker = newCircuitBreaker(1, time.Minute)
	e.breaker.Record("log:", false, time.Now())

	if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	// Skipped without state change: still delivery_scheduled, no attempt, no latency.
	if got := ms.row("notifications", "n1")["status"]; got != model.NotificationDeliveryScheduled {
		t.Errorf("status = %v, want delivery_scheduled (skipped)", got)
	}
	if n := ms.count("notification_delivery_attempts"); n != 0 {
		t.Errorf("delivery attempts = %d, want 0", n)
	}
	if got := sink.get(sink.shortCircuited, "log"); got != 1 {
		t.Errorf("short-circuit count for log = %d, want 1", got)
	}
	if got := sink.get(sink.latency, "log"); got != 0 {
		t.Errorf("latency must not be observed when short-circuited, got %d", got)
	}
}

// The paging-latency SLI: a first attempt reports how long the notification was
// queued; a retry does not (its wait is the backoff, not a delay to page).
func TestDeliveryObservesFirstAttemptDelay(t *testing.T) {
	ms := newMemStore()
	first := makeNotification("n1", model.NotificationDeliveryScheduled, "log", "", nil)
	first["created_at"] = utils.ToISO(utils.UTCNow().Add(-3 * time.Second))
	ms.seed("notifications", first)
	e := deliveryEngine(ms)
	sink := newRecordingSink()
	e.SetMetricsSink(sink)

	if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	sink.mu.Lock()
	got := append([]float64(nil), sink.firstAttempt["log"]...)
	sink.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("first-attempt observations = %v, want exactly one", got)
	}
	if got[0] < 2.5 || got[0] > 60 {
		t.Errorf("first-attempt delay = %.2fs, want ≈3s", got[0])
	}

	retry := makeNotification("n2", model.NotificationDeliveryScheduled, "log", "", nil)
	retry["retry_count"] = 2
	ms2 := newMemStore()
	ms2.seed("notifications", retry)
	e2 := deliveryEngine(ms2)
	sink2 := newRecordingSink()
	e2.SetMetricsSink(sink2)
	if _, err := e2.ProcessNotificationDeliveries(context.Background()); err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if n := len(sink2.firstAttempt["log"]); n != 0 {
		t.Errorf("a retry must not be observed as a first attempt, got %d", n)
	}
}
