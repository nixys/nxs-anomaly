package model

import "github.com/nixys/nxs-anomaly/internal/store"

// KafkaOutboxEvent is the typed Record wrapper for the kafka_outbox collection
// (transactional outbox for asynchronous Kafka publication of alert events).
// TypedColumns: topic, created_at.
type KafkaOutboxEvent struct{ mapBacked }

var _ store.Record = KafkaOutboxEvent{}

func WrapKafkaOutboxEvent(m map[string]any) KafkaOutboxEvent {
	return KafkaOutboxEvent{mapBacked{m}}
}

func (e KafkaOutboxEvent) TypedValues() []any {
	return []any{
		tvStr(e.raw, "topic"),
		tvStr(e.raw, "created_at"),
	}
}

// NotificationDeliveryAttempt is the typed Record wrapper for the
// notification_delivery_attempts collection (audit trail of provider calls).
// TypedColumns: notification_id, channel, target, attempt, status, started_at, finished_at.
type NotificationDeliveryAttempt struct{ mapBacked }

var _ store.Record = NotificationDeliveryAttempt{}

func WrapNotificationDeliveryAttempt(m map[string]any) NotificationDeliveryAttempt {
	return NotificationDeliveryAttempt{mapBacked{m}}
}

func (a NotificationDeliveryAttempt) TypedValues() []any {
	return []any{
		tvStr(a.raw, "notification_id"),
		tvStr(a.raw, "channel"),
		tvStr(a.raw, "target"),
		tvAny(a.raw, "attempt"),
		tvStr(a.raw, "status"),
		tvStr(a.raw, "started_at"),
		tvStr(a.raw, "finished_at"),
	}
}

func init() {
	registerMapBacked("kafka_outbox", func(m map[string]any) store.Record { return WrapKafkaOutboxEvent(m) })
	registerMapBacked("notification_delivery_attempts", func(m map[string]any) store.Record { return WrapNotificationDeliveryAttempt(m) })
}
