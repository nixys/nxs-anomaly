package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The schedule owns who is on call; on_duty records who confirmed it. These
// tests pin the two properties that make the second worth having: a check-in
// expires on its own, and the gap between "should be on call" and "said so" is
// counted rather than assumed away.

// dailyRotation puts each member on call for a day at a time, starting at a
// fixed instant so a test can reason about when a shift ends.
func dailyRotation(members ...string) map[string]any {
	return map[string]any{
		"id":       "sch-1",
		"name":     "primary",
		"timezone": "UTC",
		"rotation": map[string]any{
			"enabled":          true,
			"participant_ids":  toAnySlice(members),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"start_at":         "2026-01-01T00:00:00Z",
		},
	}
}

func dutyStore(t *testing.T, users []map[string]any, schedules ...map[string]any) *memStore {
	t.Helper()
	ms := newMemStore()
	for _, u := range users {
		ms.seed("users", u)
	}
	for _, s := range schedules {
		ms.seed("schedules", s)
	}
	return ms
}

func checkedInUser(id, until string) map[string]any {
	return map[string]any{
		"id": id, "username": id, "role": "responder",
		"on_duty": true, "duty_checkin_at": "2026-08-01T00:00:00Z", "duty_checkin_until": until,
	}
}

// A flag nobody clears is the failure this mechanism exists to prevent: the
// escalation step would keep paging whoever last typed "duty on".
func TestExpiredCheckinClearsOnDuty(t *testing.T) {
	past := utils.ToISO(utils.UTCNow().Add(-time.Minute))
	ms := dutyStore(t, []map[string]any{checkedInUser("usr-alice", past)})
	e := crudEngine(ms)

	cleared, err := e.ProcessDutyCheckins(context.Background())
	if err != nil {
		t.Fatalf("ProcessDutyCheckins: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared %d check-in(s), want 1", cleared)
	}
	user := ms.data["users"]["usr-alice"]
	if user["on_duty"] != false {
		t.Errorf("on_duty = %v after the check-in lapsed, want false", user["on_duty"])
	}
	if user["duty_checkin_until"] != nil {
		t.Errorf("duty_checkin_until = %v, want it cleared with the flag", user["duty_checkin_until"])
	}
}

func TestLiveCheckinSurvives(t *testing.T) {
	future := utils.ToISO(utils.UTCNow().Add(time.Hour))
	ms := dutyStore(t, []map[string]any{checkedInUser("usr-alice", future)})
	e := crudEngine(ms)

	if cleared, err := e.ProcessDutyCheckins(context.Background()); err != nil || cleared != 0 {
		t.Fatalf("cleared %d check-in(s) (err %v), want the live one left alone", cleared, err)
	}
	if ms.data["users"]["usr-alice"]["on_duty"] != true {
		t.Error("a live check-in was cleared")
	}
}

// on_duty set through the API or the web interface is a standing assignment,
// not a shift confirmation. This step did not create it and must not clear it.
func TestOnDutyWithoutACheckinIsLeftAlone(t *testing.T) {
	ms := dutyStore(t, []map[string]any{
		{"id": "usr-bob", "username": "bob", "on_duty": true},
	})
	e := crudEngine(ms)

	if cleared, _ := e.ProcessDutyCheckins(context.Background()); cleared != 0 {
		t.Fatalf("cleared %d flag(s) that this mechanism never set", cleared)
	}
	if ms.data["users"]["usr-bob"]["on_duty"] != true {
		t.Error("a standing on-duty assignment was cleared")
	}
}

// An unparsable deadline must not become a licence to page someone forever.
func TestUnparsableDeadlineIsTreatedAsLapsed(t *testing.T) {
	ms := dutyStore(t, []map[string]any{checkedInUser("usr-alice", "not-a-time")})
	e := crudEngine(ms)

	if cleared, _ := e.ProcessDutyCheckins(context.Background()); cleared != 1 {
		t.Fatal("a check-in with a broken deadline was kept")
	}
}

// Coverage says somebody is assigned; attendance says whether they answered.
//
// Both rotations name one participant, so who is on call does not depend on the
// day the suite runs. The first version of this test used a two-person rotation
// and derived its expectation from alice's flag — which stays true because her
// check-in is live, not because she is the one on call. It passed on the day it
// was written and failed the next, when the handoff moved to bob.
func TestAttendanceCountsShiftsNobodyConfirmed(t *testing.T) {
	future := utils.ToISO(utils.UTCNow().Add(time.Hour))

	t.Run("the person on call confirmed", func(t *testing.T) {
		ms := dutyStore(t,
			[]map[string]any{checkedInUser("usr-alice", future)},
			dailyRotation("usr-alice"),
		)
		sink := newRecordingSink()
		e := crudEngine(ms)
		e.SetMetricsSink(sink)

		if _, err := e.ProcessDutyCheckins(context.Background()); err != nil {
			t.Fatalf("ProcessDutyCheckins: %v", err)
		}
		if sink.dutyOnCall != 1 || sink.dutyMissing != 0 {
			t.Errorf("on call = %d, missing = %d; want 1 and 0", sink.dutyOnCall, sink.dutyMissing)
		}
	})

	t.Run("the person on call did not", func(t *testing.T) {
		// Alice is checked in but the rota names bob: a shift that is covered on
		// paper and unconfirmed in fact, which is the gap this metric exists for.
		ms := dutyStore(t,
			[]map[string]any{checkedInUser("usr-alice", future)},
			dailyRotation("usr-bob"),
		)
		sink := newRecordingSink()
		e := crudEngine(ms)
		e.SetMetricsSink(sink)

		if _, err := e.ProcessDutyCheckins(context.Background()); err != nil {
			t.Fatalf("ProcessDutyCheckins: %v", err)
		}
		if sink.dutyOnCall != 1 || sink.dutyMissing != 1 {
			t.Errorf("on call = %d, missing = %d; want 1 and 1", sink.dutyOnCall, sink.dutyMissing)
		}
	})
}

// A person covering two schedules confirms once. Counting them per schedule
// would make attendance look worse the more shifts somebody takes on.
func TestAttendanceCountsPeopleNotShifts(t *testing.T) {
	second := dailyRotation("usr-alice")
	second["id"] = "sch-2"
	ms := dutyStore(t, nil, dailyRotation("usr-alice"), second)
	sink := newRecordingSink()
	e := crudEngine(ms)
	e.SetMetricsSink(sink)

	if _, err := e.ProcessDutyCheckins(context.Background()); err != nil {
		t.Fatalf("ProcessDutyCheckins: %v", err)
	}

	if sink.dutyOnCall != 1 {
		t.Errorf("on call = %d, want one person counted once across two schedules", sink.dutyOnCall)
	}
}

// A check-in should lapse when the shift does, not on a timer that outlives it.
func TestCheckinExpiresAtTheEndOfTheShift(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	s := store.NewState()
	s.Schedules["sch-1"] = dailyRotation("usr-alice", "usr-bob")

	expiry := dutyCheckinExpiry(s, ScheduleOnCallAt(s.Schedules["sch-1"], now)[0], now)

	if !expiry.After(now) {
		t.Fatalf("expiry %s is not after %s", expiry, now)
	}
	if got := expiry.Sub(now); got > 24*time.Hour {
		t.Errorf("expiry is %s away, want it inside the daily shift rather than the fallback", got)
	}
}

// Someone standing in during an incident is on no schedule, so there is no
// shift end to expire at — the fallback keeps the flag from becoming permanent.
func TestCheckinOffScheduleFallsBackToATimeout(t *testing.T) {
	now := utils.UTCNow()
	s := store.NewState()
	s.Schedules["sch-1"] = dailyRotation("usr-alice")

	expiry := dutyCheckinExpiry(s, "usr-standin", now)

	if got := expiry.Sub(now); got < dutyCheckinFallback-time.Minute || got > dutyCheckinFallback+time.Minute {
		t.Errorf("expiry is %s away, want the %s fallback", got, dutyCheckinFallback)
	}
}

// Checking in is a statement about a person. The service principal a shared chat
// falls back to is not one, and letting it set the flag would put the chat on call.
func TestDutyCommandRefusesAnUnidentifiedSender(t *testing.T) {
	ms := dutyStore(t, []map[string]any{{"id": "usr-alice", "username": "alice"}})
	ms.seed("chatops_channels", map[string]any{
		"id": "chn-1", "platform": "telegram", "external_id": "-100500", "commands_enabled": true,
	})
	e := crudEngine(ms)
	ctx := authz.NewContext(context.Background(), authz.Actor{
		ID: "chatops:telegram", Kind: authz.KindService, Role: authz.RoleResponder,
	})

	_, err := e.PostChatopsCommand(ctx, map[string]any{
		"channel_id": "chn-1", "command": "duty on", "actor": "somebody",
	})
	if err == nil {
		t.Fatal("the service principal checked itself in")
	}
	if !isForbidden(err) {
		t.Errorf("err = %v, want a forbidden error", err)
	}
}

func TestDutyCommandChecksInAndOut(t *testing.T) {
	ms := dutyStore(t, []map[string]any{{"id": "usr-alice", "username": "alice", "role": "responder"}})
	ms.seed("chatops_channels", map[string]any{
		"id": "chn-1", "platform": "telegram", "external_id": "-100500", "commands_enabled": true,
	})
	e := crudEngine(ms)
	ctx := authz.NewContext(context.Background(), authz.Actor{
		ID: "usr-alice", Kind: authz.KindUser, DisplayName: "alice", Role: authz.RoleResponder,
	})

	if _, err := e.PostChatopsCommand(ctx, map[string]any{
		"channel_id": "chn-1", "command": "duty on", "actor": "alice",
	}); err != nil {
		t.Fatalf("duty on: %v", err)
	}
	user := ms.data["users"]["usr-alice"]
	if user["on_duty"] != true {
		t.Fatalf("on_duty = %v after checking in", user["on_duty"])
	}
	if utils.StrVal(user, "duty_checkin_until") == "" {
		t.Error("checked in without a deadline; the flag would never lapse")
	}

	if _, err := e.PostChatopsCommand(ctx, map[string]any{
		"channel_id": "chn-1", "command": "duty off", "actor": "alice",
	}); err != nil {
		t.Fatalf("duty off: %v", err)
	}
	user = ms.data["users"]["usr-alice"]
	if user["on_duty"] != false || user["duty_checkin_until"] != nil {
		t.Errorf("after checking out: on_duty=%v until=%v", user["on_duty"], user["duty_checkin_until"])
	}
}

// The gap is a standing condition, and the worker checks it every cycle. It was
// warned about every cycle too — twelve identical lines a minute for as long as
// a shift went unconfirmed. It is logged when it changes.
func TestUnconfirmedShiftIsLoggedOnChangeNotEveryCycle(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	h := &capturingHandler{}
	slog.SetDefault(slog.New(h))

	ms := dutyStore(t, nil, dailyRotation("usr-bob"))
	e := crudEngine(ms)
	count := func() int {
		n := 0
		for _, m := range h.msgs {
			if m == "duty_shift_without_checkin" {
				n++
			}
		}
		return n
	}
	for i := 0; i < 5; i++ {
		if _, err := e.ProcessDutyCheckins(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(); got != 1 {
		t.Fatalf("5 cycles with the same unconfirmed shift logged %d warnings, want 1", got)
	}

	// A second schedule puts another unconfirmed person on call: that is news.
	second := dailyRotation("usr-carol")
	second["id"] = "sch-2"
	if err := ms.UpsertItem(context.Background(), "schedules", second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ProcessDutyCheckins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 2 {
		t.Errorf("a changed gap logged %d warnings in total, want 2", got)
	}
}
