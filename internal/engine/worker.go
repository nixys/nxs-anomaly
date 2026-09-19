package engine

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/tracing"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Worker-cycle methods ──────────────────────────────────────────────────────

// ProcessDueEscalations advances due escalation steps and retries scheduled
// notifications. RunWorkerCycle is the scheduler entrypoint and additionally
// flushes notification batches, delivers notifications, archives old resolved
// groups, emits metrics, and logs cycle summaries.
func (e *Engine) ProcessDueEscalations(ctx context.Context) ([]map[string]any, error) {
	progressed, err := e.processDueEscalationsOnce(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := e.ProcessNotificationRetries(ctx); err != nil {
		return progressed, err
	}
	return progressed, nil
}

func (e *Engine) RunWorkerCycle(ctx context.Context) (map[string]any, error) {
	// Optional overall deadline so a hung stage (stuck DB query or adapter) cannot
	// block the worker indefinitely. Disabled (0) by default — see WorkerCycleTimeout.
	if e.deliveryCfg.WorkerCycleTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.deliveryCfg.WorkerCycleTimeout)
		defer cancel()
	}
	// One span per cycle, with each stage's duration as an attribute rather than
	// as a child span. The cycle is a fixed sequence of stages that always run,
	// so a dozen child spans per tick would say the same thing the durations
	// already do while multiplying the span volume of an idle deployment by the
	// tick rate. The spans worth having below this are the per-notification ones,
	// which are not fixed and are where time actually goes missing.
	ctx, cycleSpan := tracing.Start(ctx, "worker.cycle")
	defer cycleSpan.End()

	t0 := time.Now()
	stageMs := map[string]float64{}
	stage := func(name string, start time.Time) {
		ms := float64(time.Since(start).Microseconds()) / 1000
		stageMs[name] = ms
		cycleSpan.SetAttributes(attribute.Float64("nxs.stage."+name+"_ms", ms))
	}

	s := time.Now()
	reclaimed, err := e.ReclaimStaleNotificationClaims(ctx)
	if err != nil {
		slog.Warn("reclaim_stale_claims_failed", "error", err)
	}
	stage("reclaim", s)

	s = time.Now()
	progressed, err := e.processDueEscalationsOnce(ctx)
	if err != nil {
		return nil, err
	}
	stage("escalation", s)

	s = time.Now()
	shiftNotified, err := e.ProcessScheduleShiftNotifications(ctx)
	if err != nil {
		slog.Warn("schedule_shift_notifications_failed", "error", err)
	}
	if shiftNotified > 0 {
		e.sink().IncShiftNotifications(shiftNotified)
	}
	// Sources that should be talking and are not. Runs in the schedule stage
	// rather than the escalation one because it produces alerts through the
	// normal ingest path: doing it mid-escalation would have a cycle escalate
	// groups it had just created.
	if silent, err := e.ProcessSourceHeartbeats(ctx); err != nil {
		slog.Warn("source_heartbeats_failed", "error", err)
	} else if silent > 0 {
		slog.Warn("sources_silent", "count", silent)
	}
	// Check-ins expire on their own, and the shifts nobody confirmed are
	// counted. Runs before the coverage report so both readings describe the
	// same moment.
	if expired, err := e.ProcessDutyCheckins(ctx); err != nil {
		slog.Warn("duty_checkins_failed", "error", err)
	} else if expired > 0 {
		slog.Debug("duty_checkins_cleared", "count", expired)
	}
	// Standing coverage check: the chain gate can only judge a schedule as it
	// was when the chain was saved, and schedules drift afterwards. Throttled —
	// coverage changes when a human edits a schedule, not at worker cadence.
	// Heartbeat for the readiness report. Written to the database, not to
	// process memory, because the API that serves /readiness is a different
	// deployment from the worker in the documented production topology.
	e.recordWorkerHeartbeat(ctx)
	if time.Since(e.lastCoverageCheck) >= coverageCheckInterval {
		e.lastCoverageCheck = time.Now()
		if report, err := e.ScheduleCoverageReport(ctx); err != nil {
			slog.Warn("schedule_coverage_report_failed", "error", err)
		} else {
			degraded, _ := report["degraded_attached"].(int)
			total, _ := report["schedules_total"].(int)
			e.sink().SetScheduleCoverage(degraded, total)
			e.emitScheduleCoverage(ctx, report)
			if degraded > 0 {
				slog.Warn("schedules_without_coverage",
					"degraded_attached", degraded, "schedules_total", total)
			}
		}
	}
	stage("schedules", s)

	s = time.Now()
	flushed, err := e.ProcessNotificationBatches(ctx)
	if err != nil {
		return nil, err
	}
	stage("batches", s)

	s = time.Now()
	delivered, err := e.ProcessNotificationDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	stage("deliveries", s)

	s = time.Now()
	retried, err := e.ProcessNotificationRetries(ctx)
	if err != nil {
		return nil, err
	}
	stage("retries", s)

	s = time.Now()
	policySteps, err := e.advanceNotificationPolicyRuns(ctx)
	if err != nil {
		slog.Warn("advance_policy_runs_failed", "error", err)
	}
	stage("policy_runs", s)

	s = time.Now()
	purged := e.RetentionSweep(ctx)
	archived := purged[RetentionAlertGroups]
	archivedChatops := purged[RetentionChatopsMessages]
	// Expired and revoked sessions stop authenticating the moment they lapse
	// (the lookup filters on both); this only stops the table growing without
	// bound. The row is kept for a day after the fact so a session that was
	// live during an incident is still there to look at. Independent of the
	// web_sessions retention horizon, which bounds sessions that never expired.
	if _, err := e.store.DeleteExpiredWebSessions(ctx); err != nil {
		slog.Warn("clean_expired_web_sessions_failed", "error", err)
	}
	// Sign-in rate buckets. A bucket untouched for an hour has long since
	// refilled — the sign-in limiter's slowest bucket refills in under a minute —
	// so it grants exactly what a missing row grants, and keeping it only grows
	// the table by one row per client IP that ever tried to sign in.
	if _, err := e.store.PruneRateBuckets(ctx, utils.ToISO(utils.UTCNow().Add(-time.Hour))); err != nil {
		slog.Warn("prune_rate_buckets_failed", "error", err)
	}
	stage("archival", s)

	s = time.Now()
	published, kafkaErr := e.PublishKafkaOutbox(ctx)
	if kafkaErr != nil {
		slog.Error("kafka_outbox_publish_failed", "error", kafkaErr)
	}
	stage("kafka", s)

	failed := 0
	for _, n := range append(delivered, retried...) {
		if utils.StrVal(n, "status") == "failed" {
			failed++
		}
	}
	deliveredOK := 0
	for _, n := range delivered {
		if utils.StrVal(n, "status") == "delivered" {
			deliveredOK++
		}
	}
	result := map[string]any{
		"reclaimed_stale_claims":           reclaimed,
		"shift_notifications":              shiftNotified,
		"policy_steps_advanced":            policySteps,
		"processed_alert_groups":           len(progressed),
		"flushed_notification_batches":     len(flushed),
		"delivered_notifications":          deliveredOK,
		"retried_notifications":            len(retried),
		"failed_notifications":             failed,
		"archived_alert_groups":            archived,
		"archived_chatops_messages":        archivedChatops,
		"published_kafka_events":           published,
		"progressed_alert_groups":          progressed,
		"flushed_notification_batch_items": flushed,
		"delivered_notification_items":     delivered,
		"retried_notification_items":       retried,
		"stage_durations_ms":               stageMs,
	}
	cycleSpan.SetAttributes(
		attribute.Int("nxs.progressed_alert_groups", len(progressed)),
		attribute.Int("nxs.delivered_notifications", deliveredOK),
		attribute.Int("nxs.failed_notifications", failed),
	)
	// Only log if the cycle did real work to avoid per-tick noise at idle.
	if len(progressed) > 0 || len(flushed) > 0 || deliveredOK > 0 || len(retried) > 0 || failed > 0 || archived > 0 || archivedChatops > 0 || published > 0 || reclaimed > 0 || shiftNotified > 0 || policySteps > 0 {
		slog.Info("worker_cycle_complete",
			"reclaimed_stale_claims", result["reclaimed_stale_claims"],
			"processed_alert_groups", result["processed_alert_groups"],
			"flushed_notification_batches", result["flushed_notification_batches"],
			"delivered_notifications", result["delivered_notifications"],
			"retried_notifications", result["retried_notifications"],
			"failed_notifications", result["failed_notifications"],
			"archived_alert_groups", result["archived_alert_groups"],
			"shift_notifications", result["shift_notifications"],
			"duration_ms", time.Since(t0).Milliseconds(),
		)
	}
	return result, nil
}

// ReclaimStaleNotificationClaims returns notifications stuck in a transient claim
// status (their worker crashed mid-delivery) to their pending status. Disabled
// when ClaimTimeout <= 0.
func (e *Engine) ReclaimStaleNotificationClaims(ctx context.Context) (int, error) {
	if e.deliveryCfg.ClaimTimeout <= 0 {
		return 0, nil
	}
	cutoff := utils.ToISO(utils.UTCNow().Add(-e.deliveryCfg.ClaimTimeout))
	return e.store.ReclaimStaleClaims(ctx, cutoff)
}

// escalationRefs holds the read-only lookup collections an escalation shard
// needs, pre-fetched once per cycle from the short-TTL reference cache.
type escalationRefs struct {
	chatops, mobileDevices, users, teams, scheds, integs, chains map[string]map[string]any
}

// escalationShardKey maps an integration to one of 1024 advisory-lock buckets in
// a range mostly distinct from ingest (72545000+) and from the fixed operation
// locks, so escalation of different integrations runs in parallel across worker
// replicas and does not normally serialize against ingest.
//
// "Mostly" and "normally" are load-bearing: this window, [72546000, 72547023],
// shares its first 24 keys with the ingest window, and PostgreSQL keeps
// session-level and transaction-level advisory locks in ONE key space. On those
// keys an ingest holding pg_advisory_xact_lock makes the pg_try_advisory_lock
// below fail, and the shard is skipped with a debug line blaming a worker that
// does not exist. The skip is recovered on the next cycle and no state is
// corrupted. See ingestLockKey for why the ranges are not simply moved apart, and
// TestHashedLockWindowsOverlapIsTheKnownOne for the guard on the arithmetic.
func escalationShardKey(integrationID string) int64 {
	h := uint32(2166136261)
	for i := 0; i < len(integrationID); i++ {
		h ^= uint32(integrationID[i])
		h *= 16777619
	}
	return 72546000 + int64(h%1024)
}

// processDueEscalationsOnce fetches due groups and advances each one.
//
// Escalation is sharded by integration: due groups are partitioned into advisory
// shards (escalationShardKey) and each shard is processed under a non-blocking
// per-shard session lock. A worker that cannot take a shard skips it (another
// replica owns it this cycle) and moves on, so replicas escalate different
// integrations in parallel instead of serializing on one global lock. Because the
// shard lock already grants exclusive access to that shard's groups, the mutation
// runs with no inner advisory lock (lockKey 0).
func (e *Engine) processDueEscalationsOnce(ctx context.Context) ([]map[string]any, error) {
	now := utils.UTCNow()
	ts := utils.ToISO(now)

	dueGroups, err := e.store.ListDueAlertGroups(ctx, ts)
	if err != nil {
		return nil, err
	}
	if len(dueGroups) == 0 {
		return nil, nil
	}

	// Partition into shards by integration, preserving the oldest-first order
	// ListDueAlertGroups returns within each shard.
	shardOrder := make([]int64, 0)
	shards := make(map[int64][]map[string]any)
	for _, g := range dueGroups {
		sk := escalationShardKey(utils.StrVal(g, "integration_id"))
		if _, seen := shards[sk]; !seen {
			shardOrder = append(shardOrder, sk)
		}
		shards[sk] = append(shards[sk], g)
	}

	// Pre-fetch read-only lookup collections once for all shards. A failure
	// aborts the cycle rather than escalating against missing data: a step that
	// cannot see its schedule logs a coverage gap and consumes itself, which
	// would turn a database blip into "nobody was on call". See refSet.
	rs := e.newRefSet(ctx)
	refs := escalationRefs{}
	refs.chatops = rs.get("chatops_channels")
	refs.mobileDevices = rs.get("mobile_devices")
	refs.users = rs.get("users")
	refs.teams = rs.get("teams")
	refs.scheds = rs.get("schedules")
	refs.integs = rs.get("integrations")
	refs.chains = rs.get("escalation_chains")
	var chainIDs, integIDs, userIDs []string
	for _, g := range dueGroups {
		chainIDs = append(chainIDs, utils.StrVal(g, "escalation_chain_id"))
		integIDs = append(integIDs, utils.StrVal(g, "integration_id"))
	}
	refs.integs = rs.require(refs.integs, "integrations", integIDs)
	for _, id := range integIDs {
		userIDs = append(userIDs, policyUserIDs(refs.integs[id])...)
	}
	rs.requirePaging(&refs.chains, &refs.scheds, &refs.teams, &refs.users, chainIDs, userIDs)
	if err := rs.Err(); err != nil {
		return nil, err
	}

	var progressed []map[string]any
	for _, sk := range shardOrder {
		locked, unlock, err := e.store.TryAdvisoryLock(ctx, sk)
		if err != nil {
			return progressed, err
		}
		if !locked {
			slog.Debug("escalation_shard_skipped", "shard", sk, "reason", "another worker holds the shard")
			continue
		}
		p, err := e.advanceEscalationShard(ctx, now, ts, shards[sk], refs)
		unlock()
		if err != nil {
			return progressed, err
		}
		progressed = append(progressed, p...)
	}
	return progressed, nil
}

// advanceEscalationShard advances the due groups of one integration shard. The
// caller holds the shard's advisory lock, so the mutation runs lock-free
// (lockKey 0): no other worker touches this shard's groups concurrently.
func (e *Engine) advanceEscalationShard(ctx context.Context, now time.Time, ts string, groups []map[string]any, refs escalationRefs) ([]map[string]any, error) {
	dueIDList := make([]string, 0, len(groups))
	for _, g := range groups {
		dueIDList = append(dueIDList, utils.StrVal(g, "id"))
	}
	// Only the shard's due groups are loaded (mutator re-checks them by id);
	// batches are loaded open-only because attachNotificationBatch only matches
	// status "open" and new batches have no baseline, so they are always written.
	loads := append(loadGroups(dueIDList...),
		store.LoadSpec{Collection: "notification_batches", Filters: map[string]any{"status": "open"}})
	// The escalation steps executed below are the analytics events for how far a
	// group had to be pushed before somebody answered, so this transaction has
	// to be able to write them.
	saveCols := e.withAnalyticsOutbox([]string{"alert_groups", "notifications", "chatops_messages", "notification_batches", "notification_policy_runs"})

	result, err := e.store.UpdateCollectionsFiltered(ctx, loads, saveCols,
		func(state *store.State) (any, error) {
			state.ChatopsChannels = refs.chatops
			state.MobileDevices = refs.mobileDevices
			state.Users = refs.users
			state.Teams = refs.teams
			state.Schedules = refs.scheds
			state.Integrations = refs.integs
			state.EscalationChains = refs.chains
			var progressed []map[string]any
			for _, dg := range groups {
				groupID := utils.StrVal(dg, "id")
				g, ok := groupAG(state.AlertGroups[groupID])
				if !ok {
					continue
				}
				if g.SilenceExpired(now) {
					g.EndSilence(ts)
				}
				if g.Status() != model.StatusOpen {
					continue
				}
				nextRunAt := g.NextRunAt()
				if nextRunAt == "" {
					continue
				}
				due, err := utils.ParseDatetime(nextRunAt)
				if err != nil || due.After(now) {
					continue
				}
				g.AppendLog("escalation_resumed", "Resuming escalation after wait window", nil)
				g.ClearNextRunAt()
				e.advanceGroupLocked(state, g, ts)
				state.AlertGroups[groupID] = g
				progressed = append(progressed, g.Raw())
			}
			return progressed, nil
		}, 0)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	progressed := result.([]map[string]any)
	// A chain ending in a RESOLVE step closes the group unattended; its alerts
	// follow, exactly as they do when a person clicks resolve.
	var closed []string
	for _, g := range progressed {
		if utils.StrVal(g, "status") == model.StatusResolved {
			closed = append(closed, utils.StrVal(g, "id"))
		}
	}
	e.syncAlertStatusForGroups(ctx, closed, model.AlertStatusResolved)
	return progressed, nil
}

// ── Core escalation engine ────────────────────────────────────────────────────

// advanceGroupLocked walks the escalation chain from current_step,
// executing each step until a WAIT/REPEAT/RESOLVE/end is reached.
