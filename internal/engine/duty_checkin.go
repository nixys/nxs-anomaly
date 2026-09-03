package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// duty_checkin.go keeps the on_duty flag honest.
//
// The schedule decides who *should* be on call; the flag records who confirmed
// they are. Two things follow, and neither exists without this file:
//
//   - A check-in expires by itself. Without that the flag survives the shift,
//     and the NOTIFY_DUTY_USERS step keeps paging whoever last remembered to
//     type "duty on" — days later. That is the defect the old nxs-alert had with
//     its manual duty on/off, and it is worth not inheriting.
//   - The gap between the two is reported. A schedule can be fully covered on
//     paper while nobody has actually acknowledged the shift, and until now that
//     was invisible: coverage answers "is somebody assigned", never "did they
//     say yes".

// ProcessDutyCheckins expires lapsed check-ins and reports how many people who
// are on call right now have not confirmed it.
//
// Returns the number of check-ins cleared.
func (e *Engine) ProcessDutyCheckins(ctx context.Context) (int, error) {
	now := utils.UTCNow()
	result, err := e.store.UpdateCollectionsFiltered(ctx,
		[]store.LoadSpec{
			// Only people who claim to be on duty can have a check-in to expire;
			// on_duty is a typed column, so this is an indexed read rather than
			// the whole user table every cycle.
			{Collection: "users", Filters: map[string]any{"on_duty": true}},
			{Collection: "schedules"},
		},
		[]string{"users"},
		func(state *store.State) (any, error) {
			var expired []string
			for id, user := range state.Users {
				until := utils.StrVal(user, "duty_checkin_until")
				if until == "" {
					// On duty without a check-in: set through the API or the web
					// interface, which is a standing assignment rather than a
					// shift confirmation. Not this mechanism's to clear.
					continue
				}
				deadline, err := utils.ParseDatetime(until)
				if err != nil {
					// An unparsable deadline is not a licence to page someone
					// forever; treat it as lapsed and say so.
					slog.Warn("duty_checkin_bad_deadline", "user_id", id, "value", until)
					deadline = time.Time{}
				}
				if deadline.After(now) {
					continue
				}
				user["on_duty"] = false
				user["duty_checkin_at"] = nil
				user["duty_checkin_until"] = nil
				user["updated_at"] = utils.ToISO(now)
				expired = append(expired, id)
			}
			onCall, confirmed := dutyAttendance(state, now)
			return map[string]any{
				"expired": expired, "on_call": onCall, "confirmed": confirmed,
			}, nil
		}, advisoryLock["duty_checkins"])
	if err != nil {
		return 0, err
	}
	res, _ := result.(map[string]any)
	expired, _ := res["expired"].([]string)
	onCall, _ := res["on_call"].(int)
	confirmed, _ := res["confirmed"].(int)

	e.sink().SetDutyAttendance(onCall-confirmed, onCall)
	if len(expired) > 0 {
		slog.Info("duty_checkins_expired", "count", len(expired), "user_ids", joinStrings(expired, ","))
	}
	if missing := onCall - confirmed; missing > 0 {
		slog.Warn("duty_shift_without_checkin", "missing", missing, "on_call", onCall)
	}
	return len(expired), nil
}

// dutyAttendance counts the people on call across all schedules right now, and
// how many of them have a live check-in.
//
// Counted per person rather than per schedule: someone covering two schedules
// confirms once, and counting them twice would make attendance look worse the
// more shifts a person takes on.
//
// It runs after the expiry pass in the same mutator, so a check-in that lapsed
// this cycle is already not counted as confirmation.
func dutyAttendance(state *store.State, now time.Time) (onCall, confirmed int) {
	people := map[string]bool{}
	for _, schedule := range state.Schedules {
		for _, id := range ScheduleOnCallAt(schedule, now) {
			people[id] = true
		}
	}
	for id := range people {
		onCall++
		user := state.Users[id]
		if user == nil {
			// Not loaded means not among the on_duty rows this step read: the
			// filter is exactly "claims to be on duty", so absence is an answer,
			// not a gap.
			continue
		}
		if utils.BoolVal(user, "on_duty", false) && utils.StrVal(user, "duty_checkin_until") != "" {
			confirmed++
		}
	}
	return onCall, confirmed
}
