package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// shiftNotificationReason is the reason string on a shift-change notification.
// It is not an alert: no group, no severity, no escalation attached.
const shiftNotificationReason = "on-call shift started"

// ProcessScheduleShiftNotifications tells people when their on-call shift
// begins.
//
// Opt-in per schedule (`notify_on_shift_change`), because turning an existing
// deployment into one that messages people on every handoff is not a change to
// make on their behalf. The first cycle after enabling records the current
// roster without notifying: otherwise enabling the flag — or upgrading — would
// page whoever happens to be on call right then, for a shift they already know
// about.
//
// The idempotency key is the schedule, the user and the timestamp of the
// change, so a worker restart mid-cycle cannot send the same handoff twice.
//
// This runs on every worker cycle, so it must cost nothing when nothing
// happened — which is almost always. The cached schedule copy answers "did any
// opted-in schedule hand over?" without touching the database; only then does
// the transaction open, and it loads just the schedules and users involved.
// Notifications are written but never read: the committed shift_notification
// state, taken under this step's advisory lock, is what makes a handoff
// notify-once, so scanning the (largest) notifications table for duplicate
// idempotency keys would buy nothing and cost a full table read per cycle.
func (e *Engine) ProcessScheduleShiftNotifications(ctx context.Context) (int, error) {
	now := utils.UTCNow()
	ts := utils.ToISO(now)

	candidates, participants, err := e.pendingShiftHandoffs(ctx, now)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	loads := loadItems("schedules", candidates...)
	requested := make(map[string]bool, len(participants))
	for _, uid := range participants {
		requested[uid] = true
	}
	if len(participants) > 0 {
		loads = append(loads, store.LoadSpec{
			Collection: "users",
			Filters:    map[string]any{"id": toAnySlice(participants)},
		})
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loads,
		[]string{"schedules", "notifications"},
		func(state *store.State) (any, error) {
			// The pre-pass read a cached copy; everything is re-decided here
			// against the rows loaded under the lock.
			seen := map[string]struct{}{}
			sent := 0
			for schedID, sched := range state.Schedules {
				if !utils.BoolVal(sched, "notify_on_shift_change", false) {
					continue
				}
				current, _ := resolveScheduleAt(sched, now)
				previous, hadState := storedShiftRoster(sched)
				if hadState && sameIDs(previous, current) {
					continue // nothing changed; leave the row alone
				}
				wasOnCall := map[string]bool{}
				for _, uid := range previous {
					wasOnCall[uid] = true
				}
				// The roster can move between the pre-pass and this lock — a
				// handoff landing in that window, or a concurrent edit. Then
				// someone who needs telling was never loaded, and recording the
				// new roster here would lose their notification for good.
				// Leave the schedule untouched instead: the next cycle reads a
				// fresh copy and handles it.
				if hadState && !rosterFullyLoaded(current, wasOnCall, requested) {
					slog.Info("shift_notification_deferred",
						"schedule_id", schedID, "reason", "roster changed since pre-pass")
					continue
				}
				sched["updated_at"] = ts
				sched["shift_notification"] = map[string]any{
					"user_ids":    toAnySlice(current),
					"notified_at": ts,
				}
				if !hadState {
					// Baseline only: see the note above.
					continue
				}
				for _, uid := range current {
					if wasOnCall[uid] {
						continue // already on call, this handoff is not theirs
					}
					user := state.Users[uid]
					if user == nil {
						// Requested but absent: this person really is gone.
						slog.Warn("shift_notification_unknown_user",
							"schedule_id", schedID, "user_id", uid)
						continue
					}
					for _, target := range shiftNotificationTargets(user) {
						channel := utils.StrVal(target, "type")
						idemKey := fmt.Sprintf("shift:%s:%s:%s:%s", schedID, uid, channel, ts)
						ntf := model.NewNotification("", "", uid, channel,
							utils.StrVal(target, "target"), shiftNotificationReason, ts, idemKey)
						if channel != "log" && channel != "chatops" && channel != "mobile" {
							ntf.ScheduleDelivery(shiftNotificationPayload(sched, user))
						}
						addNotification(state, ntf, seen)
						sent++
					}
				}
			}
			return sent, nil
		}, advisoryLock["notify_schedule_shift"])
	if err != nil {
		return 0, err
	}
	return result.(int), nil
}

// rosterFullyLoaded reports whether everyone this handoff has to notify was
// among the users the transaction asked for. Someone already on call needs no
// message, so only the newcomers matter.
func rosterFullyLoaded(current []string, wasOnCall, requested map[string]bool) bool {
	for _, uid := range current {
		if !wasOnCall[uid] && !requested[uid] {
			return false
		}
	}
	return true
}

// pendingShiftHandoffs finds, off the cached schedules copy, which opted-in
// schedules changed roster and which users they now name. A stale cache can
// only produce a false positive, which the re-check under the lock drops, or
// delay a notification by at most the cache TTL — which is the worker's own
// cadence anyway.
func (e *Engine) pendingShiftHandoffs(ctx context.Context, now time.Time) ([]string, []string, error) {
	schedules, err := e.refCollection(ctx, "schedules")
	if err != nil {
		return nil, nil, err
	}
	var candidates []string
	seenUser := map[string]bool{}
	var participants []string
	for id, sched := range schedules {
		if !utils.BoolVal(sched, "notify_on_shift_change", false) {
			continue
		}
		current, _ := resolveScheduleAt(sched, now)
		previous, hadState := storedShiftRoster(sched)
		if hadState && sameIDs(previous, current) {
			continue
		}
		candidates = append(candidates, id)
		for _, uid := range current {
			if !seenUser[uid] {
				seenUser[uid] = true
				participants = append(participants, uid)
			}
		}
	}
	return candidates, participants, nil
}

func shiftNotificationState(sched map[string]any) map[string]any {
	m, _ := sched["shift_notification"].(map[string]any)
	return m
}

// storedShiftRoster returns the roster recorded at the last check, and whether
// there was one at all.
func storedShiftRoster(sched map[string]any) ([]string, bool) {
	m := shiftNotificationState(sched)
	if m == nil {
		return nil, false
	}
	ids, _ := utils.CoerceStringList(m["user_ids"])
	return ids, true
}

// shiftNotificationTargets picks where to tell someone their shift started.
//
// The user's own configured targets, minus "log": a handoff nobody can see is
// not worth a row. A user with only a log target gets nothing, which is the
// honest outcome — there is nowhere to send it.
func shiftNotificationTargets(user map[string]any) []map[string]any {
	var targets []map[string]any
	for _, raw := range anyList(user["notification_targets"]) {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		channel := utils.StrVal(t, "type")
		if channel == "" || channel == "log" || !supportedNotificationTargets[channel] {
			continue
		}
		target := utils.StrVal(t, "target")
		if target == "" {
			// Fall back to the profile field the channel uses, same as an
			// alert notification would.
			switch channel {
			case "email":
				target = utils.StrVal(user, "email")
			case "telegram":
				target = utils.StrVal(user, "telegram_id")
			case "call":
				target = utils.StrVal(user, "phone")
			}
		}
		if target == "" {
			continue
		}
		targets = append(targets, map[string]any{"type": channel, "target": target})
	}
	return targets
}

func shiftNotificationPayload(sched map[string]any, user map[string]any) map[string]any {
	return map[string]any{
		"title":       fmt.Sprintf("You are on call: %s", utils.StrVal(sched, "name")),
		"severity":    "info",
		"status":      "on_call",
		"reason":      shiftNotificationReason,
		"schedule_id": utils.StrVal(sched, "id"),
		// Marks the message as the one that should carry a "check in" button, so
		// confirming a shift is a tap rather than a remembered command. Read by
		// the Telegram adapter; other channels ignore it.
		"offer_checkin": true,
		"timezone":      utils.StrVal(sched, "timezone"),
		"user": map[string]any{
			"id":       utils.StrVal(user, "id"),
			"name":     utils.StrVal(user, "name"),
			"username": utils.StrVal(user, "username"),
		},
	}
}
