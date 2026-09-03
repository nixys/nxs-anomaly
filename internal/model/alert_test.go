package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// TestAlertWrapIsByteIdentityForFullShape is the strict byte-stability gate for
// the typed-fields backing: for a real ingested alert row (the shape built in
// engine/ingest.go, including alert_group_id), WrapAlert(m).MarshalData() must
// equal json.Marshal(m) exactly — no key added, dropped, or retyped.
func TestAlertWrapIsByteIdentityForFullShape(t *testing.T) {
	full := map[string]any{
		"id":             "alr1",
		"integration_id": "int1",
		"route_id":       "rt1",
		"status":         "firing",
		"title":          "CPU high",
		"message":        "cpu over 90%",
		"severity":       "critical",
		"labels":         map[string]any{"env": "prod"},
		"payload":        map[string]any{"raw": "x"},
		"annotations":    map[string]any{"summary": "s"},
		"fingerprint":    "fp1",
		"starts_at":      "ts0",
		"ends_at":        nil,
		"generator_url":  nil,
		"source":         "webhook",
		"received_at":    "ts0",
		"alert_group_id": "grp1",
	}
	want, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	got, err := WrapAlert(full).MarshalData()
	if err != nil {
		t.Fatalf("marshal wrapped: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("WrapAlert not byte-identity\nsource:  %s\nwrapped: %s", want, got)
	}
}

func TestAlertRecordImpl(t *testing.T) {
	raw := map[string]any{
		"id":             "alr1",
		"integration_id": "int1",
		"route_id":       "rt1",
		"status":         "firing",
		"severity":       "critical",
		"received_at":    "ts0",
		"alert_group_id": "grp1",
		"title":          "CPU high",
	}
	a := WrapAlert(raw)
	if a.RecordID() != "alr1" {
		t.Errorf("RecordID = %q, want alr1", a.RecordID())
	}
	// TypedValues mirrors store.typedValues("alerts", ...) order:
	// integration_id, route_id, status, severity, received_at, alert_group_id.
	want := []any{"int1", "rt1", "firing", "critical", "ts0", "grp1"}
	if got := a.TypedValues(); !reflect.DeepEqual(got, want) {
		t.Errorf("TypedValues = %#v\nwant %#v", got, want)
	}
	// Missing/nil/empty typed columns surface as nil.
	a2 := WrapAlert(map[string]any{"id": "alr2", "status": "resolved", "received_at": "ts1"})
	want2 := []any{nil, nil, "resolved", nil, "ts1", nil}
	if got := a2.TypedValues(); !reflect.DeepEqual(got, want2) {
		t.Errorf("TypedValues (sparse) = %#v\nwant %#v", got, want2)
	}
	// MarshalData is deterministic and equal to json.Marshal(raw) — byte-identical
	// to the previous mapRecord representation, keeping snapshot-diff unchanged.
	d1, err := a.MarshalData()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := a.MarshalData()
	rawJSON, _ := json.Marshal(a.Raw())
	if !bytes.Equal(d1, d2) || !bytes.Equal(d1, rawJSON) {
		t.Errorf("MarshalData not byte-stable vs raw")
	}
}
