package engine

import (
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func policyUser(id string, policies map[string]any) map[string]any {
	return map[string]any{
		"id": id, "username": id, "email": id + "@x", "telegram_id": "tg-" + id, "phone": "+1" + id,
		"notification_policies": policies,
	}
}

func newPolicyGroup(ts string) model.AlertGroup {
	return model.NewAlertGroup(model.NewAlertGroupParams{
		IntegrationID: "i1", Title: "boom", Severity: "critical", Timestamp: ts,
	})
}

func countByChannel(state *store.State) map[string]int {
	out := map[string]int{}
	for _, rec := range state.Notifications {
		n, ok := rec.(model.Notification)
		if !ok {
			continue
		}
		out[n.Channel()]++
	}
	return out
}

func TestUserPolicyStepsParsing(t *testing.T) {
	u := policyUser("u1", map[string]any{
		"default": []any{
			map[string]any{"channel": "telegram", "wait_minutes": 5},
			map[string]any{"channel": "bogus", "wait_minutes": 1}, // dropped
			map[string]any{"channel": "call"},
		},
		"important": []any{},
	})
	steps := userPolicySteps(u, "default")
	if len(steps) != 2 || steps[0].Channel != "telegram" || steps[0].WaitMinutes != 5 || steps[1].Channel != "call" {
		t.Fatalf("default steps = %+v", steps)
	}
	// Empty important falls back to default.
	if imp := userPolicySteps(u, "important"); len(imp) != 2 {
		t.Fatalf("important should fall back to default, got %+v", imp)
	}
	// No policies at all → nil (legacy path).
	if s := userPolicySteps(map[string]any{"id": "x"}, "default"); s != nil {
		t.Fatalf("expected nil for user without policies, got %+v", s)
	}
}

func TestStartPolicyRunSchedulesFallback(t *testing.T) {
	e := newEngine()
	state := store.NewState()
	ts := utils.ToISO(utils.UTCNow())
	g := newPolicyGroup(ts)
	state.AlertGroups[g.ID()] = g
	u := policyUser("u1", map[string]any{"default": []any{
		map[string]any{"channel": "telegram", "wait_minutes": 5},
		map[string]any{"channel": "call", "wait_minutes": 0},
	}})
	state.Users["u1"] = u

	if started := e.startPolicyRun(state, g, u, "default", "reason", ts, nil); !started {
		t.Fatal("startPolicyRun should return true for a user with a policy")
	}
	// Step 0 (telegram) fired immediately; call is scheduled, not yet sent.
	if c := countByChannel(state); c["telegram"] != 1 || c["call"] != 0 {
		t.Fatalf("after start: %v (want telegram=1, call=0)", c)
	}
	if len(state.NotificationPolicyRuns) != 1 {
		t.Fatalf("expected 1 run, got %d", len(state.NotificationPolicyRuns))
	}
	var run map[string]any
	for _, r := range state.NotificationPolicyRuns {
		run = r
	}
	if utils.StrVal(run, "status") != "active" || utils.IntVal(run, "step_index") != 1 {
		t.Fatalf("run should be active at step 1, got %+v", run)
	}
	nextAt, err := utils.ParseDatetime(utils.StrVal(run, "next_step_at"))
	if err != nil {
		t.Fatalf("next_step_at: %v", err)
	}
	if d := time.Until(nextAt); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("next step should be ~5min out, got %v", d)
	}

	// Idempotent within the same escalation firing: a second start does not add a run.
	e.startPolicyRun(state, g, u, "default", "reason", ts, nil)
	if len(state.NotificationPolicyRuns) != 1 {
		t.Fatalf("second start must not create a duplicate run, got %d", len(state.NotificationPolicyRuns))
	}

	// Advance the run to its next step: call fires and the run completes.
	steps := userPolicySteps(u, "default")
	e.runPolicySteps(state, g, u, run, steps, "fallback", ts, nil)
	if c := countByChannel(state); c["call"] != 1 {
		t.Fatalf("after advance: %v (want call=1)", c)
	}
	if utils.StrVal(run, "status") != "done" {
		t.Fatalf("run should be done after the last step, got %v", utils.StrVal(run, "status"))
	}
}

func TestRunPolicyStepsZeroWaitFiresAllAtOnce(t *testing.T) {
	e := newEngine()
	state := store.NewState()
	ts := utils.ToISO(utils.UTCNow())
	g := newPolicyGroup(ts)
	state.AlertGroups[g.ID()] = g
	u := policyUser("u1", map[string]any{"default": []any{
		map[string]any{"channel": "telegram", "wait_minutes": 0},
		map[string]any{"channel": "email", "wait_minutes": 0},
	}})
	e.startPolicyRun(state, g, u, "default", "reason", ts, nil)
	c := countByChannel(state)
	if c["telegram"] != 1 || c["email"] != 1 {
		t.Fatalf("zero-wait policy should fire both channels at once: %v", c)
	}
	for _, r := range state.NotificationPolicyRuns {
		if utils.StrVal(r, "status") != "done" {
			t.Fatalf("zero-wait run should complete immediately, got %v", utils.StrVal(r, "status"))
		}
	}
}

func TestResolveChannelTarget(t *testing.T) {
	u := map[string]any{"email": "e@x", "telegram_id": "tg", "phone": "+1"}
	cases := []struct {
		channel, explicit, want string
		ok                      bool
	}{
		{"email", "", "e@x", true},
		{"telegram", "", "tg", true},
		{"call", "", "+1", true},
		{"log", "", "", true},
		{"webhook", "", "", false}, // needs explicit URL
		{"webhook", "https://h/x", "https://h/x", true},
		{"telegram", "override", "override", true}, // explicit wins
	}
	for _, c := range cases {
		got, ok := resolveChannelTarget(u, c.channel, c.explicit)
		if got != c.want || ok != c.ok {
			t.Errorf("resolveChannelTarget(%s,%q) = (%q,%v), want (%q,%v)", c.channel, c.explicit, got, ok, c.want, c.ok)
		}
	}
}

func TestSanitizeNotificationPolicies(t *testing.T) {
	// nil / empty → nil (legacy path).
	if v, err := sanitizeNotificationPolicies(nil, ChannelPolicy{}); err != nil || v != nil {
		t.Fatalf("nil should sanitize to nil, got %v err=%v", v, err)
	}
	// Bad channel rejected.
	if _, err := sanitizeNotificationPolicies(map[string]any{
		"default": []any{map[string]any{"channel": "sms"}},
	}, ChannelPolicy{}); err == nil {
		t.Fatal("unsupported channel must be rejected")
	}
	// Valid policy normalized.
	v, err := sanitizeNotificationPolicies(map[string]any{
		"default": []any{map[string]any{"channel": "TELEGRAM", "wait_minutes": 3, "target": " x "}},
	}, ChannelPolicy{})
	if err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	steps, _ := v["default"].([]any)
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %v", v)
	}
	s0 := steps[0].(map[string]any)
	if s0["channel"] != "telegram" || s0["wait_minutes"] != 3 || s0["target"] != "x" {
		t.Fatalf("step not normalized: %v", s0)
	}
}
