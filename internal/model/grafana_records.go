package model

import "github.com/nixys/nxs-anomaly/internal/store"

// GrafanaPlugin is the typed Record wrapper for the grafana_plugins collection.
// No typed columns — only id + data jsonb.
type GrafanaPlugin struct{ mapBacked }

var _ store.Record = GrafanaPlugin{}

func WrapGrafanaPlugin(m map[string]any) GrafanaPlugin {
	return GrafanaPlugin{mapBacked{m}}
}

func (GrafanaPlugin) TypedValues() []any { return nil }

// GrafanaNotificationPolicy is the typed Record wrapper for
// grafana_notification_policies. TypedColumns: user_id.
type GrafanaNotificationPolicy struct{ mapBacked }

var _ store.Record = GrafanaNotificationPolicy{}

func WrapGrafanaNotificationPolicy(m map[string]any) GrafanaNotificationPolicy {
	return GrafanaNotificationPolicy{mapBacked{m}}
}

func (p GrafanaNotificationPolicy) TypedValues() []any {
	return []any{tvStr(p.raw, "user_id")}
}

// GrafanaChannelFilter is the typed Record wrapper for grafana_channel_filters.
// No typed columns.
type GrafanaChannelFilter struct{ mapBacked }

var _ store.Record = GrafanaChannelFilter{}

func WrapGrafanaChannelFilter(m map[string]any) GrafanaChannelFilter {
	return GrafanaChannelFilter{mapBacked{m}}
}

func (GrafanaChannelFilter) TypedValues() []any { return nil }

// GrafanaHeartbeat is the typed Record wrapper for grafana_heartbeats.
// No typed columns.
type GrafanaHeartbeat struct{ mapBacked }

var _ store.Record = GrafanaHeartbeat{}

func WrapGrafanaHeartbeat(m map[string]any) GrafanaHeartbeat {
	return GrafanaHeartbeat{mapBacked{m}}
}

func (GrafanaHeartbeat) TypedValues() []any { return nil }

// NotificationPolicyRun is the typed Record wrapper for notification_policy_runs
// (BETA-031). TypedColumns: alert_group_id, user_id, status, next_step_at.
type NotificationPolicyRun struct{ mapBacked }

var _ store.Record = NotificationPolicyRun{}

func WrapNotificationPolicyRun(m map[string]any) NotificationPolicyRun {
	return NotificationPolicyRun{mapBacked{m}}
}

func (r NotificationPolicyRun) TypedValues() []any {
	return []any{
		tvStr(r.raw, "alert_group_id"),
		tvStr(r.raw, "user_id"),
		tvStr(r.raw, "status"),
		tvStr(r.raw, "next_step_at"),
	}
}

func init() {
	registerMapBacked("grafana_plugins", func(m map[string]any) store.Record { return WrapGrafanaPlugin(m) })
	registerMapBacked("grafana_notification_policies", func(m map[string]any) store.Record { return WrapGrafanaNotificationPolicy(m) })
	registerMapBacked("grafana_channel_filters", func(m map[string]any) store.Record { return WrapGrafanaChannelFilter(m) })
	registerMapBacked("grafana_heartbeats", func(m map[string]any) store.Record { return WrapGrafanaHeartbeat(m) })
	registerMapBacked("notification_policy_runs", func(m map[string]any) store.Record { return WrapNotificationPolicyRun(m) })
}
