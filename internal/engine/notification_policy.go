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

// Personal notification policies (BETA-031).
//
// A user may carry two ordered policies, "default" and "important". Each is a
// sequence of steps: notify a channel, then wait before falling back to the next
// channel. When a user is paged and has the selected policy, notifyUsers starts a
// run (nxs_anomaly_notification_policy_runs) instead of blasting every target at
// once; the worker advances the run over time and stops it the moment the group
// is acknowledged or resolved. That is the deterministic state machine the
// escalation-per-user fallback needs, and it survives restarts because the state
// is a row, not a goroutine.
//
// Policies are opt-in: a user with none keeps the previous behaviour (all targets
// fired together), so existing deployments and fixtures are unchanged until they
// choose a policy or migrate their targets (MigrateUserTargetsToDefaultPolicy).

// policyChannels are the channels a personal policy step may name.
var policyChannels = map[string]bool{
	"telegram": true, "email": true, "webhook": true, "call": true, "log": true,
}

// policyStep is one step: notify Channel (Target overrides the user's profile
// address), then wait WaitMinutes before the next step (the fallback). The last
// step's wait is irrelevant — there is nothing to fall back to.
type policyStep struct {
	Channel     string
	Target      string
	WaitMinutes int
}

// normalizePolicyName maps anything that is not "important" to "default".
func normalizePolicyName(name string) string {
	if name == "important" {
		return "important"
	}
	return "default"
}

// userPolicySteps returns the validated steps of the named policy for a user, or
// nil when the user has no usable policy. An "important" policy with no steps
// falls back to "default", so a step can ask for important without every user
// having to define one.
func userPolicySteps(user map[string]any, policyName string) []policyStep {
	policies, _ := user["notification_policies"].(map[string]any)
	if policies == nil {
		return nil
	}
	name := normalizePolicyName(policyName)
	raw, _ := policies[name].([]any)
	if len(raw) == 0 && name == "important" {
		raw, _ = policies["default"].([]any)
	}
	var steps []policyStep
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		ch := utils.StrVal(m, "channel")
		if !policyChannels[ch] {
			continue
		}
		wait := utils.IntVal(m, "wait_minutes")
		if wait < 0 {
			wait = 0
		}
		steps = append(steps, policyStep{Channel: ch, Target: utils.StrVal(m, "target"), WaitMinutes: wait})
	}
	return steps
}

// resolveChannelTarget picks the delivery address for a channel: an explicit
// override wins, otherwise the user's profile field. Returns ok=false when a
// channel needs an address and none is available (e.g. webhook with no URL, or
// telegram for a user with no telegram_id) so the caller can record a gap rather
// than queue a delivery that must fail.
func resolveChannelTarget(user map[string]any, channel, explicit string) (string, bool) {
	if explicit != "" {
		return explicit, true
	}
	switch channel {
	case "log":
		return "", true
	case "email":
		if v := utils.StrVal(user, "email"); v != "" {
			return v, true
		}
	case "telegram":
		if v := utils.StrVal(user, "telegram_id"); v != "" {
			return v, true
		}
	case "call":
		if v := utils.StrVal(user, "phone"); v != "" {
			return v, true
		}
	case "webhook":
		// A webhook step needs its own URL; there is no user-profile fallback.
	}
	return "", false
}

// startPolicyRun begins a personal notification policy run for a user against a
// group. It returns true when a run was started — telling notifyUsers not to also
// blast the user's raw targets — and false when the user has no usable policy, so
// the caller keeps the legacy behaviour.
func (e *Engine) startPolicyRun(state *store.State, g model.AlertGroup, user map[string]any, policyName, reason, timestamp string, seen map[string]struct{}) bool {
	steps := userPolicySteps(user, policyName)
	if len(steps) == 0 {
		return false
	}
	groupID := g.ID()
	userID := utils.StrVal(user, "id")
	// One in-flight run per (group, user, escalation firing): a later NOTIFY of
	// the same user (a REPEAT, or a second chain step) re-arms with a new key
	// rather than stacking a duplicate run.
	escKey := fmt.Sprintf("%d:%d:%s", g.CurrentStep(), g.RepeatCount(), normalizePolicyName(policyName))
	for _, raw := range state.NotificationPolicyRuns {
		if utils.StrVal(raw, "alert_group_id") == groupID &&
			utils.StrVal(raw, "user_id") == userID &&
			utils.StrVal(raw, "escalation_key") == escKey {
			return true
		}
	}
	run := map[string]any{
		"id":             utils.MakeID("npr"),
		"alert_group_id": groupID,
		"user_id":        userID,
		"policy":         normalizePolicyName(policyName),
		"escalation_key": escKey,
		"step_index":     0,
		"status":         "active",
		"next_step_at":   timestamp,
		"created_at":     timestamp,
		"updated_at":     timestamp,
	}
	state.NotificationPolicyRuns[run["id"].(string)] = run
	e.runPolicySteps(state, g, user, run, steps, reason, timestamp, seen)
	return true
}

// runPolicySteps fires steps starting at run["step_index"], walking through any
// zero-wait steps in one pass and scheduling the next step when a wait is set.
// It mutates run in place (step_index / next_step_at / status).
func (e *Engine) runPolicySteps(state *store.State, g model.AlertGroup, user map[string]any, run map[string]any, steps []policyStep, reason, timestamp string, seen map[string]struct{}) {
	idx := utils.IntVal(run, "step_index")
	now, err := utils.ParseDatetime(timestamp)
	if err != nil {
		now = utils.UTCNow()
	}
	for idx < len(steps) {
		e.firePolicyStep(state, g, user, steps[idx], run, idx, reason, timestamp, seen)
		waited := steps[idx].WaitMinutes
		idx++
		if idx < len(steps) && waited > 0 {
			run["step_index"] = idx
			run["next_step_at"] = utils.ToISO(now.Add(time.Duration(waited) * time.Minute))
			run["status"] = "active"
			run["updated_at"] = timestamp
			return
		}
	}
	run["step_index"] = idx
	run["status"] = "done"
	run["next_step_at"] = nil
	run["updated_at"] = timestamp
}

// firePolicyStep queues one channel notification for the policy run. The
// idempotency key is deterministic in the run and step, so a re-processed cycle
// never sends the same step twice.
func (e *Engine) firePolicyStep(state *store.State, g model.AlertGroup, user map[string]any, step policyStep, run map[string]any, stepIndex int, reason, timestamp string, seen map[string]struct{}) {
	target, ok := resolveChannelTarget(user, step.Channel, step.Target)
	if !ok {
		g.AppendLog("policy_step_no_target",
			fmt.Sprintf("Policy step skipped: no %s address for %s", step.Channel, strDefault(utils.StrVal(user, "username"), utils.StrVal(user, "id"))),
			map[string]any{"channel": step.Channel})
		return
	}
	idem := fmt.Sprintf("policy:%s:%d", utils.StrVal(run, "id"), stepIndex)
	ntf := buildNotification(g, utils.StrVal(user, "id"), step.Channel, target, reason, timestamp, idem)
	ntf.ScheduleDelivery(notificationPayload(g, user, reason))
	addNotification(state, ntf, seen)
}

// advanceNotificationPolicyRuns advances every due policy run one worker cycle.
// Returns how many runs were advanced. Runs whose group is acknowledged,
// resolved or gone are completed and pruned.
func (e *Engine) advanceNotificationPolicyRuns(ctx context.Context) (int, error) {
	runs, err := e.ListCollection(ctx, "notification_policy_runs")
	if err != nil {
		return 0, err
	}
	now := utils.UTCNow()
	ts := utils.ToISO(now)

	var dueRunIDs []string
	groupSet := map[string]bool{}
	userSet := map[string]bool{}
	for _, r := range runs {
		if utils.StrVal(r, "status") != "active" {
			continue
		}
		at, err := utils.ParseDatetime(utils.StrVal(r, "next_step_at"))
		if err != nil || at.After(now) {
			continue
		}
		dueRunIDs = append(dueRunIDs, utils.StrVal(r, "id"))
		groupSet[utils.StrVal(r, "alert_group_id")] = true
		userSet[utils.StrVal(r, "user_id")] = true
	}
	if len(dueRunIDs) == 0 {
		return 0, nil
	}

	loads := loadItems("notification_policy_runs", dueRunIDs...)
	loads = append(loads, loadItems("alert_groups", setKeys(groupSet)...)...)
	loads = append(loads, loadItems("users", setKeys(userSet)...)...)
	saveCols := []string{"notification_policy_runs", "notifications", "alert_groups"}

	var completed []string
	advanced := 0
	_, err = e.store.UpdateCollectionsFiltered(ctx, loads, saveCols,
		func(state *store.State) (any, error) {
			for _, runID := range dueRunIDs {
				run := state.NotificationPolicyRuns[runID]
				if run == nil || utils.StrVal(run, "status") != "active" {
					continue
				}
				at, err := utils.ParseDatetime(utils.StrVal(run, "next_step_at"))
				if err != nil || at.After(now) {
					continue // re-check under lock: another worker got here first
				}
				g, ok := groupAG(state.AlertGroups[utils.StrVal(run, "alert_group_id")])
				if !ok {
					run["status"] = "done"
					run["updated_at"] = ts
					completed = append(completed, runID)
					continue
				}
				// Stop-on-response: the fallback exists to keep paging until
				// someone picks up. Once the group is acknowledged or resolved
				// there is nobody left to fall back to.
				if g.Status() != model.StatusOpen {
					run["status"] = "done"
					run["updated_at"] = ts
					completed = append(completed, runID)
					continue
				}
				user := state.Users[utils.StrVal(run, "user_id")]
				if user == nil {
					run["status"] = "done"
					run["updated_at"] = ts
					completed = append(completed, runID)
					continue
				}
				steps := userPolicySteps(user, utils.StrVal(run, "policy"))
				e.runPolicySteps(state, g, user, run, steps, "notification policy fallback", ts, nil)
				state.AlertGroups[g.ID()] = g
				advanced++
				if utils.StrVal(run, "status") == "done" {
					completed = append(completed, runID)
				}
			}
			return nil, nil
		}, advisoryLock["advance_policy_runs"])
	if err != nil {
		return 0, err
	}

	for _, id := range completed {
		if _, derr := e.store.DeleteItem(ctx, "notification_policy_runs", id); derr != nil {
			slog.Warn("prune_policy_run_failed", "id", id, "error", derr)
		}
	}
	return advanced, nil
}

// SendTestNotification delivers a single test notification to one of a user's
// channels synchronously and returns the provider outcome, so the UI can show
// whether the channel is actually configured and reachable. channel defaults to
// the first step of the user's default policy, or "log" when there is none.
func (e *Engine) SendTestNotification(ctx context.Context, userID, channel string) (map[string]any, error) {
	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errNotFound(fmt.Sprintf("user %s not found", userID))
	}
	if channel == "" {
		if steps := userPolicySteps(user, "default"); len(steps) > 0 {
			channel = steps[0].Channel
		} else {
			channel = "log"
		}
	}
	if !policyChannels[channel] {
		return nil, errValidation(fmt.Sprintf("unsupported test channel %q", channel))
	}
	target, ok := resolveChannelTarget(user, channel, "")
	if !ok {
		return map[string]any{
			"user_id":    userID,
			"channel":    channel,
			"status":     "skipped",
			"reason":     "no_target",
			"configured": false,
			"detail":     fmt.Sprintf("user has no %s address configured", channel),
		}, nil
	}
	n := model.NewNotification("", "", userID, channel, target, "test notification requested from the UI", utils.ToISO(utils.UTCNow()), "")
	n.ScheduleDelivery(map[string]any{
		"title":    "nxs-anomaly test notification",
		"severity": "info",
		"reason":   "test notification requested from the UI",
	})
	out := e.deliverNotificationViaAdapter(ctx, n.Raw())
	res := map[string]any{
		"user_id":         userID,
		"channel":         channel,
		"target":          target,
		"status":          out.Status,
		"provider_status": out.ProviderStatus,
		"configured":      out.Status != deliverySkipped,
	}
	if out.Code > 0 {
		res["provider_code"] = out.Code
	}
	if out.Response != "" {
		res["response"] = out.Response
	}
	if out.Err != "" {
		res["error"] = out.Err
	}
	return res, nil
}

// MigrateUserTargetsToDefaultPolicy backfills a "default" personal policy from a
// user's notification_targets when they have targets but no policy yet. Each
// target becomes one immediate (wait 0) step, so behaviour is unchanged — the
// legacy blast and a zero-wait default policy fire the same notifications — while
// giving the user an editable policy to build on. Returns the number of users
// migrated. Idempotent: users that already have a policy are left alone.
func (e *Engine) MigrateUserTargetsToDefaultPolicy(ctx context.Context) (int, error) {
	users, err := e.ListCollection(ctx, "users")
	if err != nil {
		return 0, err
	}
	var toMigrate []string
	for _, u := range users {
		if _, has := u["notification_policies"].(map[string]any); has {
			continue
		}
		if targets, _ := u["notification_targets"].([]any); len(targets) > 0 {
			toMigrate = append(toMigrate, utils.StrVal(u, "id"))
		}
	}
	if len(toMigrate) == 0 {
		return 0, nil
	}
	ts := utils.ToISO(utils.UTCNow())
	migrated := 0
	_, err = e.store.UpdateCollectionsFiltered(ctx, loadItems("users", toMigrate...), []string{"users"},
		func(state *store.State) (any, error) {
			for _, id := range toMigrate {
				user := state.Users[id]
				if user == nil {
					continue
				}
				if _, has := user["notification_policies"].(map[string]any); has {
					continue
				}
				targets, _ := user["notification_targets"].([]any)
				var steps []any
				for _, raw := range targets {
					t, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					ch := utils.StrVal(t, "type")
					if !policyChannels[ch] {
						continue
					}
					steps = append(steps, map[string]any{
						"channel":      ch,
						"target":       utils.StrVal(t, "target"),
						"wait_minutes": 0,
					})
				}
				if len(steps) == 0 {
					continue
				}
				user["notification_policies"] = map[string]any{"default": steps}
				user["updated_at"] = ts
				migrated++
			}
			return nil, nil
		}, advisoryLock["update_user"])
	if err != nil {
		return 0, err
	}
	return migrated, nil
}
