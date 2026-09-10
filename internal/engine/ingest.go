package engine

import (
	"context"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/tracing"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// CreateDirectPageGroup creates an on-demand alert group for direct paging.
// If alert_group_id is already provided, returns it unchanged.
//
// next_run_at is intentionally nil: direct-paging groups have no escalation
// chain and exist for manual operator action only. Because the worker filters
// "next_run_at IS NOT NULL", these groups are never picked up — that's why
// this method does not call wakeWorker. If a future change attaches an
// escalation_chain_id or sets a non-nil next_run_at here, add wakeWorker(ctx)
// after the upsert so the first escalation fires within the LISTEN/NOTIFY
// latency budget rather than waiting up to one poll interval.
func (e *Engine) CreateDirectPageGroup(ctx context.Context, payload map[string]any) (string, error) {
	if existingID := utils.StrVal(payload, "alert_group_id"); existingID != "" {
		return existingID, nil
	}
	ts := utils.ToISO(utils.UTCNow())
	group := map[string]any{
		"id":               utils.MakeID("ag"),
		"integration_id":   nil,
		"integration_type": "direct_paging",
		"integration_name": "Direct Paging",
		"route_id":         "",
		"dedupe_key":       utils.MakeID("dpk"),
		"status":           "open",
		"title":            strDefault(utils.StrVal(payload, "message"), "Direct paging alert"),
		"description":      utils.StrVal(payload, "message"),
		"severity":         "unknown",
		"labels":           map[string]any{},
		"alert_count":      1,
		"next_run_at":      nil,
		"alert_ids":        []any{},
		"current_step":     0,
		"repeat_count":     0,
		"logs":             []any{},
		"created_at":       ts,
		"updated_at":       ts,
		"last_received_at": ts,
	}
	if err := e.store.UpsertItem(ctx, "alert_groups", group); err != nil {
		return "", err
	}
	return group["id"].(string), nil
}

// preparedAlert carries the pre-lock work for one alert: route selection and
// normalized fields. It deliberately carries no alert group: a group read
// before the advisory lock is a snapshot that a concurrent ingest may already
// have superseded, and acting on it loses that ingest's updates. The group is
// resolved under the lock, from the state the mutator loaded.
type preparedAlert struct {
	payload          map[string]any
	route            map[string]any
	labels           map[string]string
	status           string
	title            string
	severity         string
	dedupeKey        string
	filteredChannels []string
	emergencyAlert   bool
	// traceParent carries the ingesting request's span context down to group
	// creation, which happens under the advisory lock in a function that has no
	// context.Context of its own.
	traceParent string
}

// prepareAlert does the per-alert work that can run outside the advisory lock.
func (e *Engine) prepareAlert(ctx context.Context, integration, payload map[string]any) (*preparedAlert, error) {
	// Routing is decided from what the source sent, before the pipeline runs.
	//
	// The alternative — enriching first — would let a rule change which chain
	// pages, and that turns every enrichment into a routing change nobody asked
	// for: adding a `team` label for a dashboard would silently repoint the
	// escalation. Which rota is woken has to follow from the alert as it
	// arrived, so that reading the integration's routes tells the whole story.
	//
	// Everything downstream does see the enriched alert: the dedupe key and
	// therefore grouping, the stored alert, and the notification text. That is
	// deliberate and is where the useful work is — cutting a build id out of a
	// title so repeated failures collapse into one incident, adding the pod and
	// namespace a source failed to state.
	route, err := selectRoute(integration, payload)
	if err != nil {
		return nil, err
	}

	if dropped, err := e.applyAlertPipeline(integration, payload); err != nil {
		return nil, err
	} else if dropped {
		return nil, nil
	}

	labels, _ := utils.CoerceLabelMap(payload["labels"])
	status := strings.ToLower(strDefault(
		utils.StrVal(payload, "status"),
		strDefault(labels["status"], "firing"),
	))

	labelsStr := utils.StrVal(payload, "title")
	title := pickFirst([]string{
		labelsStr,
		labels["alertname"],
		labels["summary"],
	}, "Incoming alert")

	severity := pickFirst([]string{
		utils.StrVal(payload, "severity"),
		labels["severity"],
	}, "unknown")

	dedupeKey := utils.StrVal(payload, "dedupe_key")
	if dedupeKey == "" {
		dedupeKey = buildDedupeKey(integration, labels, title)
	}

	notifChannels, _ := utils.CoerceStringList(payload["notification_channels"])
	var filteredChannels []string
	for _, ch := range notifChannels {
		if supportedNotificationTargets[ch] {
			filteredChannels = append(filteredChannels, ch)
		}
	}
	emergencyAlert := utils.BoolVal(payload, "emergency_alert", false)
	if labels["emergency"] == "true" {
		emergencyAlert = true
	}

	return &preparedAlert{
		payload:          payload,
		route:            route,
		labels:           labels,
		status:           status,
		title:            title,
		severity:         severity,
		dedupeKey:        dedupeKey,
		filteredChannels: filteredChannels,
		emergencyAlert:   emergencyAlert,
		traceParent:      tracing.Traceparent(ctx),
	}, nil
}

// IngestAlert processes a raw webhook alert payload for the given integration key.
// IngestAlert is the head of the chain a trace follows: this span is the parent
// of the grouping work, and — through the alert group id attribute — the thing
// that ties an ingest to the escalation and delivery spans a worker produces
// minutes later in another process.
func (e *Engine) IngestAlert(ctx context.Context, integrationKey string, payload map[string]any) (result map[string]any, err error) {
	ctx, span := tracing.Start(ctx, "ingest.alert",
		tracing.Source(utils.StrVal(payload, "source")),
	)
	defer func() {
		if result != nil {
			span.SetAttributes(tracing.AlertGroupID(utils.StrVal(result, "alert_group_id")))
		}
		tracing.RecordError(span, err)
		span.End()
	}()
	return e.ingestAlert(ctx, integrationKey, payload)
}

func (e *Engine) ingestAlert(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	integration, err := e.store.FindIntegrationByKey(ctx, integrationKey)
	if err != nil {
		return nil, err
	}
	if integration == nil {
		return nil, errNotFound("integration key not found")
	}
	prep, err := e.prepareAlert(ctx, integration, payload)
	if err != nil {
		return nil, err
	}
	if prep == nil {
		// A pipeline drop. Answered as a success because it is one: the sender
		// delivered the alert and the receiver decided, by configuration, not to
		// act on it. Saying so in the result keeps the decision visible instead
		// of looking like an alert that vanished.
		return map[string]any{"alert": nil, "group": nil, "result": "dropped_by_pipeline"}, nil
	}
	results, err := e.ingestPrepared(ctx, integration, []*preparedAlert{prep})
	if err != nil {
		return nil, err
	}
	return results[0], nil
}

// ingestPrepared ingests all prepared alerts of one integration in a single
// locked transaction: one alert for plain webhooks, N alerts for an
// Alertmanager envelope (previously N transactions acquiring the same
// per-integration lock N times).
func (e *Engine) ingestPrepared(ctx context.Context, integration map[string]any, preps []*preparedAlert) ([]map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	integrationID := utils.StrVal(integration, "id")

	// All six collections below are read-only inside the mutator — fetch them via
	// the short-TTL reference cache outside the advisory lock, so alert bursts
	// (e.g. one Alertmanager envelope with N alerts) don't reload them per alert.
	// Only alert_groups and notification_batches stay inside the lock because
	// they carry mutable state that must be re-read under the lock.
	// A failure here aborts the ingest: routing an alert against half-loaded
	// reference data would silently page nobody. See refSet.
	refs := e.newRefSet(ctx)
	chatopsMap := refs.get("chatops_channels")
	mobileDevicesMap := refs.get("mobile_devices")
	usersMap := refs.get("users")
	teamsMap := refs.get("teams")
	schedsMap := refs.get("schedules")
	chainsMap := refs.get("escalation_chains")
	// Windows are read here, with the other reference data, because they are
	// consulted inside the advisory lock where a database read is not allowed.
	windowsMap := refs.get("maintenance_windows")
	if err := refs.Err(); err != nil {
		return nil, err
	}
	maintenance := activeMaintenance(windowsMap, integrationID, utils.UTCNow())
	// Build single-entry integrations map from the already-loaded integration object.
	integsMap := map[string]map[string]any{integrationID: integration}

	// Per-integration advisory lock: concurrent ingests on the same integration
	// serialize while ingests on different integrations run in parallel.
	lockKey := ingestLockKey(integrationID)

	// Only the rows this envelope can actually match are loaded, keyed by the
	// dedupe keys it carries.
	//
	// Loading every unresolved group of the integration made ingest cost grow
	// with the integration's open-group count: measured 16.6ms at 50 open
	// groups, 148.1ms at 800, a straight line of ~0.174ms per group, which
	// makes a burst of n alerts cost O(n²). Closing the groups and re-measuring
	// the same integration took it back to 17.0ms, so the load was the cause
	// rather than a correlate. It degrades exactly when it hurts most: during
	// an incident, when open groups are many and alerts arrive fastest.
	//
	// The narrower filter is not a different question, only a cheaper way to
	// ask the same one. The in-mutator re-scan looks for a group matching this
	// integration, one of these dedupe keys and a non-resolved status, and
	// attachNotificationBatch looks for an open batch whose batch_key is
	// integration + dedupe key — nothing else reads these two collections here.
	// Groups created earlier in this same envelope live in state.AlertGroups
	// regardless of what was loaded, and a concurrent ingest cannot be inside
	// the advisory lock we already hold, so the re-scan still sees everything
	// it has to see.
	//
	// nxs_anomaly_alert_groups_active_lookup_idx, on
	// (integration_id, dedupe_key) WHERE status <> 'resolved', already serves
	// this shape — migration 0002 created it for the pre-lock lookup, and this
	// path simply stopped ignoring it. No new migration.
	dedupeKeys := make([]any, 0, len(preps))
	batchKeys := make([]any, 0, len(preps))
	seen := make(map[string]bool, len(preps))
	for _, p := range preps {
		if seen[p.dedupeKey] {
			continue
		}
		seen[p.dedupeKey] = true
		dedupeKeys = append(dedupeKeys, p.dedupeKey)
		batchKeys = append(batchKeys, integrationID+":"+p.dedupeKey)
	}
	loads := []store.LoadSpec{
		{Collection: "alert_groups", Filters: map[string]any{
			"integration_id": integrationID,
			"dedupe_key":     dedupeKeys,
			"status":         store.NotEqualFilter{Value: "resolved"},
		}},
		{Collection: "notification_batches", Filters: map[string]any{
			"integration_id": integrationID,
			"batch_key":      batchKeys,
			"status":         "open",
		}},
	}
	saveCols := []string{
		"alerts", "alert_groups", "notifications", "chatops_messages", "notification_batches",
		"notification_policy_runs",
	}
	if e.kafkaProducer != nil {
		saveCols = append(saveCols, "kafka_outbox")
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loads, saveCols,
		func(state *store.State) (any, error) {
			// Inject pre-fetched read-only collections into state.
			state.ChatopsChannels = chatopsMap
			state.MobileDevices = mobileDevicesMap
			state.Users = usersMap
			state.Teams = teamsMap
			state.Schedules = schedsMap
			state.EscalationChains = chainsMap
			state.Integrations = integsMap

			addOutbox := e.outboxAppender(state, integration, ts)

			results := make([]map[string]any, 0, len(preps))
			for _, p := range preps {
				results = append(results, e.ingestOneLocked(state, integration, p, ts, maintenance, addOutbox))
			}
			return results, nil
		}, lockKey)

	if err != nil {
		return nil, err
	}
	results := result.([]map[string]any)
	// A group left resolved by this ingest takes its alerts with it, and the
	// group's status is the thing to read — not the per-alert result. A
	// resolving event from the source reports "resolved", but an escalation
	// chain that reached a RESOLVE step during the ingest reports "ingested"
	// while closing the group all the same, and its alerts stayed firing on a
	// group nobody would touch again. Ids are de-duplicated because several
	// alerts of one envelope can land on the same group.
	var closed []string
	seenClosed := make(map[string]bool, len(results))
	for _, r := range results {
		g, ok := r["group"].(map[string]any)
		if !ok {
			continue
		}
		if utils.StrVal(g, "status") != model.StatusResolved {
			continue
		}
		id := utils.StrVal(g, "id")
		if id == "" || seenClosed[id] {
			continue
		}
		seenClosed[id] = true
		closed = append(closed, id)
	}
	e.syncAlertStatusForGroups(ctx, closed, model.AlertStatusResolved)
	// Wake worker loops immediately: the ingest may have scheduled
	// notifications or escalation steps.
	e.wakeWorker(ctx)
	return results, nil
}

// ingestOneLocked applies one prepared alert to the locked state and returns
// the per-alert ingest result. Must run inside the ingest advisory lock.
func (e *Engine) ingestOneLocked(state *store.State, integration map[string]any, p *preparedAlert, ts string, maintenance map[string]any, addOutbox func(alert map[string]any, g model.AlertGroup, hasGroup bool)) map[string]any {
	alert := map[string]any{
		"id":             utils.MakeID("alr"),
		"integration_id": utils.StrVal(integration, "id"),
		"route_id":       utils.StrVal(p.route, "id"),
		"status":         p.status,
		"title":          p.title,
		"message":        utils.StrVal(p.payload, "message"),
		"severity":       p.severity,
		"labels":         labelsAny(p.labels),
		"payload":        p.payload,
		"annotations":    annotationsAny(p.payload["annotations"]),
		"fingerprint":    utils.StrVal(p.payload, "fingerprint"),
		"starts_at":      nilIfEmpty(utils.StrVal(p.payload, "starts_at")),
		"ends_at":        nilIfEmpty(utils.StrVal(p.payload, "ends_at")),
		"generator_url":  nilIfEmpty(utils.StrVal(p.payload, "generator_url")),
		"source":         strDefault(utils.StrVal(p.payload, "source"), strDefault(utils.StrVal(integration, "source_type"), "webhook")),
		"received_at":    ts,
	}
	state.Alerts[alert["id"].(string)] = model.WrapAlert(alert)

	// The group is found here and only here, in the state the mutator loaded
	// under the advisory lock. A copy read before the lock would be a snapshot
	// of the row as it was before any concurrent ingest committed, and building
	// this alert's update on it silently discards that ingest's work: two
	// parallel alerts both read alert_count=N and both write N+1, so N events
	// leave a group counting fewer than N (the same loss applies to alert_ids
	// and to every state transition the other ingest made).
	//
	// The load spec asks for exactly the rows this scan looks for — this
	// integration, one of this envelope's dedupe keys, status <> resolved — so
	// a group that exists is here, and one that is absent either does not exist
	// or was resolved, in which case a firing event must open a new group
	// rather than revive a stale copy. Groups created earlier in this same
	// envelope are in state.AlertGroups too, and a concurrent ingest cannot be
	// inside the lock we hold.
	//
	// The scan runs for resolving statuses as well, so a resolve event finds a
	// group opened by a concurrent ingest or earlier in the same envelope.
	//
	// We work on a deep copy so the mutator's intermediate edits never leak
	// into other paths that may peek at state.AlertGroups before we write the
	// final version back.
	var g model.AlertGroup
	var hasGroup bool
	integID := utils.StrVal(integration, "id")
	for _, rec := range state.AlertGroups {
		cand, ok := groupAG(rec)
		if !ok {
			continue
		}
		if cand.IntegrationID() == integID && cand.DedupeKey() == p.dedupeKey && cand.Status() != model.StatusResolved {
			g = model.WrapAlertGroup(deepCopyItem(cand.Raw()))
			hasGroup = true
			break
		}
	}

	// Handle resolved events.
	if resolvedStatuses[p.status] {
		if !hasGroup {
			addOutbox(alert, model.AlertGroup{}, false)
			return map[string]any{
				"alert":  alert,
				"group":  nil,
				"result": "resolved_without_group",
			}
		}
		g.AddAlertID(alert["id"].(string))
		g.SetLastReceivedAt(ts)
		g.AppendLog("source_resolve", "Received resolving event from upstream source",
			map[string]any{"alert_id": alert["id"]})
		// An upstream "resolved" event has no principal behind it: the monitoring
		// system closed the alert, not a person. The system actor says exactly that.
		firstResolve := !g.IsResolved()
		g.Resolve(ts, "Resolved by source event", authz.SystemActor)
		if firstResolve {
			// resolution says who closed it. A source that stops firing and an
			// operator who clicked resolve are both "resolved", but only one of
			// them means somebody did something.
			e.emitGroupEvent(state, g, EventGroupResolved, ts, map[string]any{
				"actor_kind": authz.SystemActor.Kind,
				"resolution": "source",
			})
		}
		e.notifyGroupResolved(state, g, ts)
		state.AlertGroups[g.ID()] = g
		addOutbox(alert, g, true)
		return map[string]any{"alert": alert, "group": g.Raw(), "result": "resolved"}
	}

	// Firing event.
	// reopened records that this alert took an acknowledged group back to open,
	// which is the one case where an alert arriving on an existing group has to
	// restart its escalation chain.
	reopened := false
	if !hasGroup {
		g = model.NewAlertGroup(model.NewAlertGroupParams{
			IntegrationID:        utils.StrVal(integration, "id"),
			RouteID:              utils.StrVal(p.route, "id"),
			EscalationChainID:    utils.StrVal(p.route, "escalation_chain_id"),
			DedupeKey:            p.dedupeKey,
			Title:                p.title,
			Severity:             p.severity,
			Labels:               labelsAny(p.labels),
			NotificationChannels: p.filteredChannels,
			EmergencyAlert:       p.emergencyAlert,
			Timestamp:            ts,
			TraceParent:          p.traceParent,
		})
		state.AlertGroups[g.ID()] = g
		g.AppendLog("group_created", "Created alert group",
			map[string]any{"route_id": utils.StrVal(p.route, "id")})
		// Snapshotted now, while the integration is in hand: the lifecycle
		// transitions run in mutators that load only alert_groups, and reading
		// the integration there would be a nested query inside an advisory lock.
		g.SetAnalyticsContext(utils.StrVal(integration, "team_id"), utils.StrVal(integration, "kafka_topic"))
		e.emitGroupEvent(state, g, EventGroupOpened, ts, map[string]any{
			"severity": p.severity,
			"source":   utils.StrVal(alert, "source"),
			// When the source says the problem started. This is the only place
			// it can be captured for the episode: the transitions that follow
			// run in mutators that never see an alert. Empty for sources that
			// do not report it, and the detection lag is then absent rather
			// than zero — a source that says nothing must not look instant.
			"source_started_at": utils.StrVal(alert, "starts_at"),
		})
		// Silenced at birth if planned maintenance covers this integration: the
		// group and its alerts are recorded in full, but the worker's due-scan
		// excludes silenced groups, so nobody is paged for work we are doing on
		// purpose. Applied only to groups created during the window — one that
		// was already escalating belongs to whoever was woken for it.
		// No further guard is needed around escalation: advanceGroupLocked
		// returns immediately for a group that is not open, which is what makes
		// a window a rule over the existing silence rather than a second
		// suppression path to keep in step with the first.
		// Emitted here rather than inside silenceForMaintenance: the analytics
		// event belongs to the ingest transaction that created the group, and
		// without it an episode suppressed on purpose is indistinguishable in
		// ClickHouse from one nobody answered.
		if silenceForMaintenance(g, maintenance, utils.UTCNow(), ts) {
			e.emitGroupEvent(state, g, EventGroupSilenced, ts, map[string]any{
				"actor_kind": authz.SystemActor.Kind,
				"reason":     "maintenance",
				"window_id":  utils.StrVal(maintenance, "id"),
			})
		}
	} else {
		// An acknowledgement is an operator saying "I know about this, stop
		// paging me". A repeat firing of the same event is the source restating
		// what they acknowledged, so by default it does not undo the
		// acknowledgement: a monitoring system that re-sends every 30 seconds
		// would otherwise page a person who already answered, on every repeat,
		// forever. Deployments that want the opposite policy — where any new
		// alert on an acknowledged group resumes escalation — opt into it with
		// NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT=true.
		//
		// ReopenOnNewAlert starts a new episode when it returns true, so the
		// event carries the new one: the second time this group was answered is
		// a second response, not a continuation of the first.
		if e.reopenAckedOnNewAlert && g.ReopenOnNewAlert() {
			reopened = true
			e.emitGroupEvent(state, g, EventGroupReopened, ts, map[string]any{
				"reason":            "new_alert",
				"severity":          p.severity,
				"source_started_at": utils.StrVal(alert, "starts_at"),
			})
		}
		g.SetUpdatedAt(ts)
		g.SetSeverity(p.severity)
		g.SetTitle(p.title)
		if len(p.filteredChannels) > 0 {
			g.SetNotificationChannels(p.filteredChannels)
		}
		if p.emergencyAlert {
			g.MarkEmergency()
		}
		g.AppendLog("alert_attached", "Attached alert to existing alert group", nil)
	}

	g.AddAlertID(alert["id"].(string))
	alert["alert_group_id"] = g.ID()
	// Re-wrap: the alert was added to state.Alerts before its group was known,
	// and the typed Alert wrapper copies on Wrap, so the stored record must be
	// refreshed now that alert_group_id is set.
	state.Alerts[alert["id"].(string)] = model.WrapAlert(alert)
	g.IncAlertCount()
	g.SetLabels(labelsAny(p.labels))
	g.SetLastReceivedAt(ts)

	// Escalation is advanced only when this alert started a chain: a new group,
	// or a group this alert took back to open. A repeat firing on a group that
	// is already escalating must not execute the next step, because the chain's
	// position is a promise about time — a WAIT of ten minutes means the step
	// after it runs ten minutes later, not the moment the source re-sends. The
	// group is already scheduled (next_run_at) and the worker owns that clock;
	// calling advance here executed the step the WAIT was still counting down
	// to, which is how a chain ending in RESOLVE closed a group half a second
	// after it opened.
	//
	// A group whose chain has run out has no timer either, and re-advancing it
	// would only re-log "escalation chain completed" once per repeat.
	if !hasGroup || reopened {
		e.advanceGroupLocked(state, g, ts)
	}
	// The epic threshold pages a named person when a group turns into a storm.
	// A storm is what maintenance produces, so it is skipped for a group that a
	// window has silenced — including later alerts attaching to a group
	// silenced earlier in the same window, which is the shape a storm has.
	if maintenance == nil || g.Status() != model.StatusSilenced {
		e.checkEpicThreshold(state, g, integration, ts)
	}
	state.AlertGroups[g.ID()] = g
	addOutbox(alert, g, true)

	return map[string]any{"alert": alert, "group": g.Raw(), "result": "ingested"}
}

// ingestNormalizedBatch ingests already-normalized alerts of one integration in
// a single locked transaction, and answers per alert. Route selection and the
// pipeline run up front, so a batch that fails validation ingests nothing; a
// batch whose every alert was dropped by the pipeline opens no transaction and
// still answers, because the sender delivered them.
func (e *Engine) ingestNormalizedBatch(ctx context.Context, integrationKey string, normalized []map[string]any) ([]any, error) {
	integration, err := e.store.FindIntegrationByKey(ctx, integrationKey)
	if err != nil {
		return nil, err
	}
	if integration == nil {
		return nil, errNotFound("integration key not found")
	}
	preps := make([]*preparedAlert, 0, len(normalized))
	dropped := 0
	for _, alert := range normalized {
		prep, err := e.prepareAlert(ctx, integration, alert)
		if err != nil {
			return nil, err
		}
		if prep == nil {
			dropped++
			continue
		}
		preps = append(preps, prep)
	}
	var batchResults []map[string]any
	if len(preps) > 0 {
		batchResults, err = e.ingestPrepared(ctx, integration, preps)
		if err != nil {
			return nil, err
		}
	}
	results := make([]any, 0, len(batchResults)+dropped)
	for _, r := range batchResults {
		results = append(results, r)
	}
	for i := 0; i < dropped; i++ {
		results = append(results, map[string]any{"alert": nil, "group": nil, "result": "dropped_by_pipeline"})
	}
	return results, nil
}

// IngestAlertmanager processes an Alertmanager webhook envelope. All alerts in
// the envelope are ingested in one locked transaction; route selection and
// validation run up front, so an invalid envelope ingests nothing.
func (e *Engine) IngestAlertmanager(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	alerts, ok := payload["alerts"].([]any)
	if !ok || len(alerts) == 0 {
		return nil, errValidation("Alertmanager payload must contain non-empty alerts[]")
	}
	integration, err := e.store.FindIntegrationByKey(ctx, integrationKey)
	if err != nil {
		return nil, err
	}
	if integration == nil {
		return nil, errNotFound("integration key not found")
	}
	preps := make([]*preparedAlert, 0, len(alerts))
	dropped := 0
	for _, rawAlert := range alerts {
		alert, ok := rawAlert.(map[string]any)
		if !ok {
			return nil, errValidation("each Alertmanager alert must be an object")
		}
		normalized := normalizeAlertmanagerAlert(payload, alert)
		prep, err := e.prepareAlert(ctx, integration, normalized)
		if err != nil {
			return nil, err
		}
		if prep == nil {
			dropped++
			continue
		}
		preps = append(preps, prep)
	}
	// An envelope whose every alert was dropped must not open a transaction, and
	// must still answer: the sender needs a 202, not a 500 about an empty batch.
	var batchResults []map[string]any
	if len(preps) > 0 {
		batchResults, err = e.ingestPrepared(ctx, integration, preps)
		if err != nil {
			return nil, err
		}
	}
	results := make([]any, 0, len(batchResults)+dropped)
	for _, r := range batchResults {
		results = append(results, r)
	}
	for i := 0; i < dropped; i++ {
		results = append(results, map[string]any{"alert": nil, "group": nil, "result": "dropped_by_pipeline"})
	}
	return map[string]any{
		"receiver":     payload["receiver"],
		"status":       payload["status"],
		"group_key":    payload["groupKey"],
		"external_url": payload["externalURL"],
		"processed":    len(results),
		"results":      results,
	}, nil
}

func normalizeAlertmanagerAlert(envelope, alert map[string]any) map[string]any {
	labels, _ := utils.CoerceLabelMap(alert["labels"])
	annotations, _ := utils.CoerceLabelMap(alert["annotations"])
	groupLabels, _ := utils.CoerceLabelMap(envelope["groupLabels"])
	commonLabels, _ := utils.CoerceLabelMap(envelope["commonLabels"])
	commonAnnotations, _ := utils.CoerceLabelMap(envelope["commonAnnotations"])

	status := strings.ToLower(strDefault(
		utils.StrVal(alert, "status"),
		strDefault(utils.StrVal(envelope, "status"),
			strDefault(labels["status"], "firing")),
	))
	fingerprint := utils.StrVal(alert, "fingerprint")
	groupKey := utils.StrVal(envelope, "groupKey")

	title := pickFirst([]string{
		annotations["summary"],
		annotations["description"],
		labels["alertname"],
		utils.StrVal(envelope, "receiver"),
	}, "Alertmanager alert")

	dedupeKey := pickFirst([]string{
		fingerprint,
		groupKey,
		alertmanagerGroupLabelsKey(groupLabels),
	}, "")

	return map[string]any{
		"title":   title,
		"message": pickFirst([]string{annotations["description"], annotations["message"]}, title),
		"status":  status,
		"severity": pickFirst([]string{
			labels["severity"],
			utils.StrVal(envelope, "status"),
		}, "unknown"),
		"labels":        labelsAny(labels),
		"annotations":   strMapAny(annotations),
		"starts_at":     nilIfEmpty(utils.StrVal(alert, "startsAt")),
		"ends_at":       nilIfEmpty(utils.StrVal(alert, "endsAt")),
		"generator_url": nilIfEmpty(utils.StrVal(alert, "generatorURL")),
		"fingerprint":   fingerprint,
		"dedupe_key":    nilIfEmpty(dedupeKey),
		"source":        "alertmanager",
		"alertmanager": map[string]any{
			"receiver":           envelope["receiver"],
			"status":             envelope["status"],
			"group_labels":       strMapAny(groupLabels),
			"common_labels":      strMapAny(commonLabels),
			"common_annotations": strMapAny(commonAnnotations),
			"external_url":       envelope["externalURL"],
			"group_key":          groupKey,
			"truncated_alerts":   envelope["truncatedAlerts"],
		},
		"payload": alert,
	}
}

// IngestPagerDuty processes a PagerDuty Events v2 payload.
