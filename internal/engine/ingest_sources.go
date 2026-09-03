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
