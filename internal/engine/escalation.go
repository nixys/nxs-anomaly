package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// syncAlertStatusForGroups carries a group transition down to the alerts that
// belong to it: closing a group closes its alerts, reopening one reopens them.
//
// Called after the transition's transaction has committed, never inside it —
// see store.SetAlertStatusForGroups for why, and for why a failure here is a
// warning rather than an error. The group state is already durable at this
// point; refusing the operator's resolve because a follow-up write failed would
// trade a cosmetic inconsistency for a lost action.
func (e *Engine) syncAlertStatusForGroups(ctx context.Context, groupIDs []string, status string) {
	if len(groupIDs) == 0 {
		return
	}
	if _, err := e.store.SetAlertStatusForGroups(ctx, groupIDs, status); err != nil {
		slog.Warn("alert_status_sync_failed",
			"groups", groupIDs, "status", status, "err", err,
			"effect", "the group's alerts keep the status their source last reported")
	}
}

// ── Public group state-transition methods ─────────────────────────────────────
//
// Transition invariants (guards, field updates, log entries) live in
// model.AlertGroup; this file only orchestrates locking and bulk semantics.

// loadGroups builds a LoadSpec list selecting only the named alert groups,
// so state transitions don't load the whole alert_groups table under lock.
func loadGroups(ids ...string) []store.LoadSpec {
	return loadItems("alert_groups", ids...)
}

func (e *Engine) AcknowledgeGroup(ctx context.Context, groupID string) (map[string]any, error) {
	if err := e.authorizeGroupAction(ctx, groupID); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupID), e.withAnalyticsOutbox([]string{"alert_groups"}),
		func(state *store.State) (any, error) {
			g, err := getGroupOrError(state, groupID)
			if err != nil {
				return nil, err
			}
			// Read before the call: acknowledging an already-acknowledged group
			// is allowed and refreshes the timestamps, but analytics must see
			// the first transition only. A second event would move MTTA to
			// whenever somebody last clicked.
			firstAck := !g.IsAcknowledged()
			if err := g.Acknowledge(ts, "Alert group acknowledged by "+actor.Describe(), actor); err != nil {
				return nil, errValidation(err.Error())
			}
			if firstAck {
				e.emitGroupEvent(state, g, EventGroupAcknowledged, ts, map[string]any{
					"actor_kind": actor.Kind,
					"actor_role": string(actor.Role),
				})
			}
			e.auditIn(state, ctx, AuditAcknowledge, "alert_group", groupID, nil)
			return g.Raw(), nil
		}, advisoryLock["acknowledge_group"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

// resolveNotifyRefs pre-loads the collections notifyGroupResolved reads.
//
// It reads state.Users to find the people the group woke and state.Integrations
// for the notification policy that decides their channels. Neither is loaded by
// loadGroups, so before this existed the resolve-notification path found an
// empty user map, reported every recipient as unknown, and still marked the
// group as notified — which meant the notification was suppressed for good. The
// symptom was silence, on the one message that says the incident is over.
//
// Fetched through the reference cache outside the lock, the way ingest does it,
// rather than by widening the transaction's load set: these are read-only here,
// and loading every user inside the advisory lock would put the resolve path's
// cost on the size of the deployment.
func (e *Engine) resolveNotifyRefs(ctx context.Context) (users, integrations map[string]map[string]any, err error) {
	if !e.deliveryCfg.NotifyOnResolve {
		// Off by default. Nothing reads these maps in that case, and loading
		// them would be work every resolve pays for a feature it is not using.
		return nil, nil, nil
	}
	refs := e.newRefSet(ctx)
	users = refs.get("users")
	integrations = refs.get("integrations")
	if err := refs.Err(); err != nil {
		return nil, nil, err
	}
	return users, integrations, nil
}

func (e *Engine) ResolveGroup(ctx context.Context, groupID string) (map[string]any, error) {
	if err := e.authorizeGroupAction(ctx, groupID); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	users, integrations, err := e.resolveNotifyRefs(ctx)
	if err != nil {
		return nil, err
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupID), e.withAnalyticsOutbox([]string{"alert_groups", "notifications"}),
		func(state *store.State) (any, error) {
			state.Users, state.Integrations = users, integrations
			g, err := getGroupOrError(state, groupID)
			if err != nil {
				return nil, err
			}
			// Same reasoning as the acknowledgement: resolving is idempotent
			// and allowed from any state, so only the transition out of an
			// unresolved status is an episode ending.
			firstResolve := !g.IsResolved()
			g.Resolve(ts, "Resolved by "+actor.Describe(), actor)
			if firstResolve {
				e.emitGroupEvent(state, g, EventGroupResolved, ts, map[string]any{
					"actor_kind": actor.Kind,
					"actor_role": string(actor.Role),
					"resolution": "operator",
				})
			}
			e.notifyGroupResolved(state, g, ts)
			e.auditIn(state, ctx, AuditResolve, "alert_group", groupID, nil)
			return g.Raw(), nil
		}, advisoryLock["resolve_group"])
	if err != nil {
		return nil, err
	}
	e.syncAlertStatusForGroups(ctx, []string{groupID}, model.AlertStatusResolved)
	return result.(map[string]any), nil
}

func (e *Engine) UnresolveGroup(ctx context.Context, groupID string) (map[string]any, error) {
	if err := e.authorizeGroupAction(ctx, groupID); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupID), e.withAnalyticsOutbox([]string{"alert_groups"}),
		func(state *store.State) (any, error) {
			g, err := getGroupOrError(state, groupID)
			if err != nil {
				return nil, err
			}
			if err := g.Unresolve(ts, actor); err != nil {
				return nil, errValidation(err.Error())
			}
			// Unresolve started a new episode inside the model, so this event
			// carries the new one: it opens the second pass rather than
			// amending the first.
			e.emitGroupEvent(state, g, EventGroupReopened, ts, map[string]any{
				"actor_kind": actor.Kind,
				"reason":     "unresolved",
			})
			e.auditIn(state, ctx, AuditUnresolve, "alert_group", groupID, nil)
			return g.Raw(), nil
		}, advisoryLock["unresolve_group"])
	if err != nil {
		return nil, err
	}
	e.syncAlertStatusForGroups(ctx, []string{groupID}, model.AlertStatusFiring)
	// Unresolve makes the group due immediately (next_run_at = now) — wake
	// the worker instead of waiting out the poll interval.
	e.wakeWorker(ctx)
	return result.(map[string]any), nil
}

func (e *Engine) UnacknowledgeGroup(ctx context.Context, groupID string) (map[string]any, error) {
	if err := e.authorizeGroupAction(ctx, groupID); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupID), e.withAnalyticsOutbox([]string{"alert_groups"}),
		func(state *store.State) (any, error) {
			g, err := getGroupOrError(state, groupID)
			if err != nil {
				return nil, err
			}
			if err := g.Unacknowledge(ts, actor); err != nil {
				return nil, errValidation(err.Error())
			}
			// Not a new episode: the same response is still running, somebody
			// just took the acknowledgement back. Its MTTA stands.
			e.emitGroupEvent(state, g, EventGroupUnacknowledged, ts, map[string]any{
				"actor_kind": actor.Kind,
			})
			e.auditIn(state, ctx, AuditUnacknowledge, "alert_group", groupID, nil)
			return g.Raw(), nil
		}, advisoryLock["unacknowledge_group"])
	if err != nil {
		return nil, err
	}
	// Unacknowledge resumes escalation immediately (next_run_at = now).
	e.wakeWorker(ctx)
	return result.(map[string]any), nil
}

func (e *Engine) SilenceGroup(ctx context.Context, groupID string, durationMinutes int) (map[string]any, error) {
	if err := e.authorizeGroupAction(ctx, groupID); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	var silencedUntil string
	if durationMinutes > 0 {
		silencedUntil = utils.ToISO(utils.UTCNow().Add(time.Duration(durationMinutes) * time.Minute))
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupID), e.withAnalyticsOutbox([]string{"alert_groups"}),
		func(state *store.State) (any, error) {
			g, err := getGroupOrError(state, groupID)
			if err != nil {
				return nil, err
			}
			if err := g.Silence(ts, silencedUntil, "Alert group silenced by "+actor.Describe(), durationMinutes, actor); err != nil {
				return nil, errValidation(err.Error())
			}
			e.emitGroupEvent(state, g, EventGroupSilenced, ts, map[string]any{
				"actor_kind":       actor.Kind,
				"duration_minutes": durationMinutes,
			})
			e.auditIn(state, ctx, AuditSilence, "alert_group", groupID, map[string]any{"duration_minutes": durationMinutes})
			return g.Raw(), nil
		}, advisoryLock["silence_group"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) BulkAcknowledgeGroups(ctx context.Context, groupIDs []string) (map[string]any, error) {
	if len(groupIDs) == 0 {
		return nil, errValidation("group_ids must be a non-empty list")
	}
	groupIDs, forbidden, err := e.partitionAuthorizedGroups(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupIDs...), e.withAnalyticsOutbox([]string{"alert_groups"}),
		func(state *store.State) (any, error) {
			var acknowledged, notFound, skipped []string
			for _, gid := range groupIDs {
				g, ok := groupAG(state.AlertGroups[gid])
				if !ok {
					notFound = append(notFound, gid)
					continue
				}
				// Bulk semantics: already-acknowledged groups are skipped, not
				// re-acknowledged (unlike the single-group operation).
				if g.IsResolved() || g.IsAcknowledged() {
					skipped = append(skipped, gid)
					continue
				}
				if err := g.Acknowledge(ts, "Alert group acknowledged by "+actor.Describe()+" (bulk)", actor); err != nil {
					skipped = append(skipped, gid)
					continue
				}
				// One event per id that actually transitioned. Emitting for the
				// skipped and not-found ones would count acknowledgements that
				// nobody made.
				e.emitGroupEvent(state, g, EventGroupAcknowledged, ts, map[string]any{
					"actor_kind": actor.Kind,
					"actor_role": string(actor.Role),
					"bulk":       true,
				})
				acknowledged = append(acknowledged, gid)
			}
			e.auditBulkIn(state, ctx, AuditAcknowledge, acknowledged)
			return map[string]any{
				"acknowledged": acknowledged,
				"not_found":    notFound,
				"skipped":      skipped,
			}, nil
		}, advisoryLock["bulk_acknowledge_groups"])
	if err != nil {
		return nil, err
	}
	out := result.(map[string]any)
	// Ids dropped by the team boundary are reported rather than folded into
	// "skipped": "you may not touch this" and "there was nothing to do" are
	// different answers, and only one of them is a permissions problem.
	if len(forbidden) > 0 {
		out["forbidden"] = forbidden
	}
	return out, nil
}

func (e *Engine) BulkSilenceGroups(ctx context.Context, groupIDs []string, durationMinutes int) (map[string]any, error) {
	if len(groupIDs) == 0 {
		return nil, errValidation("group_ids must be a non-empty list")
	}
	groupIDs, forbidden, err := e.partitionAuthorizedGroups(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	var silencedUntil string
	if durationMinutes > 0 {
		silencedUntil = utils.ToISO(utils.UTCNow().Add(time.Duration(durationMinutes) * time.Minute))
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupIDs...), e.withAnalyticsOutbox([]string{"alert_groups"}),
		func(state *store.State) (any, error) {
			var silenced, notFound, skipped []string
			for _, gid := range groupIDs {
				g, ok := groupAG(state.AlertGroups[gid])
				if !ok {
					notFound = append(notFound, gid)
					continue
				}
				if err := g.Silence(ts, silencedUntil, "Alert group silenced by "+actor.Describe()+" (bulk)", durationMinutes, actor); err != nil {
					skipped = append(skipped, gid)
					continue
				}
				e.emitGroupEvent(state, g, EventGroupSilenced, ts, map[string]any{
					"actor_kind":       actor.Kind,
					"duration_minutes": durationMinutes,
					"bulk":             true,
				})
				silenced = append(silenced, gid)
			}
			e.auditBulkIn(state, ctx, AuditSilence, silenced)
			return map[string]any{
				"silenced":  silenced,
				"not_found": notFound,
				"skipped":   skipped,
			}, nil
		}, advisoryLock["bulk_silence_groups"])
	if err != nil {
		return nil, err
	}
	out := result.(map[string]any)
	// Ids dropped by the team boundary are reported rather than folded into
	// "skipped": "you may not touch this" and "there was nothing to do" are
	// different answers, and only one of them is a permissions problem.
	if len(forbidden) > 0 {
		out["forbidden"] = forbidden
	}
	return out, nil
}

func (e *Engine) BulkResolveGroups(ctx context.Context, groupIDs []string) (map[string]any, error) {
	if len(groupIDs) == 0 {
		return nil, errValidation("group_ids must be a non-empty list")
	}
	groupIDs, forbidden, err := e.partitionAuthorizedGroups(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	actor := authz.FromContext(ctx)
	users, integrations, err := e.resolveNotifyRefs(ctx)
	if err != nil {
		return nil, err
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(groupIDs...), e.withAnalyticsOutbox([]string{"alert_groups", "notifications"}),
		func(state *store.State) (any, error) {
			state.Users, state.Integrations = users, integrations
			var resolved, notFound, alreadyResolved []string
			for _, gid := range groupIDs {
				g, ok := groupAG(state.AlertGroups[gid])
				if !ok {
					notFound = append(notFound, gid)
					continue
				}
				if g.IsResolved() {
					alreadyResolved = append(alreadyResolved, gid)
					continue
				}
				g.Resolve(ts, "Resolved by "+actor.Describe()+" (bulk)", actor)
				e.emitGroupEvent(state, g, EventGroupResolved, ts, map[string]any{
					"actor_kind": actor.Kind,
					"actor_role": string(actor.Role),
					"resolution": "operator",
					"bulk":       true,
				})
				e.notifyGroupResolved(state, g, ts)
				resolved = append(resolved, gid)
			}
			e.auditBulkIn(state, ctx, AuditResolve, resolved)
			return map[string]any{
				"resolved":         resolved,
				"not_found":        notFound,
				"already_resolved": alreadyResolved,
			}, nil
		}, advisoryLock["bulk_resolve_groups"])
	if err != nil {
		return nil, err
	}
	out := result.(map[string]any)
	// Groups that were already resolved are included, not just the ones this
	// call closed: they are resolved groups whose alerts may still read
	// "firing" (closed before this propagation existed, or by a call whose
	// follow-up write failed), and the statement skips rows already in place.
	closed, _ := out["resolved"].([]string)
	if already, _ := out["already_resolved"].([]string); len(already) > 0 {
		closed = append(append([]string{}, closed...), already...)
	}
	e.syncAlertStatusForGroups(ctx, closed, model.AlertStatusResolved)
	// Ids dropped by the team boundary are reported rather than folded into
	// "skipped": "you may not touch this" and "there was nothing to do" are
	// different answers, and only one of them is a permissions problem.
	if len(forbidden) > 0 {
		out["forbidden"] = forbidden
	}
	return out, nil
}
