package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

func scheduledNotification() map[string]any {
	return map[string]any{
		"id":          "ntf1",
		"status":      NotificationDeliveryScheduled,
		"channel":     "webhook",
		"retry_count": 0,
	}
}

func TestNewNotificationDefaults(t *testing.T) {
	n := NewNotification("grp1", "int1", "u1", "email", "a@b.c", "step notify", "ts0", "")
	raw := n.Raw()
	if raw["status"] != NotificationDelivered {
		t.Errorf("initial status = %v, want delivered (no-delivery channels)", raw["status"])
	}
	if raw["idempotency_key"] != "grp1:u1:email:a@b.c:step notify" {
		t.Errorf("default idempotency key = %v", raw["idempotency_key"])
	}
	if raw["user_id"] != "u1" || raw["retry_count"] != 0 || raw["created_at"] != "ts0" {
		t.Errorf("defaults: %#v", raw)
	}

	// Explicit idempotency key wins; empty user becomes NULL.
	n2 := NewNotification("grp1", "int1", "", "webhook", "http://x", "escalation webhook", "ts0", "custom-key")
	if n2.Raw()["idempotency_key"] != "custom-key" {
		t.Errorf("explicit idempotency key not kept: %v", n2.Raw()["idempotency_key"])
	}
	if n2.Raw()["user_id"] != nil {
		t.Errorf("empty user_id must be nil, got %v", n2.Raw()["user_id"])
	}
}

func TestScheduleDelivery(t *testing.T) {
	n := NewNotification("grp1", "int1", "u1", "webhook", "http://x", "r", "ts0", "")
	payload := map[string]any{"title": "t"}
	n.ScheduleDelivery(payload)
	if n.Status() != NotificationDeliveryScheduled {
		t.Fatalf("status = %v", n.Status())
	}
	if p, _ := n.Raw()["payload"].(map[string]any); p["title"] != "t" {
		t.Errorf("payload = %#v", n.Raw()["payload"])
	}
}

func TestAttachToBatch(t *testing.T) {
	n := NewNotification("grp1", "int1", "u1", "slack", "hook", "r", "ts0", "")
	n.AttachToBatch("nbat1", "int1:dk1")
	raw := n.Raw()
	if raw["status"] != NotificationBatched || raw["batch_id"] != "nbat1" || raw["batch_key"] != "int1:dk1" {
		t.Errorf("batched state: %#v", raw)
	}
	if raw["provider_status"] != "waiting_for_batch" {
		t.Errorf("provider_status = %v", raw["provider_status"])
	}
}

func TestMarkDeliveredMutatesRawInPlace(t *testing.T) {
	raw := scheduledNotification()
	raw["last_error"] = "old error"
	raw["next_retry_at"] = "old"
	n := WrapNotification(raw)
	n.MarkDelivered("ts1", "http_post")
	raw = n.Raw()
	if raw["status"] != NotificationDelivered || raw["provider_status"] != "http_post" {
		t.Errorf("delivered state: %#v", raw)
	}
	if raw["last_error"] != nil || raw["next_retry_at"] != nil {
		t.Errorf("delivered must clear error/retry fields: %#v", raw)
	}
	if raw["updated_at"] != "ts1" {
		t.Errorf("updated_at = %v", raw["updated_at"])
	}
}

func TestScheduleRetryOrFailSchedulesWithDelay(t *testing.T) {
	raw := scheduledNotification()
	ts := "2026-06-12T10:00:00Z"
	n := WrapNotification(raw)
	n.ScheduleRetryOrFail(ts, "boom", 3, []int{1, 5, 30})
	raw = n.Raw()
	if raw["status"] != NotificationRetryScheduled {
		t.Fatalf("status = %v", raw["status"])
	}
	if raw["retry_count"] != 1 || raw["last_error"] != "boom" || raw["provider_status"] != "delivery_failed" {
		t.Errorf("retry state: %#v", raw)
	}
	base, _ := utils.ParseDatetime(ts)
	if want := utils.ToISO(base.Add(1 * 60e9)); raw["next_retry_at"] != want {
		t.Errorf("next_retry_at = %v, want %v (first delay = 1 minute)", raw["next_retry_at"], want)
	}
}

func TestScheduleRetryOrFailClampsDelayIndex(t *testing.T) {
	raw := scheduledNotification()
	raw["retry_count"] = 5 // attempt 6, delays has 2 entries → use last
	ts := "2026-06-12T10:00:00Z"
	n := WrapNotification(raw)
	n.ScheduleRetryOrFail(ts, "boom", 10, []int{1, 5})
	raw = n.Raw()
	base, _ := utils.ParseDatetime(ts)
	if want := utils.ToISO(base.Add(5 * 60e9)); raw["next_retry_at"] != want {
		t.Errorf("next_retry_at = %v, want %v (clamped to last delay)", raw["next_retry_at"], want)
	}
}

func TestScheduleRetryOrFailExhaustsToFailed(t *testing.T) {
	raw := scheduledNotification()
	raw["retry_count"] = 2
	n := WrapNotification(raw)
	n.ScheduleRetryOrFail("ts", "", 3, []int{1})
	raw = n.Raw()
	if raw["status"] != NotificationFailed {
		t.Fatalf("status = %v, want failed at maxRetries", raw["status"])
	}
	if raw["retry_count"] != 3 || raw["next_retry_at"] != nil {
		t.Errorf("failed state: %#v", raw)
	}
	if raw["last_error"] != "delivery failed" {
		t.Errorf("empty errMsg must default: %v", raw["last_error"])
	}
}

func TestScheduleRetryOrFailEmptyDelaysFailsInsteadOfPanic(t *testing.T) {
	raw := scheduledNotification()
	n := WrapNotification(raw)
	n.ScheduleRetryOrFail("ts", "boom", 5, nil)
	if n.Status() != NotificationFailed {
		t.Fatalf("empty delays must fail permanently, got %v", n.Status())
	}
}

func TestFailRetryContext(t *testing.T) {
	raw := scheduledNotification()
	raw["status"] = NotificationRetryScheduled
	n := WrapNotification(raw)
	n.FailRetryContext()
	raw = n.Raw()
	if raw["status"] != NotificationFailed || raw["last_error"] != "retry context not found" || raw["next_retry_at"] != nil {
		t.Errorf("state: %#v", raw)
	}
}

func TestBatchTransitions(t *testing.T) {
	raw := scheduledNotification()
	raw["status"] = NotificationBatched
	payload := map[string]any{"title": "x"}
	n := WrapNotification(raw)
	n.ReleaseFromBatch("ts", payload)
	raw = n.Raw()
	if raw["status"] != NotificationDeliveryScheduled || raw["provider_status"] != "batch_flushed" {
		t.Errorf("release state: %#v", raw)
	}
	if p, _ := raw["payload"].(map[string]any); p["title"] != "x" {
		t.Errorf("payload = %#v", raw["payload"])
	}

	raw2 := scheduledNotification()
	raw2["status"] = NotificationBatched
	n2 := WrapNotification(raw2)
	n2.FailBatchContext("ts")
	raw2 = n2.Raw()
	if raw2["status"] != NotificationFailed || raw2["last_error"] != "batch delivery context not found" {
		t.Errorf("fail state: %#v", raw2)
	}
}

func TestNotificationTypedAccessors(t *testing.T) {
	raw := map[string]any{
		"id":              "ntf1",
		"alert_group_id":  "grp1",
		"user_id":         "u1",
		"channel":         "telegram",
		"target":          "@chan",
		"reason":          "escalation",
		"status":          NotificationRetryScheduled,
		"retry_count":     float64(2),
		"last_error":      "boom",
		"idempotency_key": "grp1:u1:telegram:@chan:escalation",
		"payload":         map[string]any{"title": "x"},
	}
	n := WrapNotification(raw)
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"ID", n.ID(), "ntf1"},
		{"AlertGroupID", n.AlertGroupID(), "grp1"},
		{"UserID", n.UserID(), "u1"},
		{"Channel", n.Channel(), "telegram"},
		{"Target", n.Target(), "@chan"},
		{"Reason", n.Reason(), "escalation"},
		{"Status", n.Status(), NotificationRetryScheduled},
		{"RetryCount", n.RetryCount(), 2},
		{"LastError", n.LastError(), "boom"},
		{"IdempotencyKey", n.IdempotencyKey(), "grp1:u1:telegram:@chan:escalation"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	if !reflect.DeepEqual(n.Payload(), map[string]any{"title": "x"}) {
		t.Errorf("Payload = %#v", n.Payload())
	}
}

// TestNotificationWrapIsByteIdentityForFullShape is the strict byte-stability
// gate for the typed-fields backing: for a real persisted notification row,
// WrapNotification(m).MarshalData() must equal json.Marshal(m) exactly — no key
// added, dropped, or retyped. retry_count is float64 to mirror a jsonb load.
func TestNotificationWrapIsByteIdentityForFullShape(t *testing.T) {
	full := map[string]any{
		"id":              "ntf1",
		"alert_group_id":  "grp1",
		"channel":         "webhook",
		"target":          "http://x",
		"status":          NotificationRetryScheduled,
		"reason":          "escalation",
		"idempotency_key": "grp1::webhook:http://x:escalation",
		"retry_count":     float64(2),
		"created_at":      "ts0",
		"updated_at":      "ts1",
		"user_id":         nil,
		"next_retry_at":   "ts2",
		"last_error":      "boom",
		"batch_id":        nil,
		"batch_key":       nil,
		"provider_status": "delivery_failed",
		"payload":         map[string]any{"title": "x"},
	}
	want, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	got, err := WrapNotification(full).MarshalData()
	if err != nil {
		t.Fatalf("marshal wrapped: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("WrapNotification not byte-identity\nsource:  %s\nwrapped: %s", want, got)
	}
}

func TestNotificationRecordImpl(t *testing.T) {
	n := NewNotification("grp1", "int1", "u1", "telegram", "@chan", "escalation", "ts0", "")
	if n.RecordID() != n.ID() || n.RecordID() == "" {
		t.Errorf("RecordID = %q, want non-empty == ID", n.RecordID())
	}
	// TypedValues mirrors store.typedValues("notifications", ...) order:
	// alert_group_id, user_id, channel, status, idempotency_key, retry_count,
	// next_retry_at, last_error, batch_id, batch_key, provider_status,
	// integration_id.
	want := []any{
		"grp1", "u1", "telegram", NotificationDelivered,
		"grp1:u1:telegram:@chan:escalation", 0,
		nil, nil, nil, nil, nil, "int1",
	}
	if got := n.TypedValues(); !reflect.DeepEqual(got, want) {
		t.Errorf("TypedValues = %#v\nwant %#v", got, want)
	}
	// Empty user_id stores nil in raw and surfaces as nil in TypedValues.
	n2 := NewNotification("grp1", "int1", "", "webhook", "http://x", "escalation webhook", "ts0", "")
	if n2.TypedValues()[1] != nil {
		t.Errorf("empty user_id must yield nil typed value, got %#v", n2.TypedValues()[1])
	}
	// MarshalData is deterministic and equal to json.Marshal(raw) — byte-identical
	// to the previous mapRecord representation, keeping snapshot-diff unchanged.
	d1, err := n.MarshalData()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := n.MarshalData()
	raw, _ := json.Marshal(n.Raw())
	if !bytes.Equal(d1, d2) || !bytes.Equal(d1, raw) {
		t.Errorf("MarshalData not byte-stable vs raw")
	}
}

// A provider that answered 429 named a time; the configured backoff must give
// way to it, and the retry budget must not — a rate limit postpones an attempt,
// it does not buy extra ones.
func TestScheduleRetryOrFailAfterHonoursProviderWait(t *testing.T) {
	ts := "2026-08-01T10:00:00Z"
	n := NewNotification("grp-1", "int-1", "usr-1", "telegram", "4242", "alert", ts, "")

	n.ScheduleRetryOrFailAfter(ts, "HTTP 429", 3, []int{1, 5}, 30*time.Second)

	if got := n.Status(); got != NotificationRetryScheduled {
		t.Fatalf("status = %q, want a scheduled retry", got)
	}
	if got := n.Raw()["next_retry_at"]; got != "2026-08-01T10:00:30+00:00" {
		t.Errorf("next_retry_at = %v, want the 30s the provider asked for, not the 1m default", got)
	}
}

func TestScheduleRetryOrFailAfterStillExhaustsTheBudget(t *testing.T) {
	raw := scheduledNotification()
	raw["retry_count"] = 2
	n := WrapNotification(raw)

	n.ScheduleRetryOrFailAfter("2026-08-01T10:00:00Z", "HTTP 429", 3, []int{1, 5}, 30*time.Second)

	if got := n.Status(); got != NotificationFailed {
		t.Errorf("status = %q, want failed: a rate limit postpones an attempt, it does not add one", got)
	}
}
