package engine

import (
	"testing"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// --- test helpers ---

func newEngine() *Engine {
	return &Engine{}
}

func newState(chainSteps []any) *store.State {
	s := store.NewState()
	s.EscalationChains["chain1"] = map[string]any{
		"id":    "chain1",
		"name":  "test chain",
		"steps": chainSteps,
	}
	s.Integrations["int1"] = map[string]any{
		"id":   "int1",
		"name": "test integration",
		"notification_policy": map[string]any{
			"channels":               []any{},
			"batch_timeout_seconds":  0,
			"batch_deadline_seconds": 0,
			"emergency_user_id":      nil,
			"epic_user_id":           nil,
			"epic_threshold_count":   0,
			"epic_threshold_seconds": 0,
		},
	}
	s.Users["u1"] = map[string]any{
		"id":                   "u1",
		"username":             "user1",
		"name":                 "User One",
		"notification_targets": []any{map[string]any{"type": "log", "target": ""}},
	}
	s.Teams["team1"] = map[string]any{
		"id":         "team1",
		"name":       "Team One",
		"member_ids": []any{"u1"},
	}
	return s
}

func newGroup(current int, repeatCount int) map[string]any {
	return map[string]any{
		"id":                    "grp1",
		"status":                "open",
		"escalation_chain_id":   "chain1",
		"integration_id":        "int1",
		"title":                 "Test Alert",
		"severity":              "high",
		"current_step":          current,
		"repeat_count":          repeatCount,
		"alert_count":           1,
		"next_run_at":           nil,
		"logs":                  []any{},
		"acknowledged_at":       nil,
		"resolved_at":           nil,
		"epic_sent_at":          "",
		"notification_channels": []any{},
		"labels":                map[string]any{},
		"created_at":            "2026-05-10T10:00:00+00:00",
		"updated_at":            "2026-05-10T10:00:00+00:00",
	}
}

func logsContainType(group map[string]any, eventType string) bool {
	logs, _ := group["logs"].([]any)
	for _, l := range logs {
		if entry, ok := l.(map[string]any); ok {
			if entry["type"] == eventType {
				return true
			}
		}
	}
	return false
}

// --- shiftActive tests ---

func TestShiftActiveNoneRecurrence(t *testing.T) {
	shift := map[string]any{
		"start_at":   "2026-05-10T09:00:00+00:00",
		"end_at":     "2026-05-10T17:00:00+00:00",
		"recurrence": "none",
	}
	at12, _ := utils.ParseDatetime("2026-05-10T12:00:00+00:00")
	at18, _ := utils.ParseDatetime("2026-05-10T18:00:00+00:00")
	if !shiftActive(shift, at12) {
		t.Error("expected shift active at 12:00")
	}
	if shiftActive(shift, at18) {
		t.Error("expected shift inactive at 18:00")
	}
}

func TestShiftActiveDailyRecurrence(t *testing.T) {
	shift := map[string]any{
		"start_at":   "2026-05-10T09:00:00+00:00",
		"end_at":     "2026-05-10T17:00:00+00:00",
		"recurrence": "daily",
	}
	at10, _ := utils.ParseDatetime("2026-05-12T10:00:00+00:00")
	at20, _ := utils.ParseDatetime("2026-05-12T20:00:00+00:00")
	if !shiftActive(shift, at10) {
		t.Error("expected daily shift active at 10:00 on day+2")
	}
	if shiftActive(shift, at20) {
		t.Error("expected daily shift inactive at 20:00")
	}
}

func TestShiftActiveWeeklyRecurrence(t *testing.T) {
	shift := map[string]any{
		"start_at":   "2026-05-10T09:00:00+00:00",
		"end_at":     "2026-05-10T17:00:00+00:00",
		"recurrence": "weekly",
	}
	// May 10 is Sunday; May 17 is also Sunday (7 days later)
	at10w1, _ := utils.ParseDatetime("2026-05-17T10:00:00+00:00")
	at10w1mon, _ := utils.ParseDatetime("2026-05-18T10:00:00+00:00")
	if !shiftActive(shift, at10w1) {
		t.Error("expected weekly shift active 7 days later at same time")
	}
	if shiftActive(shift, at10w1mon) {
		t.Error("expected weekly shift inactive on different weekday")
	}
}

// --- buildDedupeKey tests ---

func TestBuildDedupeKeyUsesGroupByLabels(t *testing.T) {
	integration := map[string]any{"group_by": []any{"alertname", "cluster"}}
	labels := map[string]string{"alertname": "CPUHigh", "cluster": "prod", "pod": "api-1"}
	got := buildDedupeKey(integration, labels, "CPU high")
	want := "alertname=CPUHigh|cluster=prod"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestBuildDedupeKeyFallsBackToTitle(t *testing.T) {
	integration := map[string]any{"group_by": []any{"missing"}}
	labels := map[string]string{"alertname": "CPUHigh"}
	got := buildDedupeKey(integration, labels, "CPU high")
	want := "title=CPU high"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
