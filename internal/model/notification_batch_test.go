package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestNotificationBatchConstructorShape(t *testing.T) {
	b := NewNotificationBatch("int1:dk1", "int1", "grp1", "tsFlush", "tsDeadline", "ts0")
	if b.ID == "" {
		t.Fatal("batch must get a generated id")
	}
	got := b.ToMap()
	delete(got, "id")
	want := map[string]any{
		"batch_key":          "int1:dk1",
		"integration_id":     "int1",
		"alert_group_id":     "grp1",
		"status":             "open",
		"flush_at":           "tsFlush",
		"deadline_at":        "tsDeadline",
		"notification_count": 0,
		"created_at":         "ts0",
		"updated_at":         "ts0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("constructor shape mismatch:\n got %#v\nwant %#v", got, want)
	}
}

// TestNotificationBatchRoundTripStable is the persistence-core guarantee: a row
// decoded from jsonb and re-encoded must be byte-deterministic and lose no keys,
// including the optional flushed_at and any unmodeled (forward-compat) field.
func TestNotificationBatchRoundTripStable(t *testing.T) {
	raw := []byte(`{"id":"nbat1","batch_key":"int1:dk1","integration_id":"int1",` +
		`"alert_group_id":"grp1","status":"closed","flush_at":"ts1","deadline_at":"ts2",` +
		`"notification_count":3,"created_at":"ts0","updated_at":"ts3","flushed_at":"ts3","vendor_x":"keep"}`)
	var b NotificationBatch
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	enc1, err := b.MarshalData()
	if err != nil {
		t.Fatal(err)
	}
	enc2, _ := b.MarshalData()
	if !bytes.Equal(enc1, enc2) {
		t.Fatal("MarshalData is not deterministic")
	}
	var origMap, encMap map[string]any
	_ = json.Unmarshal(raw, &origMap)
	_ = json.Unmarshal(enc1, &encMap)
	if !reflect.DeepEqual(origMap, encMap) {
		t.Errorf("round-trip changed content:\n orig %#v\n enc  %#v", origMap, encMap)
	}
	if b.Extra["flushed_at"] != "ts3" || b.Extra["vendor_x"] != "keep" {
		t.Errorf("Extra not preserved: %#v", b.Extra)
	}
}

func TestNotificationBatchTypedValues(t *testing.T) {
	b := NewNotificationBatch("int1:dk1", "int1", "grp1", "ts1", "ts2", "ts0")
	got := b.TypedValues()
	want := []any{"int1:dk1", "open", "ts1", "ts2", "grp1", "int1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TypedValues = %#v, want %#v", got, want)
	}
	// Empty fields become NULL (nil).
	empty := &NotificationBatch{}
	for i, v := range empty.TypedValues() {
		if v != nil {
			t.Errorf("empty TypedValues[%d] = %#v, want nil", i, v)
		}
	}
}

func TestNotificationBatchTransitions(t *testing.T) {
	b := NewNotificationBatch("k", "int1", "grp1", "ts1", "ts2", "ts0")
	b.IncCount()
	b.IncCount()
	if b.NotificationCount != 2 {
		t.Errorf("count = %d, want 2", b.NotificationCount)
	}
	b.Touch("ts5", "ts5")
	if b.FlushAt != "ts5" || b.UpdatedAt != "ts5" || !b.IsOpen() {
		t.Errorf("Touch failed: %#v", b.ToMap())
	}
	b.Close("ts9", 7)
	if b.IsOpen() || b.Status != "closed" || b.NotificationCount != 7 ||
		b.UpdatedAt != "ts9" || b.Extra["flushed_at"] != "ts9" {
		t.Errorf("Close failed: %#v", b.ToMap())
	}
}
