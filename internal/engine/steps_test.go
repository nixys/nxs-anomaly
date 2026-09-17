package engine

import (
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// --- selectRoute tests ---

func TestSelectRouteLabelsMatch(t *testing.T) {
	integration := map[string]any{
		"routes": []any{
			map[string]any{"id": "default", "match_type": "all", "is_default": true, "labels": map[string]any{}, "pattern": ""},
			map[string]any{"id": "critical", "match_type": "labels", "is_default": false, "labels": map[string]any{"severity": "critical"}, "pattern": ""},
			map[string]any{"id": "database", "match_type": "regex", "is_default": false, "labels": map[string]any{}, "pattern": "database"},
		},
	}
	route, err := selectRoute(integration, map[string]any{"labels": map[string]any{"severity": "critical"}, "title": "CPU"})
	if err != nil || route["id"] != "critical" {
		t.Errorf("expected critical route, got %v err=%v", route["id"], err)
	}
}

func TestSelectRouteRegexMatch(t *testing.T) {
	integration := map[string]any{
		"routes": []any{
			map[string]any{"id": "default", "match_type": "all", "is_default": true, "labels": map[string]any{}, "pattern": ""},
			map[string]any{"id": "database", "match_type": "regex", "is_default": false, "labels": map[string]any{}, "pattern": "database"},
		},
	}
	route, err := selectRoute(integration, map[string]any{"labels": map[string]any{"severity": "warning"}, "title": "database lag"})
	if err != nil || route["id"] != "database" {
		t.Errorf("expected database route, got %v err=%v", route["id"], err)
	}
}

// The documented example: an anchored pattern on the alert name.
func TestSelectRouteRegexIsAnchoredOnTitleAndLabels(t *testing.T) {
	for _, tc := range []struct {
		name, pattern string
		payload       map[string]any
	}{
		{"title", "^Disk(Space|Inodes)", map[string]any{"title": "DiskSpaceLow on db-01"}},
		{"label value", "^Disk(Space|Inodes)", map[string]any{"title": "Inodes 95%", "labels": map[string]any{"alertname": "DiskInodesLow"}}},
		{"label name=value", "^kind=Disk$", map[string]any{"title": "x", "labels": map[string]any{"kind": "Disk"}}},
	} {
		integration := map[string]any{
			"routes": []any{
				map[string]any{"id": "default", "match_type": "all", "is_default": true},
				map[string]any{"id": "disk", "match_type": "regex", "pattern": tc.pattern},
			},
		}
		route, err := selectRoute(integration, tc.payload)
		if err != nil || route["id"] != "disk" {
			t.Errorf("%s: route = %v err=%v, want disk", tc.name, route["id"], err)
		}
	}
}

// Annotations, the message and field names are not what a route is about.
func TestSelectRouteRegexIgnoresTheRestOfThePayload(t *testing.T) {
	integration := map[string]any{
		"routes": []any{
			map[string]any{"id": "default", "match_type": "all", "is_default": true},
			map[string]any{"id": "prod", "match_type": "regex", "pattern": "prod"},
		},
	}
	route, err := selectRoute(integration, map[string]any{
		"title":       "cpu",
		"message":     "see production dashboard",
		"labels":      map[string]any{"env": "staging"},
		"annotations": map[string]any{"runbook": "how to reproduce"},
	})
	if err != nil || route["id"] != "default" {
		t.Errorf("route = %v err=%v, want default: nothing in the title or labels says prod", route["id"], err)
	}
}

func TestSelectRouteDefaultFallback(t *testing.T) {
	integration := map[string]any{
		"routes": []any{
			map[string]any{"id": "default", "match_type": "all", "is_default": true, "labels": map[string]any{}, "pattern": ""},
			map[string]any{"id": "critical", "match_type": "labels", "is_default": false, "labels": map[string]any{"severity": "critical"}, "pattern": ""},
		},
	}
	route, err := selectRoute(integration, map[string]any{"labels": map[string]any{"severity": "info"}, "title": "cache"})
	if err != nil || route["id"] != "default" {
		t.Errorf("expected default route, got %v err=%v", route["id"], err)
	}
}

// --- electDutyUsers tests ---

func TestElectDutyUsersPreferHighestPriority(t *testing.T) {
	e := newEngine()
	s := store.NewState()
	s.Users["u_low"] = map[string]any{"id": "u_low", "on_duty": true, "priority": "low"}
	s.Users["u_high"] = map[string]any{"id": "u_high", "on_duty": true, "priority": "high"}
	s.Users["u_medium"] = map[string]any{"id": "u_medium", "on_duty": true, "priority": "medium"}
	s.Users["u_off"] = map[string]any{"id": "u_off", "on_duty": false, "priority": "high"}
	s.Teams["team_ops"] = map[string]any{"id": "team_ops", "member_ids": []any{"u_low", "u_high", "u_medium", "u_off"}}

	step := map[string]any{"team_id": "team_ops", "fallback_to_all": false}
	result := e.electDutyUsers(s, step)
	if len(result) != 1 || result[0] != "u_high" {
		t.Errorf("expected [u_high], got %v", result)
	}
}

func TestElectDutyUsersFallbackToAll(t *testing.T) {
	e := newEngine()
	s := store.NewState()
	s.Users["u1"] = map[string]any{"id": "u1", "on_duty": false, "priority": "high"}
	s.Users["u2"] = map[string]any{"id": "u2", "on_duty": false, "priority": "medium"}
	s.Teams["team_ops"] = map[string]any{"id": "team_ops", "member_ids": []any{"u1", "u2"}}

	step := map[string]any{"team_id": "team_ops", "fallback_to_all": true}
	result := e.electDutyUsers(s, step)
	if len(result) != 2 {
		t.Errorf("expected 2 fallback users, got %v", result)
	}
}

func TestElectDutyUsersNoFallbackReturnsEmpty(t *testing.T) {
	e := newEngine()
	s := store.NewState()
	s.Users["u1"] = map[string]any{"id": "u1", "on_duty": false, "priority": "high"}
	s.Teams["team_ops"] = map[string]any{"id": "team_ops", "member_ids": []any{"u1"}}

	step := map[string]any{"team_id": "team_ops", "fallback_to_all": false}
	result := e.electDutyUsers(s, step)
	if len(result) != 0 {
		t.Errorf("expected empty, got %v", result)
	}
}

// --- advanceGroupLocked tests ---

func TestAdvanceGroupLockedNotifyThenWait(t *testing.T) {
	e := newEngine()
	s := newState([]any{
		map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{"u1"}},
		map[string]any{"kind": "WAIT", "delay_minutes": 5},
	})
	group := newGroup(0, 0)
	timestamp := "2026-05-10T10:00:00+00:00"

	g := model.WrapAlertGroup(group)
	e.advanceGroupLocked(s, g, timestamp)
	group = g.Raw()

	if group["current_step"] != 2 {
		t.Errorf("expected current_step=2, got %v", group["current_step"])
	}
	expectedAt := utils.ToISO(time.Date(2026, 5, 10, 10, 5, 0, 0, time.UTC))
	if group["next_run_at"] != expectedAt {
		t.Errorf("expected next_run_at=%q, got %q", expectedAt, group["next_run_at"])
	}
	if len(s.Notifications) != 1 {
		t.Errorf("expected 1 notification, got %d", len(s.Notifications))
	}
	for _, rec := range s.Notifications {
		n := notificationMap(rec)
		if n["user_id"] != "u1" {
			t.Errorf("expected user_id=u1, got %v", n["user_id"])
		}
	}
	if !logsContainType(group, "notified") {
		t.Error("expected 'notified' log entry")
	}
}

func TestAdvanceGroupLockedRepeatReschedules(t *testing.T) {
	e := newEngine()
	s := newState([]any{
		map[string]any{"kind": "REPEAT", "from_position": 0, "max_repeat_count": 2, "cooldown_minutes": 10},
	})
	group := newGroup(0, 0)
	timestamp := "2026-05-10T10:00:00+00:00"

	g := model.WrapAlertGroup(group)
	e.advanceGroupLocked(s, g, timestamp)
	group = g.Raw()

	if group["repeat_count"] != 1 {
		t.Errorf("expected repeat_count=1, got %v", group["repeat_count"])
	}
	if group["current_step"] != 0 {
		t.Errorf("expected current_step=0, got %v", group["current_step"])
	}
	expectedAt := utils.ToISO(time.Date(2026, 5, 10, 10, 10, 0, 0, time.UTC))
	if group["next_run_at"] != expectedAt {
		t.Errorf("expected next_run_at=%q, got %q", expectedAt, group["next_run_at"])
	}
	if !logsContainType(group, "escalation_repeat") {
		t.Error("expected 'escalation_repeat' log entry")
	}
}

func TestAdvanceGroupLockedRepeatExhausted(t *testing.T) {
	e := newEngine()
	s := newState([]any{
		map[string]any{"kind": "REPEAT", "from_position": 0, "max_repeat_count": 1, "cooldown_minutes": 0},
	})
	group := newGroup(0, 1)

	g := model.WrapAlertGroup(group)
	e.advanceGroupLocked(s, g, "2026-05-10T10:00:00+00:00")
	group = g.Raw()

	if group["next_run_at"] != nil {
		t.Errorf("expected next_run_at=nil, got %v", group["next_run_at"])
	}
	if !logsContainType(group, "escalation_repeat_exhausted") {
		t.Error("expected 'escalation_repeat_exhausted' log entry")
	}
}

func TestAdvanceGroupLockedResolveStep(t *testing.T) {
	e := newEngine()
	s := newState([]any{
		map[string]any{"kind": "RESOLVE"},
	})
	group := newGroup(0, 0)

	g := model.WrapAlertGroup(group)
	e.advanceGroupLocked(s, g, "2026-05-10T10:00:00+00:00")
	group = g.Raw()

	if group["status"] != "resolved" {
		t.Errorf("expected status=resolved, got %v", group["status"])
	}
	if group["resolved_at"] == nil {
		t.Error("expected resolved_at to be set")
	}
}
