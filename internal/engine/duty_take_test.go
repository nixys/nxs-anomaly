package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Taking over in an incident writes a schedule override, never the check-in
// flag. The schedule engine does not read that flag, so switching duty with it
// would leave NOTIFY_SCHEDULE, the coverage report and the preview all still
// naming the person being replaced — the two answers would disagree exactly
// when it matters. These tests pin that, and the boundaries around it.

func takeoverStore(t *testing.T, schedules ...map[string]any) *memStore {
	t.Helper()
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chn-1", "platform": "telegram", "external_id": "-100500", "commands_enabled": true,
	})
	ms.seed("users", map[string]any{
		"id": "usr-alice", "username": "alice", "role": "responder",
	})
	for _, s := range schedules {
		ms.seed("schedules", s)
	}
	return ms
}

func alice(role authz.Role, teams ...string) context.Context {
	actor := authz.Actor{
		ID: "usr-alice", Kind: authz.KindUser, DisplayName: "alice", Role: role,
	}
	if len(teams) > 0 {
		actor.TeamIDs, actor.TeamScoped = teams, true
	}
	return authz.NewContext(context.Background(), actor)
}

func runCommand(t *testing.T, e *Engine, ctx context.Context, command string) (map[string]any, error) {
	t.Helper()
	return e.PostChatopsCommand(ctx, map[string]any{
		"channel_id": "chn-1", "command": command, "actor": "alice",
	})
}

func overridesOf(t *testing.T, ms *memStore, schedID string) []any {
	t.Helper()
	sched, ok := ms.data["schedules"][schedID]
	if !ok {
		t.Fatalf("schedule %s missing", schedID)
	}
	list, _ := sched["overrides"].([]any)
	return list
}

func TestDutyTakeWritesAnOverride(t *testing.T) {
	ms := takeoverStore(t, dailyRotation("usr-bob"))
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "duty take"); err != nil {
		t.Fatalf("duty take: %v", err)
	}

	list := overridesOf(t, ms, "sch-1")
	if len(list) != 1 {
		t.Fatalf("got %d override(s), want 1", len(list))
	}
	override := list[0].(map[string]any)
	if utils.StrVal(override, "user_id") != "usr-alice" {
		t.Errorf("override names %v, want the person who took over", override["user_id"])
	}
	// The default window is short on purpose: forgetting to hand back must not
	// silently rewrite the rota for the night.
	until, err := utils.ParseDatetime(utils.StrVal(override, "until"))
	if err != nil {
		t.Fatalf("parse until: %v", err)
	}
	if got := time.Until(until).Round(time.Minute); got != dutyTakeDefaultHours*time.Hour {
		t.Errorf("override runs for %s, want the %d-hour default", got, dutyTakeDefaultHours)
	}
}

// The override is what pages them; the check-in records that they said so, and
// must lapse with it rather than outliving it.
func TestDutyTakeChecksInForTheSameWindow(t *testing.T) {
	ms := takeoverStore(t, dailyRotation("usr-bob"))
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "duty take 4"); err != nil {
		t.Fatalf("duty take: %v", err)
	}

	user := ms.data["users"]["usr-alice"]
	if user["on_duty"] != true {
		t.Fatalf("on_duty = %v after taking over", user["on_duty"])
	}
	override := overridesOf(t, ms, "sch-1")[0].(map[string]any)
	if utils.StrVal(user, "duty_checkin_until") != utils.StrVal(override, "until") {
		t.Errorf("check-in until %v, override until %v — they must lapse together",
			user["duty_checkin_until"], override["until"])
	}
}

// Beyond a day it is not an emergency stand-in but a schedule change, which
// belongs where the whole team can see it.
func TestDutyTakeRejectsAnUnreasonableWindow(t *testing.T) {
	ms := takeoverStore(t, dailyRotation("usr-bob"))
	e := crudEngine(ms)

	for _, command := range []string{"duty take 0", "duty take 25"} {
		if _, err := runCommand(t, e, alice(authz.RoleResponder), command); err == nil {
			t.Errorf("%q was accepted", command)
		}
	}
	if len(overridesOf(t, ms, "sch-1")) != 0 {
		t.Error("a rejected takeover still wrote an override")
	}
}

// An id is exactly what somebody woken at 4am should not have to find, so one
// reachable schedule is chosen for them.
func TestDutyTakePicksTheOnlyReachableSchedule(t *testing.T) {
	ms := takeoverStore(t, dailyRotation("usr-bob"))
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "duty take"); err != nil {
		t.Fatalf("duty take: %v", err)
	}
	if len(overridesOf(t, ms, "sch-1")) != 1 {
		t.Error("the only reachable schedule was not chosen")
	}
}

// With several, guessing would be a coin flip over whose phone stops ringing.
func TestDutyTakeAsksWhichScheduleWhenSeveralAreReachable(t *testing.T) {
	second := dailyRotation("usr-bob")
	second["id"] = "sch-2"
	ms := takeoverStore(t, dailyRotation("usr-bob"), second)
	e := crudEngine(ms)

	_, err := runCommand(t, e, alice(authz.RoleResponder), "duty take")
	if err == nil {
		t.Fatal("a schedule was picked at random")
	}
	if len(overridesOf(t, ms, "sch-1")) != 0 || len(overridesOf(t, ms, "sch-2")) != 0 {
		t.Error("an ambiguous takeover still wrote an override")
	}
}

func TestDutyTakeRefusesAScheduleOutsideTheActorsTeams(t *testing.T) {
	sched := dailyRotation("usr-bob")
	sched["team_id"] = "team-payments"
	ms := takeoverStore(t, sched)
	e := crudEngine(ms)

	_, err := runCommand(t, e, alice(authz.RoleResponder, "team-infra"), "duty take sch-1")
	if err == nil {
		t.Fatal("took over a schedule belonging to another team")
	}
	if !isForbidden(err) && !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want a refusal", err)
	}
	if len(overridesOf(t, ms, "sch-1")) != 0 {
		t.Error("a refused takeover still wrote an override")
	}
}

// Being taken off duty without being told is the same failure as being put on
// it without being told: the person keeps behaving as though the pager is
// theirs. This test also keeps the notice's construction honest — it belongs to
// no alert group, and reaching for the group's id would panic.
func TestDutyTakeTellsThePersonBeingRelieved(t *testing.T) {
	ms := takeoverStore(t, dailyRotation("usr-bob"))
	// A real transport, not "log": a handover notice that lands in the service
	// log tells the person nothing, and shiftNotificationTargets excludes it for
	// exactly that reason.
	ms.seed("users", map[string]any{
		"id": "usr-bob", "username": "bob", "role": "responder",
		"notification_targets": []any{map[string]any{"type": "telegram", "target": "4343"}},
	})
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "duty take"); err != nil {
		t.Fatalf("duty take: %v", err)
	}

	var told []string
	for _, row := range ms.data["notifications"] {
		if utils.StrVal(row, "reason") == "duty taken over" {
			told = append(told, utils.StrVal(row, "user_id"))
		}
	}
	if len(told) != 1 || told[0] != "usr-bob" {
		t.Fatalf("notified %v, want the person who was relieved", told)
	}
}

// The person taking over typed the command; telling them what they just did is
// noise on a phone that is about to start receiving alerts.
func TestDutyTakeDoesNotNotifyTheTaker(t *testing.T) {
	ms := takeoverStore(t, dailyRotation("usr-alice"))
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "duty take"); err != nil {
		t.Fatalf("duty take: %v", err)
	}

	for _, row := range ms.data["notifications"] {
		if utils.StrVal(row, "reason") == "duty taken over" {
			t.Errorf("the person taking over was notified about their own command: %+v", row)
		}
	}
}

func TestPriorityChangesOwnLevel(t *testing.T) {
	ms := takeoverStore(t)
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "priority high"); err != nil {
		t.Fatalf("priority high: %v", err)
	}
	if got := ms.data["users"]["usr-alice"]["priority"]; got != "high" {
		t.Errorf("priority = %v, want high", got)
	}
}

// Reordering yourself in the queue is your business; deciding when a
// colleague's phone rings is an editor's.
func TestPriorityForSomeoneElseNeedsEditor(t *testing.T) {
	ms := takeoverStore(t)
	ms.seed("users", map[string]any{"id": "usr-bob", "username": "bob", "role": "responder"})
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "priority bob low"); err == nil {
		t.Fatal("a responder changed a colleague's priority")
	}
	if got := ms.data["users"]["usr-bob"]["priority"]; got != nil {
		t.Errorf("priority = %v, want it untouched", got)
	}

	if _, err := runCommand(t, e, alice(authz.RoleEditor), "priority bob low"); err != nil {
		t.Fatalf("editor could not set a colleague's priority: %v", err)
	}
	if got := ms.data["users"]["usr-bob"]["priority"]; got != "low" {
		t.Errorf("priority = %v, want low", got)
	}
}

func TestPriorityRejectsAnUnknownLevel(t *testing.T) {
	ms := takeoverStore(t)
	e := crudEngine(ms)

	if _, err := runCommand(t, e, alice(authz.RoleResponder), "priority urgent"); err == nil {
		t.Fatal("an unsupported level was accepted")
	}
	if got := ms.data["users"]["usr-alice"]["priority"]; got != nil {
		t.Errorf("priority = %v, want it untouched", got)
	}
}
