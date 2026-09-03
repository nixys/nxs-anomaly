package engine

import (
	"crypto/sha256"
	"encoding/hex"
)

// analytics.go is the part of the analytics stream that both editions share:
// the event names, which appear at the call sites in escalation, delivery and
// the worker, and the identifier digest, which personal_data.go needs whether
// or not anything is emitting. The emitting itself lives in analytics_emit.go
// (enterprise) and analytics_community.go (the no-op), so the call sites read
// the same in both builds and a lifecycle transition cannot quietly change
// shape between editions.

// Domain event names. These are the values of the `event` field, kept as the
// field name v1 used so a consumer reading it does not have to learn a second
// one.
const (
	EventAlertIngested       = "alert.ingested"
	EventGroupOpened         = "alert_group.opened"
	EventGroupReopened       = "alert_group.reopened"
	EventGroupAcknowledged   = "alert_group.acknowledged"
	EventGroupUnacknowledged = "alert_group.unacknowledged"
	EventGroupSilenced       = "alert_group.silenced"
	EventGroupResolved       = "alert_group.resolved"
	EventDeliveryAttempted   = "notification.delivery_attempted"
	// EventEscalationStep is one executed step of a chain. EventEscalationExhausted
	// is the chain running out with the group still open — the moment the service
	// has nobody left to try, which no other event reports.
	EventEscalationStep      = "alert_group.escalation_step"
	EventEscalationExhausted = "alert_group.escalation_exhausted"
	// EventScheduleCoverage is the standing coverage sample: how many schedules
	// have a hole in the coming week. Emitted on the worker's existing check, so
	// it is a periodic gauge rather than an alert.
	EventScheduleCoverage = "schedule.coverage_reported"
)

// analyticsDigest is a stable, one-way digest of an identifier. Truncated to 16
// hex characters: enough that two of this installation's ids will not collide,
// short enough to be obviously not the original.
func analyticsDigest(id string) string {
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("nxs-anomaly/analytics/" + id))
	return hex.EncodeToString(sum[:])[:16]
}

// AnalyticsConfigured reports whether this process has a producer to drain the
// outbox into. It is shared by both editions because the answer is about the
// deployment (KAFKA_BROKERS), not about the build: an enterprise install with
// no broker configured is in exactly the same state a community install is
// permanently in, and the two are told apart by AnalyticsSupported.
func (e *Engine) AnalyticsConfigured() bool { return e.kafkaProducer != nil }
