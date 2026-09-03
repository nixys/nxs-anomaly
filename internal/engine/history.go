package engine

import (
	"context"
	"fmt"
	"sort"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func (e *Engine) GetHistory(ctx context.Context, filters map[string]any) (map[string]any, error) {
	sqlFilters := map[string]any{}
	if v := utils.StrVal(filters, "integration"); v != "" {
		sqlFilters["integration_id"] = v
	}
	if v := utils.StrVal(filters, "severity"); v != "" {
		sqlFilters["severity"] = v
	}
	if v := utils.StrVal(filters, "status"); v != "" {
		sqlFilters["status"] = v
	}
	if v := utils.StrVal(filters, "from"); v != "" {
		sqlFilters["from_at"] = v
	}
	if v := utils.StrVal(filters, "to"); v != "" {
		sqlFilters["to_at"] = v
	}
	if v := utils.StrVal(filters, "channel"); v != "" {
		sqlFilters["channel"] = v
	}
	if v := utils.StrVal(filters, "user"); v != "" {
		sqlFilters["user_id"] = v
	}
	if v := utils.StrVal(filters, "team"); v != "" {
		team, err := e.store.GetItem(ctx, "teams", v)
		if err == nil && team != nil {
			memberIDs := anyToStringSlice(team["member_ids"])
			anyIDs := make([]any, len(memberIDs))
			for i, id := range memberIDs {
				anyIDs[i] = id
			}
			sqlFilters["team_user_ids"] = anyIDs
		} else {
			sqlFilters["team_user_ids"] = []any{}
		}
	}

	// History is the widest read in the service: it returns alert groups with
	// their notifications and delivery attempts inlined, and a notification
	// carries its target — a Telegram id, a phone number, an address. Scoping
	// the groups scopes all of it, because everything below is fetched by the
	// ids this query returns.
	if actor := authz.FromContext(ctx); actor.TeamScoped {
		visible, err := e.visibleIntegrationIDs(ctx, actor)
		if err != nil {
			return nil, err
		}
		if wanted, ok := sqlFilters["integration_id"]; ok {
			// Intersect rather than widen: asking for one integration outside
			// your teams yields nothing, not everything.
			if !containsAny(visible, wanted) {
				return map[string]any{"count": 0, "total": 0, "items": []any{}}, nil
			}
		} else {
			sqlFilters["integration_ids"] = visible
		}
	}

	limit := clampInt(intFromAny(filters["limit"], 50), 1, 200)
	offset := maxInt(intFromAny(filters["offset"], 0), 0)

	groups, total, err := e.store.QueryHistoryGroups(ctx, sqlFilters, limit, offset)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return map[string]any{"count": 0, "total": 0, "items": []any{}}, nil
	}

	groupIDs := make([]any, len(groups))
	for i, g := range groups {
		groupIDs[i] = utils.StrVal(g, "id")
	}

	notifications, _ := e.store.ListItemsIn(ctx, "notifications", "alert_group_id", groupIDs)
	notifsByGroup := map[string][]map[string]any{}
	var notifIDs []any
	for _, n := range notifications {
		gid := utils.StrVal(n, "alert_group_id")
		notifsByGroup[gid] = append(notifsByGroup[gid], n)
		notifIDs = append(notifIDs, utils.StrVal(n, "id"))
	}

	attempts, _ := e.store.ListItemsIn(ctx, "notification_delivery_attempts", "notification_id", notifIDs)
	attemptsByNotif := map[string][]map[string]any{}
	for _, a := range attempts {
		nid := utils.StrVal(a, "notification_id")
		attemptsByNotif[nid] = append(attemptsByNotif[nid], a)
	}

	batches, _ := e.store.ListItemsIn(ctx, "notification_batches", "alert_group_id", groupIDs)
	batchesByGroup := map[string][]map[string]any{}
	for _, b := range batches {
		gid := utils.StrVal(b, "alert_group_id")
		batchesByGroup[gid] = append(batchesByGroup[gid], b)
	}

	var allAlertIDs []any
	for _, group := range groups {
		for _, aid := range model.WrapAlertGroup(group).AlertIDs() {
			allAlertIDs = append(allAlertIDs, aid)
		}
	}
	alertsByID := map[string]map[string]any{}
	if len(allAlertIDs) > 0 {
		alertList, _ := e.store.ListItemsIn(ctx, "alerts", "id", allAlertIDs)
		for _, a := range alertList {
			alertsByID[utils.StrVal(a, "id")] = a
		}
	}

	items := []map[string]any{}
	for _, group := range groups {
		g := model.WrapAlertGroup(group)
		gid := g.ID()
		groupNotifs := notifsByGroup[gid]
		var groupAttempts []map[string]any
		for _, n := range groupNotifs {
			groupAttempts = append(groupAttempts, attemptsByNotif[utils.StrVal(n, "id")]...)
		}
		var alerts []map[string]any
		for _, aid := range g.AlertIDs() {
			if a, ok := alertsByID[aid]; ok {
				alerts = append(alerts, a)
			}
		}
		items = append(items, map[string]any{
			"alert_group":       group,
			"alerts":            alerts,
			"notifications":     groupNotifs,
			"delivery_attempts": groupAttempts,
			"batches":           batchesByGroup[gid],
			"timeline":          g.Logs(),
		})
	}
	return map[string]any{"count": len(items), "total": total, "items": items}, nil
}

// GetDeliveryAttempts returns delivery attempts, optionally filtered by notification_id.
func (e *Engine) GetDeliveryAttempts(ctx context.Context, notificationID string) (map[string]any, error) {
	// Delivery attempts have no team of their own; they inherit the notification's.
	// A scoped caller must therefore name a notification: the unfiltered feed
	// would be every attempt in the deployment, which is exactly the leak this
	// check exists to close.
	if authz.FromContext(ctx).TeamScoped {
		if notificationID == "" {
			return nil, errValidation("notification_id is required")
		}
		notification, err := e.store.GetItem(ctx, "notifications", notificationID)
		if err != nil {
			return nil, err
		}
		if notification == nil {
			return nil, errNotFound(fmt.Sprintf("notification %s not found", notificationID))
		}
		if err := e.authorizeItem(ctx, "notifications", notification); err != nil {
			return nil, err
		}
	}
	filters := map[string]any{}
	if notificationID != "" {
		filters["notification_id"] = notificationID
	}
	items, total, err := e.store.ListCollectionPage(ctx, "notification_delivery_attempts", filters, 1000, 0, store.SortSpec{})
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"items": items, "total": total}, nil
}

// DebugRoute simulates route selection and notification preview without ingesting.
func (e *Engine) DebugRoute(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	integration, err := e.store.FindIntegrationByKey(ctx, integrationKey)
	if err != nil {
		return nil, err
	}
	if integration == nil {
		return nil, errNotFound("integration key not found")
	}
	// Same order as ingest: the route is decided from what the source sent, then
	// the pipeline runs. Previewing them the other way round would show a route
	// the real alert never takes, which is worse than no preview.
	route, err := selectRoute(integration, payload)
	if err != nil {
		return nil, err
	}

	before, _ := utils.CoerceLabelMap(payload["labels"])
	beforeTitle := utils.StrVal(payload, "title")
	dropped, perr := e.applyAlertPipeline(integration, payload)
	if perr != nil {
		return nil, perr
	}
	if dropped {
		// The route is still reported: it is what the alert would have matched,
		// and seeing it next to the drop is how someone checks that the rule
		// silenced what they meant it to.
		return map[string]any{
			"result": "dropped_by_pipeline",
			"route":  route,
			"pipeline": map[string]any{
				"labels_before": strMapAny(before),
				"title_before":  beforeTitle,
			},
		}, nil
	}
	labels, _ := utils.CoerceLabelMap(payload["labels"])
	title := pickFirst([]string{
		utils.StrVal(payload, "title"),
		labels["alertname"],
		labels["summary"],
	}, "Incoming alert")
	dedupeKey := utils.StrVal(payload, "dedupe_key")
	if dedupeKey == "" {
		dedupeKey = buildDedupeKey(integration, labels, title)
	}
	// The preview only reads reference data — serve it from the short-TTL
	// cache instead of re-reading seven tables per debug request.
	state := store.NewState()
	for _, col := range []string{"users", "teams", "schedules", "escalation_chains", "integrations", "chatops_channels", "mobile_devices"} {
		items, err := e.refCollection(ctx, col)
		if err != nil {
			return nil, err
		}
		state.SetCollection(col, items)
	}
	previewGroup := map[string]any{
		"id":                    "debug",
		"integration_id":        utils.StrVal(integration, "id"),
		"route_id":              utils.StrVal(route, "id"),
		"escalation_chain_id":   utils.StrVal(route, "escalation_chain_id"),
		"dedupe_key":            dedupeKey,
		"title":                 title,
		"severity":              pickFirst([]string{utils.StrVal(payload, "severity"), labels["severity"]}, "unknown"),
		"labels":                labelsAny(labels),
		"notification_channels": toAnySlice(anyToStringSlice(payload["notification_channels"])),
	}
	ntfPayload := map[string]any{
		"alert_group_id": "debug",
		"title":          title,
		"severity":       previewGroup["severity"],
		"reason":         "debug preview",
	}
	fakeNotif := map[string]any{"alert_group_id": "debug", "reason": "debug preview"}
	return map[string]any{
		"integration":          integration,
		"route":                route,
		"dedupe_key":           dedupeKey,
		"pipeline":             pipelinePreview(before, beforeTitle, labels, title),
		"notification_preview": e.notificationDebugPreview(state, previewGroup),
		"message_preview":      renderNotificationText(fakeNotif, ntfPayload, ""),
	}, nil
}

func (e *Engine) notificationDebugPreview(state *store.State, group map[string]any) []map[string]any {
	g := model.WrapAlertGroup(group)
	chainID := g.EscalationChainID()
	if chainID == "" {
		return nil
	}
	chain := state.EscalationChains[chainID]
	if chain == nil {
		return nil
	}
	now := utils.UTCNow()
	var previews []map[string]any
	steps, _ := chain["steps"].([]any)
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var recipients []string
		switch step["kind"] {
		case StepNotifyUser:
			recipients = anyToStringSlice(step["user_ids"])
		case StepNotifyTeam:
			team := state.Teams[utils.StrVal(step, "team_id")]
			if team != nil {
				recipients = anyToStringSlice(team["member_ids"])
			}
		case StepNotifySchedule:
			sched := state.Schedules[utils.StrVal(step, "schedule_id")]
			recipients = e.getScheduleUserIDs(sched, now)
		case StepNotifyEmergency:
			recipients = e.emergencyUserIDs(state, g, utils.StrVal(step, "user_id"))
		default:
			continue
		}
		for _, uid := range recipients {
			user := state.Users[uid]
			if user != nil {
				previews = append(previews, map[string]any{
					"step":     step["kind"],
					"user_id":  uid,
					"username": utils.StrVal(user, "username"),
					"targets":  e.notificationTargetsForUser(state, g, user),
				})
			}
		}
	}
	return previews
}

// pipelinePreview reports what the ingest pipeline changed, so a rule can be
// checked before it is trusted with routing and grouping.
//
// It states the before and after rather than a diff: the reader is deciding
// whether the rule is right, and "namespace: prod" arriving from nowhere is
// easier to judge next to the labels the source actually sent.
func pipelinePreview(beforeLabels map[string]string, beforeTitle string,
	afterLabels map[string]string, afterTitle string) map[string]any {
	added := map[string]any{}
	changed := map[string]any{}
	for k, v := range afterLabels {
		old, existed := beforeLabels[k]
		switch {
		case !existed:
			added[k] = v
		case old != v:
			changed[k] = map[string]any{"from": old, "to": v}
		}
	}
	removed := []string{}
	for k := range beforeLabels {
		if _, still := afterLabels[k]; !still {
			removed = append(removed, k)
		}
	}
	sort.Strings(removed)
	return map[string]any{
		"labels_before":  strMapAny(beforeLabels),
		"labels_after":   strMapAny(afterLabels),
		"labels_added":   added,
		"labels_changed": changed,
		"labels_removed": removed,
		"title_before":   beforeTitle,
		"title_after":    afterTitle,
	}
}
