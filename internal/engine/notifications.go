package engine

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// notifyUsers creates notifications for each recipient user using their default
// personal policy (or the legacy target blast when they have none).
func (e *Engine) notifyUsers(state *store.State, g model.AlertGroup, userIDs []string, reason, timestamp string) {
	e.notifyUsersPolicy(state, g, userIDs, reason, timestamp, "default")
}

// notifyUsersPolicy is notifyUsers with an explicit personal-policy selector
// ("default" or "important"). When a recipient has that policy it drives a
// notify/wait/fallback run (startPolicyRun); otherwise every configured target
// is fired at once, the pre-BETA-031 behaviour.
func (e *Engine) notifyUsersPolicy(state *store.State, g model.AlertGroup, userIDs []string, reason, timestamp, policyName string) {
	seen := notificationIdemSet(state)
	var notified []string
	var unknown []string
	for _, userID := range userIDs {
		user := state.Users[userID]
		if user == nil {
			// A schedule or chain naming someone who no longer exists would
			// otherwise page nobody and say nothing about it.
			unknown = append(unknown, userID)
			continue
		}
		// A personal policy replaces the direct-target blast for this user; the
		// chatops/mobile fan-outs below are separate surfaces and always run.
		if !e.startPolicyRun(state, g, user, policyName, reason, timestamp, seen) {
			targets := e.notificationTargetsForUser(state, g, user)
			for _, target := range targets {
				channel := utils.StrVal(target, "type")
				targetAddr := utils.StrVal(target, "target")
				ntf := buildNotification(g, userID, channel, targetAddr, reason, timestamp, "")
				if e.attachNotificationBatch(state, g, ntf, timestamp) {
					addNotification(state, ntf, seen)
					continue
				}
				// Every channel is queued for delivery, including log: a
				// notification may only claim it was delivered after an adapter
				// says so. Channels that turn out to have no transport here come
				// back skipped, which is the truth the old shortcut hid.
				ntf.ScheduleDelivery(notificationPayload(g, user, reason))
				addNotification(state, ntf, seen)
			}
		}
		e.fanoutChatopsNotifications(state, g, user, reason, timestamp, seen)
		e.fanoutMobileNotifications(state, g, user, reason, timestamp, seen)
		g.AddNotifiedUser(userID)
		notified = append(notified, utils.StrVal(user, "username"))
	}
	if len(notified) > 0 {
		g.AppendLog("notified",
			fmt.Sprintf("Notified users: %s", joinStrings(notified, ", ")),
			map[string]any{"reason": reason})
	}
	if len(unknown) > 0 {
		slog.Warn("notify_unknown_users",
			"group_id", g.ID(), "reason", reason, "user_ids", joinStrings(unknown, ","))
		g.AppendLog("notify_skipped_unknown_users",
			fmt.Sprintf("Skipped %d recipient(s) that no longer exist", len(unknown)),
			map[string]any{"reason": reason, "user_ids": toAnySlice(unknown)})
	}
}

// notificationTargetsForUser resolves which channels/targets a user should receive for a group.
func (e *Engine) notificationTargetsForUser(state *store.State, g model.AlertGroup, user map[string]any) []map[string]any {
	integID := g.IntegrationID()
	var policy map[string]any
	if integ := state.Integrations[integID]; integ != nil {
		policy, _ = integ["notification_policy"].(map[string]any)
	}
	if policy == nil {
		policy = copyMap(defaultNotificationPolicy)
	}

	explicitTargets, _ := user["notification_targets"].([]any)
	if len(explicitTargets) == 0 {
		explicitTargets = []any{map[string]any{"type": "log", "target": ""}}
	}

	// Requested channels: from group override > policy > user targets.
	var requestedChannels []string
	if ch := g.NotificationChannels(); len(ch) > 0 {
		requestedChannels = ch
	} else if pch := anyToStringSlice(policy["channels"]); len(pch) > 0 {
		requestedChannels = pch
	} else {
		for _, raw := range explicitTargets {
			if t, ok := raw.(map[string]any); ok {
				requestedChannels = append(requestedChannels, strDefault(utils.StrVal(t, "type"), "log"))
			}
		}
	}

	var channels []string
	for _, ch := range requestedChannels {
		if supportedNotificationTargets[ch] {
			channels = append(channels, ch)
		}
	}

	targetsByType := map[string]map[string]any{}
	for _, raw := range explicitTargets {
		if t, ok := raw.(map[string]any); ok {
			targetsByType[utils.StrVal(t, "type")] = t
		}
	}

	var targets []map[string]any
	for _, ch := range channels {
		if explicit, ok := targetsByType[ch]; ok {
			targets = append(targets, map[string]any{"type": ch, "target": utils.StrVal(explicit, "target")})
			continue
		}
		switch ch {
		case "log":
			targets = append(targets, map[string]any{"type": "log", "target": ""})
		case "email":
			if email := utils.StrVal(user, "email"); email != "" {
				targets = append(targets, map[string]any{"type": "email", "target": email})
			}
		case "telegram":
			if tgID := utils.StrVal(user, "telegram_id"); tgID != "" {
				targets = append(targets, map[string]any{"type": "telegram", "target": tgID})
			}
		case "call":
			if phone := utils.StrVal(user, "phone"); phone != "" {
				targets = append(targets, map[string]any{"type": "call", "target": phone})
			}
		}
	}
	if len(targets) == 0 {
		return []map[string]any{{"type": "log", "target": ""}}
	}
	return targets
}

// attachNotificationBatch tries to batch the notification; returns true if batched.
func (e *Engine) attachNotificationBatch(state *store.State, g model.AlertGroup, ntf model.Notification, timestamp string) bool {
	integID := g.IntegrationID()
	var policy map[string]any
	if integ := state.Integrations[integID]; integ != nil {
		policy, _ = integ["notification_policy"].(map[string]any)
	}
	if policy == nil {
		return false
	}
	timeoutSecs := utils.IntVal(policy, "batch_timeout_seconds")
	deadlineSecs := utils.IntVal(policy, "batch_deadline_seconds")
	if timeoutSecs <= 0 && deadlineSecs <= 0 {
		return false
	}

	batchKey := integID + ":" + g.DedupeKey()
	var batch *model.NotificationBatch
	for _, b := range state.NotificationBatches {
		if bb, ok := b.(*model.NotificationBatch); ok && bb.BatchKey == batchKey && bb.IsOpen() {
			batch = bb
			break
		}
	}

	now, _ := utils.ParseDatetime(timestamp)
	flushSecs := timeoutSecs
	if flushSecs <= 0 {
		flushSecs = deadlineSecs
	}
	flushAt := utils.ToISO(now.Add(time.Duration(flushSecs) * time.Second))

	if batch == nil {
		dl := deadlineSecs
		if dl <= 0 {
			dl = timeoutSecs
		}
		deadlineAt := utils.ToISO(now.Add(time.Duration(dl) * time.Second))
		batch = model.NewNotificationBatch(batchKey, integID, g.ID(), flushAt, deadlineAt, timestamp)
		state.NotificationBatches[batch.ID] = batch
	} else {
		batch.Touch(flushAt, timestamp)
	}

	ntf.AttachToBatch(batch.ID, batchKey)
	batch.IncCount()
	return true
}

// fanoutChatopsNotifications sends notifications to chatops channels the user belongs to.
func (e *Engine) fanoutChatopsNotifications(state *store.State, g model.AlertGroup, user map[string]any, reason, timestamp string, seen map[string]struct{}) {
	userID := utils.StrVal(user, "id")
	stepKey := fmt.Sprintf("%d:%d", g.CurrentStep(), g.RepeatCount())
	groupID := g.ID()

	for _, ch := range state.ChatopsChannels {
		if !utils.BoolVal(ch, "notifications_enabled", true) {
			continue
		}
		belongs := utils.StrVal(ch, "user_id") == userID
		if !belongs {
			if teamID := utils.StrVal(ch, "team_id"); teamID != "" {
				team := state.Teams[teamID]
				if team != nil {
					for _, mid := range anyToStringSlice(team["member_ids"]) {
						if mid == userID {
							belongs = true
							break
						}
					}
				}
			}
		}
		if !belongs {
			continue
		}

		channelID := utils.StrVal(ch, "id")
		platform := utils.StrVal(ch, "platform")
		channelName := utils.StrVal(ch, "name")

		var notificationID string
		if platform == "telegram" {
			idemKey := fmt.Sprintf("%s:%s:telegram:%s:%s", groupID, userID, channelName, stepKey)
			ntf := buildNotification(g, userID, "telegram", channelName, reason, timestamp, idemKey)
			ntf.ScheduleDelivery(notificationPayload(g, user, reason))
			addNotification(state, ntf, seen)
			notificationID = ntf.ID()
		} else {
			idemKey := fmt.Sprintf("%s:%s:chatops:%s:%s", groupID, userID, channelID, stepKey)
			ntf := buildNotification(g, userID, "chatops", channelID, reason, timestamp, idemKey)
			// Queued, not assumed: the channel is a real transport only if it
			// has an incoming webhook, and the delivery step is what decides.
			ntf.ScheduleDelivery(notificationPayload(g, user, reason))
			addNotification(state, ntf, seen)
			notificationID = ntf.ID()
		}

		// The message history must not claim an outbound message that had
		// nowhere to go. Whether this channel has a transport is known here —
		// it is the same webhook_url the delivery step will look for — so the
		// row records that, and points at the notification whose delivery
		// carries the final answer.
		deliveryStatus := "queued"
		if platform != "telegram" && utils.StrVal(ch, "webhook_url") == "" {
			deliveryStatus = "skipped_no_transport"
		}
		msg := map[string]any{
			"id":              utils.MakeID("chatmsg"),
			"channel_id":      channelID,
			"direction":       "outbound",
			"actor":           "system",
			"command":         nil,
			"notification_id": notificationID,
			"delivery_status": deliveryStatus,
			"response": map[string]any{
				"text":           fmt.Sprintf("[%s] %s (%s)", g.Severity(), g.Title(), reason),
				"alert_group_id": groupID,
			},
			"created_at": timestamp,
		}
		state.ChatopsMessages[msg["id"].(string)] = msg
	}
}

// fanoutMobileNotifications creates mobile push notifications for the user's devices.
func (e *Engine) fanoutMobileNotifications(state *store.State, g model.AlertGroup, user map[string]any, reason, timestamp string, seen map[string]struct{}) {
	userID := utils.StrVal(user, "id")
	groupID := g.ID()
	stepKey := fmt.Sprintf("%d:%d", g.CurrentStep(), g.RepeatCount())

	for _, device := range state.MobileDevices {
		if utils.StrVal(device, "user_id") != userID {
			continue
		}
		if !utils.BoolVal(device, "active", true) {
			continue
		}
		deviceID := utils.StrVal(device, "id")
		idemKey := fmt.Sprintf("%s:%s:mobile:%s:%s", groupID, userID, deviceID, stepKey)
		ntf := buildNotification(g, userID, "mobile", deviceID, reason, timestamp, idemKey)
		// Queued for the push relay. Without one configured the delivery step
		// reports skipped; it must never be born "delivered".
		ntf.ScheduleDelivery(notificationPayload(g, user, reason))
		addNotification(state, ntf, seen)
	}
}

// notificationIdemSet collects the idempotency keys of the notifications already
// in state. A fan-out (notifyUsers and the chatops/mobile fan-outs) builds it
// once and threads it through addNotification, turning the per-insert duplicate
// check from an O(n) scan of all notifications into an O(1) lookup.
func notificationIdemSet(state *store.State) map[string]struct{} {
	seen := make(map[string]struct{}, len(state.Notifications))
	for _, rec := range state.Notifications {
		if en, ok := rec.(model.Notification); ok {
			if k := en.IdempotencyKey(); k != "" {
				seen[k] = struct{}{}
			}
		}
	}
	return seen
}

// addNotification stores a notification unless one with the same idempotency key
// is already present. It takes the typed wrapper (not a raw map) so callers that
// mutate the notification — ScheduleDelivery, AttachToBatch — store the mutated
// state, which the pointer-backed wrapper carries even across helper calls.
//
// seen, when non-nil, is an idempotency-key set the caller maintains across a
// fan-out so the duplicate check is O(1); addNotification keeps it up to date.
// When nil (single-shot callers) it falls back to scanning state.Notifications.
func addNotification(state *store.State, n model.Notification, seen map[string]struct{}) {
	if idemKey := n.IdempotencyKey(); idemKey != "" {
		if seen != nil {
			if _, dup := seen[idemKey]; dup {
				return
			}
		} else {
			for _, existing := range state.Notifications {
				if en, ok := existing.(model.Notification); ok && en.IdempotencyKey() == idemKey {
					return
				}
			}
		}
		if seen != nil {
			seen[idemKey] = struct{}{}
		}
	}
	state.Notifications[n.ID()] = n
}

// buildNotification is a thin shim over the canonical constructor in
// internal/model (Notification owns the delivery state machine).
//
// It takes the alert group rather than its id so the owning integration is
// carried onto every notification. That is what makes delivery records
// team-scopeable, and taking the group here means no call site can forget it —
// this is the single place the engine constructs a notification.
func buildNotification(g model.AlertGroup, userID, channel, target, reason, timestamp, idempotencyKey string) model.Notification {
	n := model.NewNotification(g.ID(), g.IntegrationID(), userID, channel, target, reason, timestamp, idempotencyKey)
	// The analytics context travels with the notification, because the delivery
	// worker will not have the group in hand when it reports the attempt. Read,
	// not ensured: assigning an episode is a transition the group's own mutators
	// own, and a helper that quietly mutated the group would write it from a
	// path that may not be saving groups at all.
	n.SetAnalyticsContext(g.EpisodeID(), g.AnalyticsTeamID())
	return n
}

func notificationPayload(g model.AlertGroup, user map[string]any, reason string) map[string]any {
	p := map[string]any{
		"alert_group_id": g.ID(),
		"integration_id": g.IntegrationID(),
		"title":          g.Title(),
		"severity":       g.Severity(),
		"status":         strDefault(g.Status(), "open"),
		"labels":         g.LabelsRaw(),
		"reason":         reason,
	}
	// Carried on the payload so the delivery span can link back to the ingest
	// that caused it without the worker re-reading the group.
	if tp := g.TraceParent(); tp != "" {
		p["trace_parent"] = tp
	}
	if user != nil {
		p["user"] = map[string]any{
			"id":       utils.StrVal(user, "id"),
			"name":     utils.StrVal(user, "name"),
			"username": utils.StrVal(user, "username"),
		}
	}
	return p
}

// triggerWebhook adds a webhook delivery_scheduled notification to state.
func (e *Engine) triggerWebhook(state *store.State, g model.AlertGroup, webhookURL, timestamp string) {
	groupID := g.ID()
	ntf := buildNotification(g, "", "webhook", webhookURL, "escalation webhook", timestamp, "")
	ntf.ScheduleDelivery(map[string]any{
		"alert_group_id":   groupID,
		"title":            g.Title(),
		"severity":         g.Severity(),
		"labels":           g.LabelsRaw(),
		"status":           g.Status(),
		"last_received_at": g.LastReceivedAt(),
		"trace_parent":     g.TraceParent(),
	})
	addNotification(state, ntf, nil)
	g.AppendLog("webhook", "Triggered escalation webhook",
		map[string]any{
			"webhook_url":     webhookURL,
			"delivery_status": "delivery_scheduled",
			"notification_id": ntf.ID(),
		})
}

// resolveNotificationReason is the reason string on a resolution notice. It is
// also what the recipient reads, so it says what happened rather than naming a
// mechanism.
const resolveNotificationReason = "alert resolved"

// notifyGroupResolved tells the people this group woke that it is over.
//
// Off unless NXS_ANOMALY_NOTIFY_ON_RESOLVE is set: a resolution notice is a new
// class of message, and switching it on for every existing installation at
// upgrade time is not a decision an upgrade gets to make.
//
// Three properties are deliberate:
//
//   - Only people who were actually paged are told. Recomputing recipients from
//     the chain would notify whoever is on call *now*, which is a different set
//     and, at 4am, the wrong one.
//   - It fires once. Resolve is idempotent and allowed from any state, so a
//     second resolve — a duplicate source event, an operator closing an already
//     closed group — would otherwise page everyone again.
//   - No personal policy run is started. A policy escalates until someone
//     acknowledges, and there is nothing left to acknowledge; this is one
//     message per configured target and then silence.
func (e *Engine) notifyGroupResolved(state *store.State, g model.AlertGroup, timestamp string) {
	if !e.deliveryCfg.NotifyOnResolve || g.ResolveNotifiedAt() != "" {
		return
	}
	recipients := g.NotifiedUserIDs()
	if len(recipients) == 0 {
		return
	}
	g.MarkResolveNotified(timestamp)

	seen := notificationIdemSet(state)
	var notified, unknown []string
	for _, userID := range recipients {
		user := state.Users[userID]
		if user == nil {
			unknown = append(unknown, userID)
			continue
		}
		for _, target := range e.notificationTargetsForUser(state, g, user) {
			channel := utils.StrVal(target, "type")
			// Keyed on the group and channel rather than the escalation step: a
			// group resolves once, so this is the whole key there is.
			idemKey := fmt.Sprintf("%s:%s:%s:resolved", g.ID(), userID, channel)
			ntf := buildNotification(g, userID, channel, utils.StrVal(target, "target"),
				resolveNotificationReason, timestamp, idemKey)
			ntf.ScheduleDelivery(notificationPayload(g, user, resolveNotificationReason))
			addNotification(state, ntf, seen)
		}
		notified = append(notified, utils.StrVal(user, "username"))
	}
	if len(unknown) > 0 {
		// The group remembers ids; a person deleted since being paged cannot be
		// told. Saying so beats a resolution notice that quietly reaches fewer
		// people than the alert did.
		slog.Warn("resolve_notify_unknown_users",
			"group_id", g.ID(), "user_ids", joinStrings(unknown, ","))
	}
	if len(notified) > 0 {
		g.AppendLog("resolve_notified",
			fmt.Sprintf("Notified of resolution: %s", joinStrings(notified, ", ")), nil)
	}
}

// renderNotificationText renders the notification text using templates or a fallback.
func renderNotificationText(notification, payload map[string]any, templateStr string) string {
	title := strDefault(utils.StrVal(payload, "title"), "Alert notification")
	severity := strDefault(utils.StrVal(payload, "severity"), "unknown")
	reason := strDefault(utils.StrVal(payload, "reason"), utils.StrVal(notification, "reason"))
	groupID := strDefault(utils.StrVal(payload, "alert_group_id"), utils.StrVal(notification, "alert_group_id"))
	labels, _ := utils.CoerceLabelMap(payload["labels"])
	if templateStr != "" {
		ctx := map[string]any{
			"title":    title,
			"severity": severity,
			"reason":   reason,
			"group_id": groupID,
			"status":   strDefault(utils.StrVal(payload, "status"), "open"),
			"labels":   formatLabels(labels, nil),
		}
		// One flat key per label, because the renderer stringifies the context
		// into map[string]string before executing: {{ labels.pod }} cannot work
		// by construction, so {{ label_pod }} is what a template can reach.
		for k, v := range labels {
			if key := labelContextKey(k); key != "" {
				if _, taken := ctx[key]; !taken {
					ctx[key] = v
				}
			}
		}
		if u, ok := payload["user"].(map[string]any); ok {
			ctx["user_name"] = utils.StrVal(u, "name")
			ctx["user_username"] = utils.StrVal(u, "username")
		}
		return renderTemplate(templateStr, ctx)
	}
	base := fmt.Sprintf("[%s] %s", severity, title)
	if reason != "" {
		base += "\n" + reason
	}
	// The labels are where a person finds the pod and the namespace. They were
	// carried in the payload and stored — which is why the UI shows them — and
	// then dropped here, so the delivered message named an incident without
	// saying what it was about.
	//
	// alertname and severity are skipped: the first is the title and the second
	// is already the prefix, so repeating them costs a line and says nothing.
	if s := formatLabels(labels, map[string]bool{"alertname": true, "severity": true}); s != "" {
		base += "\n" + s
	}
	if groupID != "" {
		base += "\nAlert group: " + groupID
	}
	return base
}

// maxDefaultLabelChars bounds the labels line in the default text. A Telegram
// message and an SMS both have limits, and an Alertmanager alert can carry
// dozens of labels; a message truncated by the provider helps nobody.
const maxDefaultLabelChars = 400

// formatLabels renders labels as a deterministic "k=v, k=v" line, skipping the
// keys in skip. Sorted so the same alert reads the same way twice.
func formatLabels(labels map[string]string, skip map[string]bool) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if skip[k] || labels[k] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		part := k + "=" + labels[k]
		if sb.Len() > 0 {
			if sb.Len()+2+len(part) > maxDefaultLabelChars {
				sb.WriteString(", …")
				break
			}
			sb.WriteString(", ")
		}
		sb.WriteString(part)
	}
	return sb.String()
}

// labelContextKey turns a label name into something a template can reference.
// Label names carry dots and slashes (kubernetes.io/name) that a Go template
// identifier cannot, so everything outside [a-z0-9_] becomes an underscore and
// the result is prefixed to keep it clear of the built-in context keys.
func labelContextKey(name string) string {
	if name == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("label_")
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
