package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Silence from a source looks exactly like health, and the longer it lasts the
// more reassuring it gets. These tests pin the one signal that reads it as news.

// heartbeatStore seeds an integration with a heartbeat and, optionally, an alert
// received at some point in the past.
func heartbeatStore(t *testing.T, intervalSeconds int, lastSeen time.Duration) *memStore {
	t.Helper()
	ms := newMemStore()
	ms.seed("escalation_chains", map[string]any{
		"id": "chain-1", "name": "chain",
		"steps": []any{map[string]any{"kind": StepWait, "delay_minutes": 0}},
	})
	ms.seed("integrations", map[string]any{
		"id": "int-1", "name": "prod exporter", "key": "key-1", "routing_key": "key-1",
		"group_by": []any{"alertname"},
		"routes": []any{map[string]any{
			"id": "r1", "name": "default", "match_type": "all",
			"is_default": true, "escalation_chain_id": "chain-1",
		}},
		"heartbeat": sanitizeHeartbeat(map[string]any{"interval_seconds": intervalSeconds}),
	})
	if lastSeen >= 0 {
		ms.seed("alerts", map[string]any{
			"id": "alt-1", "integration_id": "int-1",
			"received_at": utils.ToISO(utils.UTCNow().Add(-lastSeen)),
		})
	}
	return ms
}

// silenceAlerts returns the alert groups this feature raised.
func silenceAlerts(ms *memStore) []map[string]any {
	var out []map[string]any
	for _, row := range ms.data["alert_groups"] {
		labels, _ := row["labels"].(map[string]any)
		if utils.StrVal(labels, "alertname") == heartbeatAlertName {
			out = append(out, row)
		}
	}
	return out
}

func TestSilentSourceRaisesAnAlert(t *testing.T) {
	ms := heartbeatStore(t, 60, 10*time.Minute)
	e := crudEngine(ms)

	silent, err := e.ProcessSourceHeartbeats(context.Background())
	if err != nil {
		t.Fatalf("ProcessSourceHeartbeats: %v", err)
	}
	if silent != 1 {
		t.Fatalf("reported %d silent source(s), want 1", silent)
	}
	raised := silenceAlerts(ms)
	if len(raised) != 1 {
		t.Fatalf("raised %d alert group(s), want 1", len(raised))
	}
	if got := utils.StrVal(raised[0], "status"); got != "open" {
		t.Errorf("group status = %q, want open", got)
	}
	// Raised through the ordinary ingest path, so it carries the integration
	// that owns it and can be routed and escalated like any other alert.
	if got := utils.StrVal(raised[0], "integration_id"); got != "int-1" {
		t.Errorf("integration_id = %q, want the silent integration", got)
	}
}

// A source inside its interval is not news, however loudly the check runs.
func TestTalkingSourceRaisesNothing(t *testing.T) {
	ms := heartbeatStore(t, 600, time.Minute)
	e := crudEngine(ms)

	silent, err := e.ProcessSourceHeartbeats(context.Background())
	if err != nil {
		t.Fatalf("ProcessSourceHeartbeats: %v", err)
	}
	if silent != 0 || len(silenceAlerts(ms)) != 0 {
		t.Errorf("silent=%d raised=%d, want nothing for a source inside its interval",
			silent, len(silenceAlerts(ms)))
	}
}

// Grace is what buys a source a late tick. Without a test in the band between
// the interval and the interval plus grace, the whole allowance could be dropped
// and every check would still agree — which is exactly what a mutation of this
// line showed before this test existed.
func TestGraceCoversALateTick(t *testing.T) {
	// interval 60 → grace defaults to 20, so the source is overdue only past 80s.
	t.Run("inside the grace band", func(t *testing.T) {
		ms := heartbeatStore(t, 60, 70*time.Second)
		e := crudEngine(ms)

		silent, _ := e.ProcessSourceHeartbeats(context.Background())
		if silent != 0 || len(silenceAlerts(ms)) != 0 {
			t.Errorf("silent=%d raised=%d at 70s with a 60s interval and 20s grace; a late tick is not an outage",
				silent, len(silenceAlerts(ms)))
		}
	})
	t.Run("past the grace band", func(t *testing.T) {
		ms := heartbeatStore(t, 60, 90*time.Second)
		e := crudEngine(ms)

		silent, _ := e.ProcessSourceHeartbeats(context.Background())
		if silent != 1 || len(silenceAlerts(ms)) != 1 {
			t.Errorf("silent=%d raised=%d at 90s; past interval+grace it is news",
				silent, len(silenceAlerts(ms)))
		}
	})
}

// Opt-in per integration: plenty of sources are legitimately quiet for weeks,
// and reporting them would train everyone to ignore this alert.
func TestSourceWithoutAHeartbeatIsNeverReported(t *testing.T) {
	ms := heartbeatStore(t, 0, 30*24*time.Hour)
	e := crudEngine(ms)

	silent, _ := e.ProcessSourceHeartbeats(context.Background())
	if silent != 0 || len(silenceAlerts(ms)) != 0 {
		t.Errorf("silent=%d raised=%d, want nothing without a declared interval",
			silent, len(silenceAlerts(ms)))
	}
}

// A source wired up minutes ago and not yet firing has nothing to compare
// against; calling that "silent" would page somebody about a working setup.
func TestSourceThatNeverReportedIsNotSilent(t *testing.T) {
	ms := heartbeatStore(t, 60, -1)
	e := crudEngine(ms)

	silent, _ := e.ProcessSourceHeartbeats(context.Background())
	if silent != 0 || len(silenceAlerts(ms)) != 0 {
		t.Errorf("silent=%d raised=%d, want nothing from a source that never reported",
			silent, len(silenceAlerts(ms)))
	}
}

// The worker runs every few seconds. Raising on every pass would keep the group
// open with a climbing alert_count — a storm attributed to the source that is,
// by definition, sending nothing.
func TestSilenceIsReportedOnceNotEveryCycle(t *testing.T) {
	ms := heartbeatStore(t, 60, 10*time.Minute)
	e := crudEngine(ms)

	for i := 0; i < 3; i++ {
		if _, err := e.ProcessSourceHeartbeats(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	if got := len(silenceAlerts(ms)); got != 1 {
		t.Fatalf("three cycles raised %d group(s), want 1", got)
	}
	for _, g := range silenceAlerts(ms) {
		if count := utils.IntVal(g, "alert_count"); count != 1 {
			t.Errorf("alert_count = %d after three cycles, want 1", count)
		}
	}
}

// Coming back is the other half of a dead-man switch: an alert that only ever
// fires has to be closed by hand, and will be.
func TestSourceComingBackResolvesTheAlert(t *testing.T) {
	ms := heartbeatStore(t, 60, 10*time.Minute)
	e := crudEngine(ms)

	if _, err := e.ProcessSourceHeartbeats(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if len(silenceAlerts(ms)) != 1 {
		t.Fatal("no alert to resolve")
	}

	// The source reports again.
	ms.seed("alerts", map[string]any{
		"id": "alt-2", "integration_id": "int-1", "received_at": utils.ToISO(utils.UTCNow()),
	})
	silent, err := e.ProcessSourceHeartbeats(context.Background())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}

	if silent != 0 {
		t.Errorf("still reporting %d silent source(s) after it came back", silent)
	}
	for _, g := range silenceAlerts(ms) {
		if got := utils.StrVal(g, "status"); got != "resolved" {
			t.Errorf("group status = %q, want resolved", got)
		}
	}
}

func TestSanitizeHeartbeat(t *testing.T) {
	off := sanitizeHeartbeat(nil)
	if off["interval_seconds"] != 0 {
		t.Errorf("absent config = %v, want the check off", off)
	}

	// Below a minute the check would fire on ordinary scheduling jitter: the
	// worker cycle is five seconds and a late tick is not an outage.
	floored := sanitizeHeartbeat(map[string]any{"interval_seconds": 5})
	if floored["interval_seconds"] != int(heartbeatMinInterval/time.Second) {
		t.Errorf("interval = %v, want it floored to %s", floored["interval_seconds"], heartbeatMinInterval)
	}

	// Grace defaults to a third: enough to absorb jitter, short enough that the
	// report still means something.
	defaulted := sanitizeHeartbeat(map[string]any{"interval_seconds": 300})
	if defaulted["grace_seconds"] != 100 {
		t.Errorf("grace = %v, want a third of the interval", defaulted["grace_seconds"])
	}

	explicit := sanitizeHeartbeat(map[string]any{"interval_seconds": 300, "grace_seconds": 30})
	if explicit["grace_seconds"] != 30 {
		t.Errorf("grace = %v, want the value given", explicit["grace_seconds"])
	}
}
