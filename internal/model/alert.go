package model

import (
	"encoding/json"
	"fmt"

	"github.com/nixys/nxs-anomaly/internal/store"
)

func init() {
	store.RegisterRecordWrapper("alerts", func(m map[string]any) store.Record {
		return WrapAlert(m)
	})
}

// Alert statuses. An alert reports what its source last said about it, until
// its group's lifecycle overrides that: closing a group closes its alerts and
// reopening it reopens them (store.SetAlertStatusForGroups).
const (
	AlertStatusFiring   = "firing"
	AlertStatusResolved = "resolved"
)

// alertData is the typed backing for an alert (Step B). Alerts are written once
// at ingest and afterwards only their status changes, and only through the
// store's group-wide update — so only the stable always-present scalars are
// promoted; alert_group_id (set after the group is resolved, absent for
// group-less resolves), the container fields and the nullable timestamps stay in
// Extra so the row round-trips byte-for-byte.
type alertData struct {
	ID            string
	IntegrationID string
	RouteID       string
	Status        string
	Severity      string
	Title         string
	Message       string
	Source        string
	Fingerprint   string
	ReceivedAt    string
	Extra         map[string]any
}

func newAlertData(m map[string]any) *alertData {
	d := &alertData{Extra: make(map[string]any, len(m))}
	for k, v := range m {
		switch k {
		case "id":
			d.ID = asString(v)
		case "integration_id":
			d.IntegrationID = asString(v)
		case "route_id":
			d.RouteID = asString(v)
		case "status":
			d.Status = asString(v)
		case "severity":
			d.Severity = asString(v)
		case "title":
			d.Title = asString(v)
		case "message":
			d.Message = asString(v)
		case "source":
			d.Source = asString(v)
		case "fingerprint":
			d.Fingerprint = asString(v)
		case "received_at":
			d.ReceivedAt = asString(v)
		default:
			d.Extra[k] = v
		}
	}
	return d
}

func (d *alertData) toMap() map[string]any {
	m := make(map[string]any, len(d.Extra)+10)
	m["id"] = d.ID
	m["integration_id"] = d.IntegrationID
	m["route_id"] = d.RouteID
	m["status"] = d.Status
	m["severity"] = d.Severity
	m["title"] = d.Title
	m["message"] = d.Message
	m["source"] = d.Source
	m["fingerprint"] = d.Fingerprint
	m["received_at"] = d.ReceivedAt
	for k, v := range d.Extra {
		m[k] = v
	}
	return m
}

// Alert is a typed view over an alert. Backed by a pointer to typed fields so
// copies of an Alert alias the same data.
type Alert struct {
	d *alertData
}

// Alert is held in State as a store.Record (typed collection).
var _ store.Record = Alert{}

// WrapAlert wraps an existing raw alert map into typed fields. The map must not
// be nil.
func WrapAlert(raw map[string]any) Alert { return Alert{d: newAlertData(raw)} }

// Raw returns a freshly rebuilt map view of the alert (a copy, not the live
// backing store).
func (a Alert) Raw() map[string]any { return a.d.toMap() }

func (a Alert) ID() string { return a.d.ID }

// store.Record implementation.

func (a Alert) RecordID() string             { return a.ID() }
func (a Alert) MarshalData() ([]byte, error) { return json.Marshal(a.d.toMap()) }

// MarshalJSON surfaces the rebuilt map directly to encoding/json, so print-state
// and similar callers serialize the row's data, not an empty struct.
func (a Alert) MarshalJSON() ([]byte, error) { return a.MarshalData() }

// TypedValues mirrors the store's typedValues("alerts", ...) exactly:
// integration_id, route_id, status, severity, received_at, alert_group_id.
// Empty/nil string columns become NULL.
func (a Alert) TypedValues() []any {
	ev := func(k string) any {
		v, ok := a.d.Extra[k]
		if !ok || v == nil || v == "" {
			return nil
		}
		return fmt.Sprintf("%v", v)
	}
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return []any{
		nz(a.d.IntegrationID), nz(a.d.RouteID), nz(a.d.Status),
		nz(a.d.Severity), nz(a.d.ReceivedAt), ev("alert_group_id"),
	}
}
