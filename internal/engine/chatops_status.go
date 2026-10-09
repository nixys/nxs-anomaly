package engine

import (
	"context"
	"fmt"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Status changes a ChatOps channel is told about. The values ride on the
// notification payload as chatops_event and are the template's {{ .event }}.
const (
	chatopsEventAcknowledged   = "acknowledged"
	chatopsEventUnacknowledged = "unacknowledged"
	chatopsEventResolved       = "resolved"
)

// chatopsStatusSaveCols are what a status transition from the API writes: the
// group, and the channel status messages with their history rows.
var chatopsStatusSaveCols = []string{"alert_groups", "notifications", "chatops_messages"}

// notifyChatopsStatus tells the ChatOps channels a group was posted to that its
// status changed.
//
// A channel is read by several people at once, and the alert it showed stays
// on screen after somebody took it: without this, the room cannot tell an
// alert being worked from one nobody has seen, and the obvious reaction is a
// second person getting up for it. The personal resolve notice
// (notifyGroupResolved) does not cover this — it reaches people, only on
// resolve, and only when NXS_ANOMALY_NOTIFY_ON_RESOLVE is set.
//
// Callers invoke it only for an actual transition: acknowledging an
// acknowledged group or resolving a resolved one is allowed, and must not post
// to the channel again. The status message is an ordinary notification, so it
// is delivered by the worker with the usual retries, never from the request.
//
// Only channels the group reached are told (NotifiedChatChannels). Recomputing
// them from the current teams would post an "acknowledged" into a channel that
// never showed the alert.
func (e *Engine) notifyChatopsStatus(state *store.State, g model.AlertGroup, event string, actor authz.Actor, timestamp string) {
	if !e.deliveryCfg.ChatopsStatusUpdates {
		return
	}
	channels := g.NotifiedChatChannels()
	if len(channels) == 0 {
		return
	}
	reason := chatopsStatusReason(event, actor)
	// Numbered per group, so each transition is its own message — a group can
	// be acknowledged, taken back and acknowledged again within a second, and
	// each of those is news. The number is written on the group in the same
	// transaction, so a mutator that is re-run computes the same keys.
	seq := g.NextChatopsStatusSeq()
	for _, ch := range channels {
		idemKey := fmt.Sprintf("%s:chatops:%s:%s:%d", g.ID(), ch.ID, event, seq)
		ntf := buildNotification(g, "", ch.Channel, ch.Target, reason, timestamp, idemKey)
		payload := notificationPayload(g, nil, reason)
		payload["chatops_channel_id"] = ch.ID
		payload["chatops_event"] = event
		payload["chatops_status_seq"] = seq
		// The alert message this status belongs to. Its platform message id is
		// only known once that message is delivered, so it is looked up at
		// delivery rather than copied here.
		if ch.NotificationID != "" {
			payload["chatops_alert_notification_id"] = ch.NotificationID
		}
		ntf.ScheduleDelivery(payload)
		addNotification(state, ntf, nil)
		recordChatopsOutbound(state, ch.ID, ntf.ID(), "queued", g, reason, timestamp)
	}
}

// sourceActor and policyActor stand for the system actor in a status message.
// Both close a group as authz.SystemActor, which reads "system" — true, and of
// no use to a room deciding whether somebody is on it. They are only ever
// described, never authorised.
var (
	sourceActor = authz.Actor{Kind: authz.KindSystem, DisplayName: "the source", Role: authz.RoleNone}
	policyActor = authz.Actor{Kind: authz.KindSystem, DisplayName: "the escalation policy", Role: authz.RoleNone}
)

// chatopsStatusReason is the line the channel reads under the alert's title.
func chatopsStatusReason(event string, actor authz.Actor) string {
	switch event {
	case chatopsEventAcknowledged:
		return "acknowledged by " + actor.Describe()
	case chatopsEventUnacknowledged:
		return "acknowledgement taken back by " + actor.Describe() + ", escalation resumes"
	case chatopsEventResolved:
		return "resolved by " + actor.Describe()
	}
	return event
}

// chatopsStatusChannelGone reports a status message whose channel was deleted
// or muted since the alert was posted to it.
//
// The status message is queued for every channel the group reached, without
// reading the channels: the paths that change a group's status do not load
// them, and should not have to. Whether the channel still wants messages is
// therefore answered here, at delivery — the same place an alert notification
// learns that its channel has no webhook.
func (e *Engine) chatopsStatusChannelGone(ctx context.Context, payload map[string]any) (deliveryOutcome, bool) {
	if utils.StrVal(payload, "chatops_event") == "" {
		return deliveryOutcome{}, false
	}
	channelID := utils.StrVal(payload, "chatops_channel_id")
	channel, err := e.store.GetItem(ctx, "chatops_channels", channelID)
	if err != nil {
		return failed("chatops", err.Error(), 0, ""), true
	}
	if channel == nil {
		return skipped(skipNotConfigured, "chatops channel "+channelID+" no longer exists"), true
	}
	if !utils.BoolVal(channel, "notifications_enabled", true) {
		return skipped(skipNotConfigured,
			"chatops channel "+utils.StrVal(channel, "name")+" has notifications disabled"), true
	}
	return deliveryOutcome{}, false
}
