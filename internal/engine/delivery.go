package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Batch / delivery / retry processing ──────────────────────────────────────

// runBounded applies fn to each item with at most `limit` concurrent goroutines
// and returns the results fn chose to keep (second return value true). It is used
// to parallelize the per-notification provider calls, which dominate a worker
// cycle's latency: each call is independent and side-effect-free with respect to
// shared engine state (the *http.Client and the sync.Map template cache are
// concurrency-safe), so only the result collection is mutex-guarded. Order of
// results is unspecified; callers key results by notification id. A limit < 1
// runs sequentially, which is also the zero-value default for an unset config.
func runBounded[T, R any](items []T, limit int, fn func(T) (R, bool)) []R {
	if limit < 1 {
		limit = 1
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []R
	)
	sem := make(chan struct{}, limit)
	for _, item := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func(item T) {
			defer wg.Done()
			defer func() { <-sem }()
			// Backstop so a panicking task never crashes the worker process. The
			// delivery/retry callers recover inside fn (turning a panic into a
			// dropped result the reaper re-delivers); this catches any other caller.
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("runbounded_task_panicked", "panic", rec)
				}
			}()
			if r, ok := fn(item); ok {
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}(item)
	}
	wg.Wait()
	return results
}

func (e *Engine) ProcessNotificationBatches(ctx context.Context) ([]map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	dueBatches, err := e.store.ListDueNotificationBatches(ctx, ts)
	if err != nil {
		return nil, err
	}
	if len(dueBatches) == 0 {
		return nil, nil
	}

	batchIDs := make([]any, 0, len(dueBatches))
	groupIDSet := map[string]bool{}
	for _, b := range dueBatches {
		batchIDs = append(batchIDs, utils.StrVal(b, "id"))
		if gid := utils.StrVal(b, "alert_group_id"); gid != "" {
			groupIDSet[gid] = true
		}
	}
	// Candidate groups for payload rebuild: the batches' own groups plus the
	// groups of currently batched notifications. Read outside the lock — a
	// notification attached after this read whose group differs from its
	// batch's group would fail context rebuild, the same outcome as a
	// genuinely missing group.
	preNotifs, err := e.store.ListItemsIn(ctx, "notifications", "batch_id", batchIDs)
	if err != nil {
		return nil, err
	}
	for _, n := range preNotifs {
		if gid := utils.StrVal(n, "alert_group_id"); gid != "" {
			groupIDSet[gid] = true
		}
	}
	groupIDs := make([]any, 0, len(groupIDSet))
	for gid := range groupIDSet {
		groupIDs = append(groupIDs, gid)
	}

	// users is read-only in the mutator — served from the reference cache.
	// Flushing a batch against an unreadable user table would resolve every
	// recipient to nothing, so the failure has to stop the flush. See refSet.
	refs := e.newRefSet(ctx)
	usersMap := refs.get("users")
	recipients := make([]string, 0, len(preNotifs))
	for _, n := range preNotifs {
		recipients = append(recipients, utils.StrVal(n, "user_id"))
	}
	usersMap = refs.require(usersMap, "users", recipients)
	if err := refs.Err(); err != nil {
		return nil, err
	}

	loads := []store.LoadSpec{
		{Collection: "notification_batches", Filters: map[string]any{"id": batchIDs}},
		{Collection: "notifications", Filters: map[string]any{"batch_id": batchIDs}},
		{Collection: "alert_groups", Filters: map[string]any{"id": groupIDs}},
	}

	type batchOutcome struct {
		flushed   []map[string]any
		failedCtx []map[string]any
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx, loads,
		[]string{"notification_batches", "notifications"},
		func(state *store.State) (any, error) {
			state.Users = usersMap
			var out batchOutcome
			for _, dueBatch := range dueBatches {
				batchID := utils.StrVal(dueBatch, "id")
				cur, _ := state.NotificationBatches[batchID].(*model.NotificationBatch)
				if cur == nil {
					cur = model.WrapNotificationBatch(dueBatch)
				}
				if !cur.IsOpen() {
					continue
				}
				var batchNotifs []map[string]any
				for _, rec := range state.Notifications {
					n := notificationMap(rec)
					if utils.StrVal(n, "batch_id") == batchID && utils.StrVal(n, "status") == model.NotificationBatched {
						batchNotifs = append(batchNotifs, n)
					}
				}
				for _, ntf := range batchNotifs {
					n := model.WrapNotification(ntf)
					payload := n.Payload()
					if payload == nil {
						g, hasGroup := groupAG(state.AlertGroups[n.AlertGroupID()])
						user := state.Users[n.UserID()]
						if hasGroup && user != nil {
							payload = notificationPayload(g, user, strDefault(n.Reason(), "batched notification"))
						}
					}
					if payload == nil {
						n.FailBatchContext(ts)
						out.failedCtx = append(out.failedCtx, n.Raw())
					} else {
						n.ReleaseFromBatch(ts, payload)
					}
					state.Notifications[n.ID()] = n
				}
				cur.Close(ts, len(batchNotifs))
				state.NotificationBatches[batchID] = cur
				out.flushed = append(out.flushed, cur.ToMap())
			}
			return out, nil
		}, advisoryLock["process_notification_batches"])
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	out := result.(batchOutcome)
	e.fireDeadLetters(ctx, out.failedCtx)
	return out.flushed, nil
}

func (e *Engine) ProcessNotificationDeliveries(ctx context.Context) ([]map[string]any, error) {
	// Atomically claim a batch: each row moves delivery_scheduled → 'delivering'
	// and is handed to exactly one worker, so concurrent worker replicas never
	// deliver the same notification twice.
	deliverable, err := e.store.ClaimDeliverableNotifications(ctx, e.workerID, utils.ToISO(utils.UTCNow()))
	if err != nil {
		return nil, err
	}
	if len(deliverable) == 0 {
		return nil, nil
	}

	type deliveryResult struct {
		ntf            map[string]any
		attempt        map[string]any
		outcome        deliveryOutcome
		finishedAt     string
		shortCircuited bool
	}
	// HTTP/SMTP/etc. delivery runs concurrently (bounded) — it is the slow part
	// of the cycle and already happens outside the advisory lock. Rows are already
	// claimed ('delivering'), so this loop owns them exclusively.
	results := runBounded(deliverable, e.deliveryCfg.DeliveryConcurrency, func(ntf map[string]any) (res deliveryResult, ok bool) {
		// A panic in any adapter must not crash the worker. Drop the row from the
		// results (ok=false) so it stays claimed ('delivering') and the reaper
		// re-delivers it after ClaimTimeout — the notification is never lost.
		defer func() {
			if rec := recover(); rec != nil {
				nw := model.WrapNotification(ntf)
				slog.Error("delivery_task_panicked", "stage", "deliveries", "notification_id", nw.ID(), "channel", nw.Channel(), "panic", rec)
				e.sink().IncWorkerPanic("deliveries")
				res, ok = deliveryResult{}, false
			}
		}()
		n := model.WrapNotification(ntf)
		breakerKey := n.Channel() + ":" + n.Target()
		if !e.breaker.Allow(breakerKey, utils.UTCNow()) {
			// Provider breaker is open: don't call it. Release the claim back to
			// delivery_scheduled so the next cycle re-picks it once the breaker
			// half-opens (instead of leaving it stuck until the reaper).
			e.sink().IncDeliveryShortCircuited(n.Channel())
			return deliveryResult{ntf: ntf, shortCircuited: true}, true
		}
		attemptNum := n.RetryCount() + 1
		tStart := utils.ToISO(utils.UTCNow())
		callStart := time.Now()
		outcome := e.deliverNotificationViaAdapter(ctx, ntf)
		elapsed := time.Since(callStart)
		e.sink().ObserveDeliveryLatency(n.Channel(), elapsed.Seconds())
		// A skip is not a provider verdict: it never reached one, so it must
		// not push the breaker in either direction.
		if outcome.Status != deliverySkipped {
			e.breaker.Record(breakerKey, outcome.Status == deliveryDelivered, utils.UTCNow())
		}
		tEnd := utils.ToISO(utils.UTCNow())
		attempt := deliveryAttemptRow(n, attemptNum, outcome, tStart, tEnd, elapsed)
		return deliveryResult{ntf: ntf, attempt: attempt, outcome: outcome, finishedAt: tEnd}, true
	})

	if len(results) == 0 {
		return nil, nil
	}

	ntfIDs := make([]any, 0, len(results))
	for _, r := range results {
		ntfIDs = append(ntfIDs, model.WrapNotification(r.ntf).ID())
	}

	// notification_delivery_attempts is append-only here (the mutator never
	// reads existing attempts), so it is saved but not loaded: without a
	// baseline every attempt placed into state is treated as new and written.
	// Notifications are loaded by id: the mutator only finalizes the rows this
	// worker claimed (status still 'delivering'). Every claimed row is rewritten,
	// so "notifications" is in writeAll (skip the baseline marshal pass), and no
	// advisory lock is needed — the per-row claim already grants exclusive
	// ownership, so lockKey 0 disables the save lock.
	out, err := e.store.UpdateCollectionsWriteAll(ctx,
		[]store.LoadSpec{{Collection: "notifications", Filters: map[string]any{"id": ntfIDs}}},
		e.withAnalyticsOutbox([]string{"notifications", "notification_delivery_attempts"}),
		[]string{"notifications"},
		func(state *store.State) (any, error) {
			var processed []map[string]any
			for _, r := range results {
				ntfID := model.WrapNotification(r.ntf).ID()
				cur := notificationMap(state.Notifications[ntfID])
				if cur == nil {
					cur = r.ntf
				}
				n := model.WrapNotification(cur)
				if !n.IsDelivering() {
					continue
				}
				if r.shortCircuited {
					n.ReleaseClaim(model.NotificationDeliveryScheduled)
					state.Notifications[ntfID] = n
					continue
				}
				state.NotificationDeliveryAttempts[r.attempt["id"].(string)] = r.attempt
				e.applyOutcome(n, r.outcome, r.finishedAt)
				// After applyOutcome: the event carries the status the
				// notification ended up in, which is what distinguishes a
				// failure that will be retried from one that has given up.
				e.emitDeliveryAttempt(state, n, r.attempt, r.outcome, r.finishedAt)
				state.Notifications[ntfID] = n
				processed = append(processed, n.Raw())
			}
			return processed, nil
		}, 0)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	processed := out.([]map[string]any)
	e.fireDeadLetters(ctx, processed)
	return processed, nil
}

// deliveryAttemptRow records one provider call: what was tried, what came back
// (status, provider code, redacted excerpt) and how long it took.
func deliveryAttemptRow(n model.Notification, attempt int, res deliveryOutcome, startedAt, finishedAt string, elapsed time.Duration) map[string]any {
	row := map[string]any{
		"id":                utils.MakeID("dlat"),
		"notification_id":   n.ID(),
		"channel":           n.Channel(),
		"target":            n.Target(),
		"attempt":           attempt,
		"status":            res.Status,
		"provider_status":   nilIfEmpty(res.ProviderStatus),
		"provider_response": res.Response,
		"error":             nilIfEmpty(res.Err),
		"duration_ms":       elapsed.Milliseconds(),
		"started_at":        startedAt,
		"finished_at":       finishedAt,
		"created_at":        startedAt,
		"updated_at":        finishedAt,
	}
	if res.Code > 0 {
		row["provider_code"] = res.Code
	}
	return row
}

// applyOutcome moves a claimed notification to its terminal or retry state.
//
// A skip is terminal on purpose: the channel has no transport on this
// deployment, so a retry would fail identically four times and then dead-letter
// something that never failed.
func (e *Engine) applyOutcome(n model.Notification, res deliveryOutcome, ts string) {
	switch res.Status {
	case deliveryDelivered:
		n.MarkDelivered(ts, strDefault(res.ProviderStatus, "delivered"))
	case deliverySkipped:
		n.MarkSkipped(ts, strDefault(res.ProviderStatus, skipNotConfigured), res.Err)
		e.sink().IncNotificationSkipped(n.Channel(), strDefault(res.ProviderStatus, skipNotConfigured))
		slog.Warn("notification_skipped",
			"notification_id", n.ID(), "channel", n.Channel(),
			"reason", res.ProviderStatus, "detail", res.Err)
	default:
		n.ScheduleRetryOrFailAfter(ts, res.Err, e.deliveryCfg.MaxRetries, e.deliveryCfg.RetryDelays, res.RetryAfter)
	}
}

// fireDeadLetters sends dead-letter events for notifications that just became
// permanently failed. Runs after the saving transaction, outside any lock.
func (e *Engine) fireDeadLetters(ctx context.Context, notifs []map[string]any) {
	var failed []model.Notification
	for _, n := range notifs {
		nw := model.WrapNotification(n)
		if nw.Status() != model.NotificationFailed {
			continue
		}
		failed = append(failed, nw)
		// Count the dead-letter regardless of whether a webhook is configured —
		// permanent failure is the operationally interesting signal.
		e.sink().IncDeadLetter(nw.Channel())
		if e.deliveryCfg.DeadLetterWebhookURL != "" {
			go e.sendDeadLetterEvent(context.WithoutCancel(ctx), deepCopyItem(n))
		}
	}
	if err := e.recordDeliveryFailures(ctx, failed); err != nil {
		slog.Warn("record_delivery_failures_failed", "error", err)
	}
}

// recordDeliveryFailures writes a permanent delivery failure into the history
// of the group it was about. The group's timeline said "Notified users" and
// "Escalation chain completed" while the person was never reached; the failure
// was visible only on the notification row.
func (e *Engine) recordDeliveryFailures(ctx context.Context, failed []model.Notification) error {
	byGroup := map[string][]model.Notification{}
	var ids []string
	for _, n := range failed {
		gid := n.AlertGroupID()
		if gid == "" {
			continue
		}
		if _, seen := byGroup[gid]; !seen {
			ids = append(ids, gid)
		}
		byGroup[gid] = append(byGroup[gid], n)
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := e.store.UpdateCollectionsFiltered(ctx, loadGroups(ids...), []string{"alert_groups"},
		func(state *store.State) (any, error) {
			for gid, notifs := range byGroup {
				g, ok := groupAG(state.AlertGroups[gid])
				if !ok {
					continue
				}
				for _, n := range notifs {
					recipient := n.UserID()
					if user, _ := n.Payload()["user"].(map[string]any); utils.StrVal(user, "username") != "" {
						recipient = utils.StrVal(user, "username")
					}
					if recipient == "" {
						recipient = n.Target()
					}
					g.AppendLog("delivery_failed",
						fmt.Sprintf("Notification to %s via %s failed permanently: %s", recipient, n.Channel(), n.LastError()),
						map[string]any{"notification_id": n.ID(), "channel": n.Channel(), "user_id": n.UserID()})
				}
				state.AlertGroups[gid] = g
			}
			return nil, nil
		}, advisoryLock["record_delivery_failures"])
	return err
}

func (e *Engine) ProcessNotificationRetries(ctx context.Context) ([]map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	// Atomically claim due retries (retry_scheduled → 'retrying') so each is
	// retried by exactly one worker.
	dueNotifs, err := e.store.ClaimRetryableNotifications(ctx, e.workerID, ts)
	if err != nil {
		return nil, err
	}
	if len(dueNotifs) == 0 {
		return nil, nil
	}

	// Rebuild missing payloads outside lock, fetching only the groups and
	// users the due retries actually reference.
	var needsContext []map[string]any
	for _, n := range dueNotifs {
		if n["payload"] == nil {
			needsContext = append(needsContext, n)
		}
	}
	ctx2Map := map[string]map[string]any{}
	if len(needsContext) > 0 {
		groupIDSet := map[string]bool{}
		userIDSet := map[string]bool{}
		for _, n := range needsContext {
			nw := model.WrapNotification(n)
			if id := nw.AlertGroupID(); id != "" {
				groupIDSet[id] = true
			}
			if id := nw.UserID(); id != "" {
				userIDSet[id] = true
			}
		}
		groups, _ := e.store.ListItemsByIDs(ctx, "alert_groups", setKeys(groupIDSet))
		for _, g := range groups {
			ctx2Map["ag:"+model.WrapAlertGroup(g).ID()] = g
		}
		users, _ := e.store.ListItemsByIDs(ctx, "users", setKeys(userIDSet))
		for _, u := range users {
			ctx2Map["usr:"+utils.StrVal(u, "id")] = u
		}
	}

	type retryResult struct {
		ntf            map[string]any
		attempt        map[string]any
		outcome        deliveryOutcome
		finishedAt     string
		failedContext  bool
		shortCircuited bool
	}
	// ctx2Map is read-only from here on, so the retry deliveries run concurrently
	// (bounded), like ProcessNotificationDeliveries. Rows are already claimed
	// ('retrying'), so this loop owns them exclusively.
	results := runBounded(dueNotifs, e.deliveryCfg.DeliveryConcurrency, func(ntf map[string]any) (res retryResult, ok bool) {
		// As in ProcessNotificationDeliveries: a panic is dropped (ok=false) so the
		// row stays claimed ('retrying') and the reaper re-delivers it later.
		defer func() {
			if rec := recover(); rec != nil {
				nw := model.WrapNotification(ntf)
				slog.Error("retry_task_panicked", "stage", "retries", "notification_id", nw.ID(), "channel", nw.Channel(), "panic", rec)
				e.sink().IncWorkerPanic("retries")
				res, ok = retryResult{}, false
			}
		}()
		ntfCopy := deepCopyItem(ntf)
		nc := model.WrapNotification(ntfCopy)
		payload := nc.Payload()
		if payload == nil {
			group := ctx2Map["ag:"+nc.AlertGroupID()]
			user := ctx2Map["usr:"+nc.UserID()]
			if group != nil && user != nil {
				payload = notificationPayload(model.WrapAlertGroup(group), user, strDefault(nc.Reason(), "notification retry"))
				ntfCopy["payload"] = payload
			}
			ch := nc.Channel()
			if group == nil || (payload == nil && (ch == "webhook" || ch == "slack" || ch == "mattermost")) {
				return retryResult{ntf: ntfCopy, failedContext: true}, true
			}
		}

		breakerKey := nc.Channel() + ":" + nc.Target()
		if !e.breaker.Allow(breakerKey, utils.UTCNow()) {
			// Open breaker: release the claim back to retry_scheduled (next_retry_at
			// stays due) so it is re-evaluated next cycle when the breaker half-opens.
			e.sink().IncDeliveryShortCircuited(nc.Channel())
			return retryResult{ntf: ntfCopy, shortCircuited: true}, true
		}
		attemptNum := nc.RetryCount() + 1
		tStart := utils.ToISO(utils.UTCNow())
		callStart := time.Now()
		outcome := e.deliverNotificationViaAdapter(ctx, ntfCopy)
		elapsed := time.Since(callStart)
		e.sink().ObserveDeliveryLatency(nc.Channel(), elapsed.Seconds())
		if outcome.Status != deliverySkipped {
			e.breaker.Record(breakerKey, outcome.Status == deliveryDelivered, utils.UTCNow())
		}
		tEnd := utils.ToISO(utils.UTCNow())
		attempt := deliveryAttemptRow(nc, attemptNum, outcome, tStart, tEnd, elapsed)
		return retryResult{ntf: ntfCopy, attempt: attempt, outcome: outcome, finishedAt: tEnd}, true
	})

	if len(results) == 0 {
		return nil, nil
	}

	retryIDs := make([]any, 0, len(results))
	for _, r := range results {
		retryIDs = append(retryIDs, model.WrapNotification(r.ntf).ID())
	}

	// As in ProcessNotificationDeliveries: attempts are append-only, save-only,
	// notifications are loaded by id and every claimed row is rewritten (writeAll
	// skips the baseline), and the per-row claim makes the save advisory lock
	// redundant (lockKey 0).
	out, err := e.store.UpdateCollectionsWriteAll(ctx,
		[]store.LoadSpec{{Collection: "notifications", Filters: map[string]any{"id": retryIDs}}},
		e.withAnalyticsOutbox([]string{"notifications", "notification_delivery_attempts"}),
		[]string{"notifications"},
		func(state *store.State) (any, error) {
			var retried []map[string]any
			for _, r := range results {
				ntfID := model.WrapNotification(r.ntf).ID()
				cur := notificationMap(state.Notifications[ntfID])
				if cur == nil {
					cur = r.ntf
				}
				n := model.WrapNotification(cur)
				if !n.IsRetrying() {
					continue
				}
				if r.shortCircuited {
					n.ReleaseClaim(model.NotificationRetryScheduled)
					state.Notifications[ntfID] = n
					continue
				}
				if r.failedContext {
					n.FailRetryContext()
					state.Notifications[ntfID] = n
					retried = append(retried, n.Raw())
					continue
				}
				state.NotificationDeliveryAttempts[r.attempt["id"].(string)] = r.attempt
				e.applyOutcome(n, r.outcome, r.finishedAt)
				e.emitDeliveryAttempt(state, n, r.attempt, r.outcome, r.finishedAt)
				state.Notifications[ntfID] = n
				retried = append(retried, n.Raw())
			}
			return retried, nil
		}, 0)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	retried := out.([]map[string]any)
	e.fireDeadLetters(ctx, retried)
	return retried, nil
}
