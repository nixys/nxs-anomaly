package model

import (
	"encoding/json"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// NotificationBatch is the first collection whose State representation is a typed
// struct (source of truth), not a raw map. It implements store.Record so the
// generic load/save path persists it like any other row.
//
// Byte-stability with the store's snapshot-diff is preserved by construction:
//   - the baseline is taken in memory (decode → struct → MarshalJSON) right after
//     load, so an untouched row marshals to the same bytes and is skipped on save;
//   - every key not modeled by a typed field is kept verbatim in Extra (e.g. the
//     optional "flushed_at", or any forward-compatible field), so nothing is lost.
type NotificationBatch struct {
	ID                string
	BatchKey          string
	IntegrationID     string
	AlertGroupID      string
	Status            string
	FlushAt           string
	DeadlineAt        string
	NotificationCount int
	CreatedAt         string
	UpdatedAt         string
	// Extra holds keys without a typed field (optional/unknown), preserved as-is.
	Extra map[string]any
}

// batchKnownKeys are the keys mapped to typed fields; everything else → Extra.
var batchKnownKeys = map[string]bool{
	"id": true, "batch_key": true, "integration_id": true, "alert_group_id": true,
	"status": true, "flush_at": true, "deadline_at": true, "notification_count": true,
	"created_at": true, "updated_at": true,
}

func init() {
	store.RegisterRecordWrapper("notification_batches", func(m map[string]any) store.Record {
		return WrapNotificationBatch(m)
	})
}

// NewNotificationBatch creates an open batch in its canonical shape (matching the
// historical map literal: ten keys, notification_count 0, no flushed_at yet).
func NewNotificationBatch(batchKey, integrationID, alertGroupID, flushAt, deadlineAt, timestamp string) *NotificationBatch {
	return &NotificationBatch{
		ID:                utils.MakeID("nbat"),
		BatchKey:          batchKey,
		IntegrationID:     integrationID,
		AlertGroupID:      alertGroupID,
		Status:            "open",
		FlushAt:           flushAt,
		DeadlineAt:        deadlineAt,
		NotificationCount: 0,
		CreatedAt:         timestamp,
		UpdatedAt:         timestamp,
		Extra:             map[string]any{},
	}
}

func (b *NotificationBatch) toMap() map[string]any {
	m := make(map[string]any, len(b.Extra)+10)
	for k, v := range b.Extra {
		m[k] = v
	}
	m["id"] = b.ID
	m["batch_key"] = b.BatchKey
	m["integration_id"] = b.IntegrationID
	m["alert_group_id"] = b.AlertGroupID
	m["status"] = b.Status
	m["flush_at"] = b.FlushAt
	m["deadline_at"] = b.DeadlineAt
	m["notification_count"] = b.NotificationCount
	m["created_at"] = b.CreatedAt
	m["updated_at"] = b.UpdatedAt
	return m
}

// ToMap returns the batch as a plain map (for API/diagnostic results that still
// speak maps, e.g. the flushed list returned by ProcessNotificationBatches).
func (b *NotificationBatch) ToMap() map[string]any { return b.toMap() }

// MarshalJSON / UnmarshalJSON keep Extra round-tripping so unknown/optional keys
// survive a decode→encode cycle.
func (b *NotificationBatch) MarshalJSON() ([]byte, error) { return json.Marshal(b.toMap()) }

func (b *NotificationBatch) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	b.fillFrom(m)
	return nil
}

// WrapNotificationBatch builds a typed batch from a raw map (e.g. a row read
// outside the generic load path, like ListDueNotificationBatches results).
func WrapNotificationBatch(m map[string]any) *NotificationBatch {
	var b NotificationBatch
	b.fillFrom(m)
	return &b
}

func (b *NotificationBatch) fillFrom(m map[string]any) {
	b.ID = utils.StrVal(m, "id")
	b.BatchKey = utils.StrVal(m, "batch_key")
	b.IntegrationID = utils.StrVal(m, "integration_id")
	b.AlertGroupID = utils.StrVal(m, "alert_group_id")
	b.Status = utils.StrVal(m, "status")
	b.FlushAt = utils.StrVal(m, "flush_at")
	b.DeadlineAt = utils.StrVal(m, "deadline_at")
	b.NotificationCount = utils.IntVal(m, "notification_count")
	b.CreatedAt = utils.StrVal(m, "created_at")
	b.UpdatedAt = utils.StrVal(m, "updated_at")
	extra := map[string]any{}
	for k, v := range m {
		if !batchKnownKeys[k] {
			extra[k] = v
		}
	}
	b.Extra = extra
}

// store.Record implementation.

func (b *NotificationBatch) RecordID() string             { return b.ID }
func (b *NotificationBatch) MarshalData() ([]byte, error) { return b.MarshalJSON() }

// TypedValues returns the typed-column values in TypedColumns["notification_batches"]
// order: batch_key, status, flush_at, deadline_at, alert_group_id, integration_id.
// Empty strings become NULL, matching the previous map-based typedValues.
func (b *NotificationBatch) TypedValues() []any {
	return []any{
		nullIfEmpty(b.BatchKey), nullIfEmpty(b.Status), nullIfEmpty(b.FlushAt),
		nullIfEmpty(b.DeadlineAt), nullIfEmpty(b.AlertGroupID), nullIfEmpty(b.IntegrationID),
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsOpen reports whether the batch is still accepting notifications.
func (b *NotificationBatch) IsOpen() bool { return b.Status == "open" }

// Touch refreshes the flush window of an open batch when another notification
// joins it.
func (b *NotificationBatch) Touch(flushAt, ts string) {
	b.FlushAt = flushAt
	b.UpdatedAt = ts
}

// IncCount increments the batched-notification counter.
func (b *NotificationBatch) IncCount() { b.NotificationCount++ }

// Close flushes the batch: it becomes closed with a final count and flushed_at.
func (b *NotificationBatch) Close(ts string, count int) {
	b.Status = "closed"
	b.UpdatedAt = ts
	b.NotificationCount = count
	if b.Extra == nil {
		b.Extra = map[string]any{}
	}
	b.Extra["flushed_at"] = ts
}
