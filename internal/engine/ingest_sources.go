package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

func (e *Engine) IngestPagerDuty(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errValidation("PagerDuty payload must be an object")
	}
	eventAction := strings.ToLower(strDefault(utils.StrVal(payload, "event_action"), "trigger"))
	dedupKey := utils.StrVal(payload, "dedup_key")

	if eventAction == "acknowledge" {
		integration, err := e.store.FindIntegrationByKey(ctx, integrationKey)
		if err != nil {
			return nil, err
		}
		if integration == nil {
			return nil, errNotFound("integration key not found")
		}
		if dedupKey != "" {
			active, err := e.store.FindActiveAlertGroup(ctx, utils.StrVal(integration, "id"), dedupKey)
			if err == nil && active != nil {
				if _, ackErr := e.AcknowledgeGroup(ctx, utils.StrVal(active, "id")); ackErr != nil {
					slog.Error("acknowledge_group_failed", "group_id", utils.StrVal(active, "id"), "error", ackErr)
				}
			}
		}
		return map[string]any{"status": "success", "message": "Event processed", "dedup_key": dedupKey}, nil
	}

	normalized := normalizePagerDutyAlert(payload)
	if _, err := e.IngestAlert(ctx, integrationKey, normalized); err != nil {
		return nil, err
	}
	resultKey := dedupKey
	if resultKey == "" {
		resultKey = utils.StrVal(normalized, "dedupe_key")
	}
	return map[string]any{"status": "success", "message": "Event processed", "dedup_key": resultKey}, nil
}

func normalizePagerDutyAlert(payload map[string]any) map[string]any {
	eventAction := strings.ToLower(strDefault(utils.StrVal(payload, "event_action"), "trigger"))
	pdPayload, _ := payload["payload"].(map[string]any)
	customDetails, _ := utils.CoerceLabelMap(pdPayload["custom_details"])

	labels := map[string]string{}
	for k, v := range customDetails {
		labels[k] = v
	}
	for _, field := range []string{"component", "group", "class"} {
		if v := utils.StrVal(pdPayload, field); v != "" {
			labels[field] = v
		}
	}
	if v := utils.StrVal(pdPayload, "source"); v != "" {
		labels["host"] = v
	}

	status := "firing"
	if eventAction == "resolve" {
		status = "resolved"
	}
	pdSeverity := strings.ToLower(strDefault(utils.StrVal(pdPayload, "severity"), "warning"))
	severity := pdSeverity
	if pdSeverity != "critical" && pdSeverity != "warning" && pdSeverity != "info" {
		severity = "warning"
	}
	title := pickFirst([]string{utils.StrVal(pdPayload, "summary"), utils.StrVal(payload, "client")}, "PagerDuty alert")
	dedupKey := utils.StrVal(payload, "dedup_key")

	links, _ := payload["links"].([]any)
	return map[string]any{
		"title":       title,
		"message":     strDefault(utils.StrVal(pdPayload, "summary"), title),
		"status":      status,
		"severity":    severity,
		"labels":      labelsAny(labels),
		"annotations": map[string]any{},
		"starts_at":   nilIfEmpty(utils.StrVal(pdPayload, "timestamp")),
		"fingerprint": dedupKey,
		"dedupe_key":  nilIfEmpty(dedupKey),
		"source":      "pagerduty",
		"pagerduty": map[string]any{
			"event_action": eventAction,
			"routing_key":  utils.StrVal(payload, "routing_key"),
			"dedup_key":    dedupKey,
			"links":        links,
			"client":       utils.StrVal(payload, "client"),
			"client_url":   utils.StrVal(payload, "client_url"),
		},
	}
}

// IngestVictorOps processes a VictorOps / Splunk On-Call payload.
func (e *Engine) IngestVictorOps(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errValidation("VictorOps payload must be an object")
	}
	messageType := strings.ToUpper(strDefault(utils.StrVal(payload, "message_type"), "CRITICAL"))
	entityID := utils.StrVal(payload, "entity_id")

	if messageType == "ACKNOWLEDGEMENT" {
		integration, err := e.store.FindIntegrationByKey(ctx, integrationKey)
		if err != nil {
			return nil, err
		}
		if integration == nil {
			return nil, errNotFound("integration key not found")
		}
		if entityID != "" {
			active, err := e.store.FindActiveAlertGroup(ctx, utils.StrVal(integration, "id"), entityID)
			if err == nil && active != nil {
				if _, ackErr := e.AcknowledgeGroup(ctx, utils.StrVal(active, "id")); ackErr != nil {
					slog.Error("acknowledge_group_failed", "group_id", utils.StrVal(active, "id"), "error", ackErr)
				}
			}
		}
		return map[string]any{"result": "success", "entity_id": entityID}, nil
	}

	normalized := normalizeVictorOpsAlert(payload)
	if _, err := e.IngestAlert(ctx, integrationKey, normalized); err != nil {
		return nil, err
	}
	resultKey := entityID
	if resultKey == "" {
		resultKey = utils.StrVal(normalized, "dedupe_key")
	}
	return map[string]any{"result": "success", "entity_id": resultKey}, nil
}

func normalizeVictorOpsAlert(payload map[string]any) map[string]any {
	messageType := strings.ToUpper(strDefault(utils.StrVal(payload, "message_type"), "CRITICAL"))
	status := "firing"
	if messageType == "RECOVERY" {
		status = "resolved"
	}
	severityMap := map[string]string{
		"CRITICAL": "critical", "WARNING": "warning", "INFO": "info", "PROBLEM": "critical",
	}
	severity, ok := severityMap[messageType]
	if !ok {
		severity = "warning"
	}

	labels := map[string]string{}
	for field, label := range map[string]string{
		"host_name":       "host",
		"monitoring_tool": "monitoring_tool",
		"service":         "service",
	} {
		if v := utils.StrVal(payload, field); v != "" {
			labels[label] = v
		}
	}
	skip := map[string]bool{
		"message_type": true, "entity_id": true, "entity_display_name": true,
		"state_message": true, "monitoring_tool": true, "host_name": true, "state_start_time": true,
	}
	for k, v := range payload {
		if !skip[k] {
			if s, ok := v.(string); ok && s != "" {
				labels[k] = s
			}
		}
	}

	entityID := utils.StrVal(payload, "entity_id")
	title := pickFirst([]string{
		utils.StrVal(payload, "entity_display_name"),
		entityID,
	}, "VictorOps alert")

	var startsAt any
	if raw := payload["state_start_time"]; raw != nil {
		// state_start_time is a Unix epoch in seconds; convert to ISO for consistency.
		switch v := raw.(type) {
		case float64:
			startsAt = utils.ToISO(time.Unix(int64(v), 0).UTC())
		case int64:
			startsAt = utils.ToISO(time.Unix(v, 0).UTC())
		}
	}

	return map[string]any{
		"title":       title,
		"message":     utils.StrVal(payload, "state_message"),
		"status":      status,
		"severity":    severity,
		"labels":      labelsAny(labels),
		"annotations": map[string]any{},
		"starts_at":   startsAt,
		"fingerprint": entityID,
		"dedupe_key":  nilIfEmpty(entityID),
		"source":      "victorops",
		"victorops": map[string]any{
			"message_type":    messageType,
			"entity_id":       entityID,
			"monitoring_tool": utils.StrVal(payload, "monitoring_tool"),
			"host_name":       utils.StrVal(payload, "host_name"),
		},
	}
}

// IngestGrafanaAlerting processes a Grafana Alerting webhook v2 payload.
func (e *Engine) IngestGrafanaAlerting(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	alerts, ok := payload["alerts"].([]any)
	if !ok || len(alerts) == 0 {
		return nil, errValidation("Grafana Alerting payload must contain non-empty alerts[]")
	}
	var results []any
	for _, rawAlert := range alerts {
		alert, ok := rawAlert.(map[string]any)
		if !ok {
			return nil, errValidation("each Grafana Alerting alert must be an object")
		}
		normalized := normalizeGrafanaAlertingAlert(payload, alert)
		r, err := e.IngestAlert(ctx, integrationKey, normalized)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return map[string]any{
		"receiver":  payload["receiver"],
		"status":    payload["status"],
		"title":     payload["title"],
		"processed": len(results),
		"results":   results,
	}, nil
}

func normalizeGrafanaAlertingAlert(envelope, alert map[string]any) map[string]any {
	labels, _ := utils.CoerceLabelMap(alert["labels"])
	annotations, _ := utils.CoerceLabelMap(alert["annotations"])
	groupLabels, _ := utils.CoerceLabelMap(envelope["groupLabels"])
	commonLabels, _ := utils.CoerceLabelMap(envelope["commonLabels"])
	commonAnnotations, _ := utils.CoerceLabelMap(envelope["commonAnnotations"])

	alertStatus := strings.ToLower(strDefault(utils.StrVal(alert, "status"), "firing"))
	state := strings.ToLower(utils.StrVal(envelope, "state"))
	status := "firing"
	if alertStatus == "resolved" || state == "ok" {
		status = "resolved"
	}

	fingerprint := utils.StrVal(alert, "fingerprint")
	groupKey := utils.StrVal(envelope, "groupKey")
	title := pickFirst([]string{
		annotations["summary"],
		annotations["description"],
		utils.StrVal(envelope, "title"),
		labels["alertname"],
		utils.StrVal(envelope, "receiver"),
	}, "Grafana Alerting")
	severity := pickFirst([]string{labels["severity"], commonLabels["severity"]}, "unknown")
	dedupeKey := pickFirst([]string{fingerprint, groupKey, alertmanagerGroupLabelsKey(groupLabels)}, "")

	values, _ := alert["values"].(map[string]any)
	return map[string]any{
		"title":         title,
		"message":       pickFirst([]string{annotations["description"], annotations["message"], utils.StrVal(envelope, "message")}, title),
		"status":        status,
		"severity":      severity,
		"labels":        labelsAny(labels),
		"annotations":   strMapAny(annotations),
		"starts_at":     nilIfEmpty(utils.StrVal(alert, "startsAt")),
		"ends_at":       nilIfEmpty(utils.StrVal(alert, "endsAt")),
		"generator_url": nilIfEmpty(utils.StrVal(alert, "generatorURL")),
		"fingerprint":   fingerprint,
		"dedupe_key":    nilIfEmpty(dedupeKey),
		"source":        "grafana_alerting",
		"grafana_alerting": map[string]any{
			"receiver":           envelope["receiver"],
			"status":             envelope["status"],
			"state":              envelope["state"],
			"title":              envelope["title"],
			"org_id":             envelope["orgId"],
			"group_labels":       strMapAny(groupLabels),
			"common_labels":      strMapAny(commonLabels),
			"common_annotations": strMapAny(commonAnnotations),
			"external_url":       envelope["externalURL"],
			"rule_url":           envelope["ruleUrl"],
			"group_key":          groupKey,
			"silence_url":        alert["silenceURL"],
			"dashboard_url":      alert["dashboardURL"],
			"panel_url":          alert["panelURL"],
			"values":             values,
			"value_string":       utils.StrVal(alert, "valueString"),
		},
	}
}

// IngestOpenSearch processes an OpenSearch Alerting notification.
//
// The Alerting plugin has no wire format of its own: a notification channel
// posts whatever Mustache template the operator saved on the trigger, and the
// default one is plain text. So the shape below is ours — documented in
// ALERT_PROCESSING.md and pasted into the channel — and this normaliser stays
// tolerant of every field being absent.
//
// A bucket-level monitor fires for several buckets at once. Those arrive as
// `alerts[]` and are ingested in one locked transaction, the way an Alertmanager
// envelope is: a monitor that produced eight buckets is one event, and eight
// transactions would take the integration's advisory lock eight times.
func (e *Engine) IngestOpenSearch(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errValidation("OpenSearch payload must be an object")
	}
	entries, hasEntries := payload["alerts"].([]any)
	if hasEntries && len(entries) == 0 {
		return nil, errValidation("OpenSearch alerts[] must not be empty")
	}

	var normalized []map[string]any
	if hasEntries {
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				return nil, errValidation("each OpenSearch alert must be an object")
			}
			normalized = append(normalized, normalizeOpenSearchAlert(payload, entry))
		}
	} else {
		normalized = []map[string]any{normalizeOpenSearchAlert(payload, nil)}
	}

	results, err := e.ingestNormalizedBatch(ctx, integrationKey, normalized)
	if err != nil {
		return nil, err
	}
	monitor, _ := payload["monitor"].(map[string]any)
	trigger, _ := payload["trigger"].(map[string]any)
	return map[string]any{
		"monitor":   utils.StrVal(monitor, "name"),
		"trigger":   utils.StrVal(trigger, "name"),
		"processed": len(results),
		"results":   results,
	}, nil
}

// openSearchSeverities maps the plugin's trigger severity — 1 is the highest —
// onto the vocabulary the on-call queue sorts by (store.SeverityRank). The two
// scales have the same five levels, so nothing is lost in the translation.
var openSearchSeverities = map[string]string{
	"1": "critical",
	"2": "error",
	"3": "warning",
	"4": "info",
	"5": "debug",
}

func normalizeOpenSearchAlert(envelope, entry map[string]any) map[string]any {
	monitor, _ := envelope["monitor"].(map[string]any)
	trigger, _ := envelope["trigger"].(map[string]any)

	monitorID := utils.StrVal(monitor, "id")
	monitorName := utils.StrVal(monitor, "name")
	triggerID := utils.StrVal(trigger, "id")
	triggerName := utils.StrVal(trigger, "name")
	bucketKeys := utils.StrVal(entry, "bucket_keys")

	// A per-alert status wins over the envelope's: one COMPLETED bucket in an
	// envelope of firing ones closes only its own group.
	status := "firing"
	rawStatus := strings.ToLower(pickFirst([]string{
		utils.StrVal(entry, "status"),
		utils.StrVal(envelope, "status"),
	}, "firing"))
	if rawStatus == "completed" || resolvedStatuses[rawStatus] {
		status = "resolved"
	}

	rawSeverity := strings.ToLower(pickFirst([]string{
		utils.StrVal(entry, "severity"),
		utils.StrVal(trigger, "severity"),
	}, ""))
	severity := rawSeverity
	if mapped, ok := openSearchSeverities[rawSeverity]; ok {
		severity = mapped
	}
	if severity == "" {
		severity = "warning"
	}

	hits := pickFirst([]string{utils.StrVal(entry, "hits"), utils.StrVal(envelope, "hits")}, "")
	osError := utils.StrVal(envelope, "error")

	labels := map[string]string{}
	for k, v := range map[string]string{
		"monitor":     monitorName,
		"trigger":     triggerName,
		"hits":        hits,
		"bucket_keys": bucketKeys,
	} {
		if v != "" {
			labels[k] = v
		}
	}

	title := pickFirst([]string{triggerName, monitorName}, "OpenSearch alert")
	if bucketKeys != "" {
		title += " (" + bucketKeys + ")"
	}
	message := osError
	if message == "" && hits != "" {
		message = title + ": " + hits + " hits"
	}

	dedupeKey := strings.Join(nonEmpty([]string{
		pickFirst([]string{monitorID, monitorName}, ""),
		pickFirst([]string{triggerID, triggerName}, ""),
		bucketKeys,
	}), ":")

	return map[string]any{
		"title":         title,
		"message":       strDefault(message, title),
		"status":        status,
		"severity":      severity,
		"labels":        labelsAny(labels),
		"annotations":   map[string]any{},
		"starts_at":     nilIfEmpty(utils.StrVal(envelope, "period_start")),
		"ends_at":       nilIfEmpty(utils.StrVal(envelope, "period_end")),
		"generator_url": nilIfEmpty(utils.StrVal(envelope, "url")),
		"fingerprint":   dedupeKey,
		"dedupe_key":    nilIfEmpty(dedupeKey),
		"source":        "opensearch",
		"opensearch": map[string]any{
			"monitor_id":   monitorID,
			"monitor_name": monitorName,
			"trigger_id":   triggerID,
			"trigger_name": triggerName,
			"severity":     rawSeverity,
			"bucket_keys":  bucketKeys,
			"hits":         hits,
			"error":        osError,
			"period_start": utils.StrVal(envelope, "period_start"),
			"period_end":   utils.StrVal(envelope, "period_end"),
		},
	}
}

// IngestElasticsearch processes a Kibana rule action or an Elasticsearch Watcher
// webhook. Both are templated by the operator the same way an OpenSearch channel
// is, and both are answered by one normaliser: the two products differ in what
// they can name an alert by — a rule and an alert instance, or a watch — which is
// a difference in three fields, not in a format.
func (e *Engine) IngestElasticsearch(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errValidation("Elasticsearch payload must be an object")
	}
	normalized := normalizeElasticsearchAlert(payload)
	result, err := e.IngestAlert(ctx, integrationKey, normalized)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":     utils.StrVal(normalized, "status"),
		"dedupe_key": normalized["dedupe_key"],
		"result":     result,
	}, nil
}

func normalizeElasticsearchAlert(payload map[string]any) map[string]any {
	rule, _ := payload["rule"].(map[string]any)
	alert, _ := payload["alert"].(map[string]any)
	metadata, _ := utils.CoerceLabelMap(payload["metadata"])

	ruleID := utils.StrVal(rule, "id")
	ruleName := utils.StrVal(rule, "name")
	alertID := utils.StrVal(alert, "id")
	watchID := utils.StrVal(payload, "watch_id")
	// Kibana names its recovery action group `recovered`, which is not one of the
	// statuses the engine already treats as a closure; the mapping is local, the
	// way Grafana's `state=ok` is.
	actionGroup := strings.ToLower(utils.StrVal(alert, "actionGroup"))

	status := "firing"
	rawStatus := strings.ToLower(pickFirst([]string{utils.StrVal(payload, "status"), actionGroup}, "firing"))
	if rawStatus == "recovered" || resolvedStatuses[rawStatus] {
		status = "resolved"
	}

	// Neither Kibana rules nor Watcher have a severity of their own, so it comes
	// from the template as a literal. The default is `warning` rather than
	// `unknown`: an unknown severity ranks below every known one, which would sink
	// these alerts to the bottom of the on-call queue.
	severity := strings.ToLower(pickFirst([]string{
		utils.StrVal(payload, "severity"),
		metadata["severity"],
	}, "warning"))

	hits := utils.StrVal(payload, "hits")
	message := utils.StrVal(payload, "message")

	labels := map[string]string{}
	for k, v := range metadata {
		labels[k] = v
	}
	for k, v := range map[string]string{
		"rule":  ruleName,
		"watch": watchID,
		"hits":  hits,
	} {
		if v != "" {
			labels[k] = v
		}
	}

	title := pickFirst([]string{ruleName, watchID, message}, "Elasticsearch alert")
	dedupeKey := strings.Join(nonEmpty([]string{
		pickFirst([]string{ruleID, ruleName, watchID}, ""),
		alertID,
	}), ":")

	return map[string]any{
		"title":         title,
		"message":       strDefault(message, title),
		"status":        status,
		"severity":      severity,
		"labels":        labelsAny(labels),
		"annotations":   map[string]any{},
		"starts_at":     nilIfEmpty(pickFirst([]string{utils.StrVal(payload, "date"), utils.StrVal(payload, "execution_time")}, "")),
		"generator_url": nilIfEmpty(utils.StrVal(payload, "url")),
		"fingerprint":   dedupeKey,
		"dedupe_key":    nilIfEmpty(dedupeKey),
		"source":        "elasticsearch",
		"elasticsearch": map[string]any{
			"rule_id":        ruleID,
			"rule_name":      ruleName,
			"alert_id":       alertID,
			"action_group":   actionGroup,
			"watch_id":       watchID,
			"execution_time": utils.StrVal(payload, "execution_time"),
			"hits":           hits,
			"metadata":       strMapAny(metadata),
		},
	}
}

// nonEmpty drops the empty strings from parts. A dedupe key is joined from
// identifiers a template may leave unfilled, and "mon::bucket" and "mon:bucket"
// must not name two different groups for the same alert.
func nonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IngestLegacyPool processes a legacy nxs-alert (NXS pool) payload.
func (e *Engine) IngestLegacyPool(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"triggerMessage", "monitoringURL"}); err != nil {
		return nil, errValidation(err.Error())
	}
	channels := parseLegacyAlertChannels(utils.StrVal(payload, "alertChannel"))
	issue, _ := payload["issue"].(map[string]any)

	triggerMessage := utils.StrVal(payload, "triggerMessage")
	title := legacyTitle(triggerMessage)

	severity := "warning"
	if utils.BoolVal(payload, "isEmergencyAlert", false) {
		severity = "critical"
	}

	issueProject, issueSubject, issueDescription := "", "", ""
	if issue != nil {
		issueProject = utils.StrVal(issue, "project")
		issueSubject = utils.StrVal(issue, "subject")
		issueDescription = utils.StrVal(issue, "description")
	}

	normalized := map[string]any{
		"title":    title,
		"message":  triggerMessage,
		"status":   "firing",
		"severity": severity,
		"labels": map[string]any{
			"alertname":      strDefault(utils.StrVal(payload, "alertname"), "LegacyPoolAlert"),
			"source":         "nxs-alert",
			"monitoring_url": utils.StrVal(payload, "monitoringURL"),
		},
		"annotations": map[string]any{
			"raw_trigger_message": utils.StrVal(payload, "rawTriggerMessage"),
			"issue_project":       issueProject,
			"issue_subject":       issueSubject,
			"issue_description":   issueDescription,
		},
		"notification_channels": toAnySlice(channels),
		"emergency_alert":       utils.BoolVal(payload, "isEmergencyAlert", false),
		"source":                "nxs-alert-compat",
		"legacy_pool_payload":   payload,
	}
	result, err := e.IngestAlert(ctx, integrationKey, normalized)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message": "success", "result": result}, nil
}

// legacyTitle derives an alert title from a legacy nxs-alert triggerMessage:
// the first line, capped at 160 runes.
func legacyTitle(triggerMessage string) string {
	title := triggerMessage
	if nl := strings.Index(title, "\n"); nl >= 0 {
		title = title[:nl]
	}
	return utils.TruncateRunes(title, 160)
}

func parseLegacyAlertChannels(alertChannel string) []string {
	channels := map[string]bool{}
	for _, item := range strings.Fields(strings.ReplaceAll(alertChannel, ",", " ")) {
		if supportedNotificationTargets[item] {
			channels[item] = true
		}
	}
	if !channels["telegram"] && !channels["call"] {
		channels["telegram"] = true
		channels["call"] = true
	}
	if !channels["email"] {
		channels["email"] = true
	}
	var result []string
	for ch := range channels {
		result = append(result, ch)
	}
	return result
}

// --- local helpers ---

func labelsAny(labels map[string]string) map[string]any {
	out := make(map[string]any, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func strMapAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func annotationsAny(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func deepCopyItem(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return deepCopyValue(m).(map[string]any)
}

func deepCopyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopyValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopyValue(val)
		}
		return out
	default:
		return v
	}
}
