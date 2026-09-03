package engine

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func (e *Engine) advanceGroupLocked(state *store.State, g model.AlertGroup, timestamp string) {
	if g.Status() != model.StatusOpen {
		return
	}
	chainID := g.EscalationChainID()
	chain := state.EscalationChains[chainID]
	if chain == nil {
		g.AppendLog("missing_chain", "Escalation chain not found", nil)
		g.ClearNextRunAt()
		return
	}
	steps, _ := chain["steps"].([]any)
	currentStep := g.CurrentStep()

	for currentStep < len(steps) && g.Status() == model.StatusOpen {
		step, ok := steps[currentStep].(map[string]any)
		if !ok {
			currentStep++
			continue
		}
		kind := utils.StrVal(step, "kind")
		g.SetUpdatedAt(timestamp)
		slog.Debug("escalation_step", "group_id", g.ID(), "step", currentStep, "kind", kind)
		// Emitted before the step runs rather than after, because several kinds
		// return from inside the switch. The mutator is atomic, so an event for a
		// step whose transaction then rolls back cannot survive — the ordering
		// here decides nothing except how many places have to remember to emit.
		e.emitEscalationStep(state, g, currentStep, kind, timestamp)

		switch kind {
		case StepWait:
			delayMin := utils.IntVal(step, "delay_minutes")
			due := mustParseTime(timestamp).Add(time.Duration(delayMin) * time.Minute)
			g.SetNextRunAt(utils.ToISO(due))
			g.SetCurrentStep(currentStep + 1)
			g.AppendLog("wait_scheduled", "Scheduled next escalation step after wait window",
				map[string]any{"next_run_at": g.NextRunAt()})
			return

		case StepNotifyUser:
			recipients := anyToStringSlice(step["user_ids"])
			// A step may page with the user's "important" policy instead of the
			// default one; unset means default.
			e.notifyUsersPolicy(state, g, recipients, "direct user notification", timestamp,
				normalizePolicyName(utils.StrVal(step, "notify_policy")))

		case StepNotifySchedule:
			schedID := utils.StrVal(step, "schedule_id")
			sched := state.Schedules[schedID]
			recipients := e.getScheduleUserIDs(sched, mustParseTime(timestamp))
			if len(recipients) > 0 {
				e.notifyUsers(state, g, recipients, "on-call schedule notification", timestamp)
			} else {
				g.AppendLog("schedule_gap", "No active user found in current schedule window", nil)
			}

		case StepNotifyTeam:
			teamID := utils.StrVal(step, "team_id")
			team := state.Teams[teamID]
			var recipients []string
			if team != nil {
				recipients = anyToStringSlice(team["member_ids"])
			}
			if len(recipients) > 0 {
				e.notifyUsers(state, g, recipients, "team escalation notification", timestamp)
			} else {
				g.AppendLog("team_empty", "Team escalation step has no members", nil)
			}

		case StepNotifyEmergency:
			recipients := e.emergencyUserIDs(state, g, utils.StrVal(step, "user_id"))
			if len(recipients) > 0 {
				e.notifyUsers(state, g, recipients, "emergency escalation notification", timestamp)
			} else {
				g.AppendLog("emergency_missing", "Emergency escalation step has no configured user", nil)
			}

		case StepNotifyDutyUsers:
			recipients := e.electDutyUsers(state, step)
			if len(recipients) > 0 {
				e.notifyUsers(state, g, recipients, "duty user notification", timestamp)
			} else {
				g.AppendLog("no_duty_users", "No on-duty users available for this escalation step", nil)
			}

		case StepTriggerWebhook:
			e.triggerWebhook(state, g, utils.StrVal(step, "webhook_url"), timestamp)

		case StepCreateIssue:
			e.executeCreateIssue(state, g, step, timestamp)

		case StepResolve:
			// An escalation policy step runs unattended: no principal is behind it.
			g.Resolve(timestamp, "Resolved by escalation policy", authz.SystemActor)
			e.notifyGroupResolved(state, g, timestamp)
			return

		case StepRepeat:
			fromPos := utils.IntVal(step, "from_position")
			maxRepeats := maxInt(utils.IntVal(step, "max_repeat_count"), 1)
			cooldownMin := utils.IntVal(step, "cooldown_minutes")
			repeatCount := g.RepeatCount() + 1
			if repeatCount > maxRepeats {
				g.AppendLog("escalation_repeat_exhausted",
					fmt.Sprintf("REPEAT exhausted after %d repetitions", maxRepeats), nil)
				g.ClearNextRunAt()
				return
			}
			g.SetRepeatCount(repeatCount)
			due := mustParseTime(timestamp).Add(time.Duration(cooldownMin) * time.Minute)
			g.SetNextRunAt(utils.ToISO(due))
			g.SetCurrentStep(fromPos)
			g.AppendLog("escalation_repeat",
				fmt.Sprintf("Repeating escalation from step %d (#%d/%d)", fromPos, repeatCount, maxRepeats), nil)
			return
		}

		currentStep++
		g.SetCurrentStep(currentStep)
		g.ClearNextRunAt()
	}

	if g.Status() == model.StatusOpen {
		g.AppendLog("escalation_complete", "Escalation chain completed", nil)
		g.ClearNextRunAt()
		// The chain is out of steps and the group is still open: everyone the
		// policy names has been tried and nobody has answered. It is a separate
		// event because it cannot be derived from the step events — a chain that
		// finished and one that is waiting on its last WAIT look identical from
		// the outside until this is emitted.
		e.emitGroupEvent(state, g, EventEscalationExhausted, timestamp, map[string]any{
			"steps_executed": g.CurrentStep(),
			"repeat_count":   g.RepeatCount(),
		})
	}
}

// emitEscalationStep records one executed step of the chain.
//
// Depth is what this is for: "the third page went unanswered and it took the
// REPEAT to reach anybody" is not visible in the group's final state, and it is
// exactly what a chain gets tuned on. The step kind travels with it; nothing
// from the step's configuration does, since a webhook URL is a credential and a
// user id is already an entity dimension elsewhere.
func (e *Engine) emitEscalationStep(state *store.State, g model.AlertGroup, index int, kind, timestamp string) {
	e.emitGroupEvent(state, g, EventEscalationStep, timestamp, map[string]any{
		"step":         index,
		"kind":         kind,
		"repeat_count": g.RepeatCount(),
	})
}

// electDutyUsers returns on-duty user IDs for a NOTIFY_DUTY_USERS step.
func (e *Engine) electDutyUsers(state *store.State, step map[string]any) []string {
	teamID := utils.StrVal(step, "team_id")
	fallbackToAll := utils.BoolVal(step, "fallback_to_all", true)

	var candidateIDs []string
	if teamID != "" {
		team := state.Teams[teamID]
		if team != nil {
			candidateIDs = anyToStringSlice(team["member_ids"])
		}
	} else {
		for uid := range state.Users {
			candidateIDs = append(candidateIDs, uid)
		}
	}

	var onDuty []map[string]any
	for _, uid := range candidateIDs {
		u := state.Users[uid]
		if u != nil && utils.BoolVal(u, "on_duty", false) {
			onDuty = append(onDuty, u)
		}
	}
	if len(onDuty) == 0 {
		if fallbackToAll {
			return candidateIDs
		}
		return nil
	}
	for _, priority := range priorityOrder {
		var tier []string
		for _, u := range onDuty {
			if strDefault(utils.StrVal(u, "priority"), "medium") == priority {
				tier = append(tier, utils.StrVal(u, "id"))
			}
		}
		if len(tier) > 0 {
			return tier
		}
	}
	var ids []string
	for _, u := range onDuty {
		ids = append(ids, utils.StrVal(u, "id"))
	}
	return ids
}

// emergencyUserIDs resolves emergency user IDs for a NOTIFY_EMERGENCY step.
func (e *Engine) emergencyUserIDs(state *store.State, g model.AlertGroup, explicitUserID string) []string {
	if explicitUserID != "" {
		return []string{explicitUserID}
	}
	integID := g.IntegrationID()
	integ := state.Integrations[integID]
	policy, _ := integ["notification_policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	if uid := utils.StrVal(policy, "emergency_user_id"); uid != "" {
		return []string{uid}
	}
	if uid := utils.StrVal(policy, "epic_user_id"); uid != "" {
		return []string{uid}
	}
	legacyPool, _ := integ["legacy_pool"].(map[string]any)
	if legacyPool != nil {
		if uid := utils.StrVal(legacyPool, "emergency_user_id"); uid != "" {
			return []string{uid}
		}
	}
	return nil
}

// checkEpicThreshold sends an epic alert notification when count/time threshold is crossed.
func (e *Engine) checkEpicThreshold(state *store.State, g model.AlertGroup, integration map[string]any, timestamp string) {
	policy, _ := integration["notification_policy"].(map[string]any)
	if policy == nil {
		return
	}
	epicUserID := utils.StrVal(policy, "epic_user_id")
	thresholdCount := utils.IntVal(policy, "epic_threshold_count")
	thresholdSecs := utils.IntVal(policy, "epic_threshold_seconds")
	if epicUserID == "" || (thresholdCount <= 0 && thresholdSecs <= 0) {
		return
	}
	if g.EpicSentAt() != "" {
		return
	}
	count := g.AlertCount()
	if thresholdCount > 0 && count >= thresholdCount {
		e.sendEpicNotification(state, g, epicUserID, timestamp)
		return
	}
	if thresholdSecs > 0 && count > 1 {
		createdAt := g.CreatedAt()
		t0, err := utils.ParseDatetime(createdAt)
		if err == nil {
			t1, err := utils.ParseDatetime(timestamp)
			if err == nil && t1.Sub(t0).Seconds() >= float64(thresholdSecs) {
				e.sendEpicNotification(state, g, epicUserID, timestamp)
			}
		}
	}
}

func (e *Engine) sendEpicNotification(state *store.State, g model.AlertGroup, epicUserID, timestamp string) {
	user := state.Users[epicUserID]
	if user == nil {
		return
	}
	count := g.AlertCount()
	reason := fmt.Sprintf("epic alert: %d events accumulated in group", count)
	targets, _ := user["notification_targets"].([]any)
	if len(targets) == 0 {
		targets = []any{map[string]any{"type": "log"}}
	}
	for _, raw := range targets {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		channel := strDefault(utils.StrVal(t, "type"), "log")
		target := utils.StrVal(t, "target")
		if channel == "email" && target == "" {
			target = utils.StrVal(user, "email")
		} else if channel == "telegram" && target == "" {
			target = utils.StrVal(user, "telegram_id")
		}
		ntf := buildNotification(g, epicUserID, channel, target, reason, timestamp, "")
		payload := notificationPayload(g, user, reason)
		payload["epic_alert"] = true
		payload["epic_count"] = count
		ntf.ScheduleDelivery(payload)
		addNotification(state, ntf, nil)
	}
	g.SetEpicSentAt(timestamp)
	g.AppendLog("epic_alert",
		fmt.Sprintf("Epic alert sent to %s", utils.StrVal(user, "username")),
		map[string]any{"epic_count": count, "epic_user_id": epicUserID})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustParseTime(s string) time.Time {
	t, err := utils.ParseDatetime(s)
	if err != nil {
		return utils.UTCNow()
	}
	return t
}

// GetHistory returns paginated alert group history with notifications and delivery attempts.
