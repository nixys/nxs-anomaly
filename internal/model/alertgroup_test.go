package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// testActor stands in for an authenticated operator. These tests cover the
// state machine rather than attribution, so one fixed actor keeps the calls
// readable; attribution itself is asserted in TestTransitionsRecordActor.
var testActor = authz.Actor{
	ID:          "usr-test",
	Kind:        authz.KindUser,
	DisplayName: "tester",
	Role:        authz.RoleResponder,
}

func openGroup() map[string]any {
	return map[string]any{
		"id":     "grp1",
		"status": StatusOpen,
		"title":  "test group",
		"logs":   []any{},
	}
}

func lastLog(t *testing.T, raw map[string]any) map[string]any {
	t.Helper()
	logs, _ := raw["logs"].([]any)
	if len(logs) == 0 {
		t.Fatal("expected at least one log entry")
	}
	entry, _ := logs[len(logs)-1].(map[string]any)
	if entry == nil {
		t.Fatalf("log entry is not a map: %#v", logs[len(logs)-1])
	}
	return entry
}

func TestNewAlertGroupCanonicalShape(t *testing.T) {
	g := NewAlertGroup(NewAlertGroupParams{
		IntegrationID:        "int1",
		RouteID:              "rt1",
		EscalationChainID:    "ch1",
		DedupeKey:            "dk1",
		Title:                "CPU high",
		Severity:             "critical",
		Labels:               map[string]any{"env": "prod"},
		NotificationChannels: []string{"telegram"},
		EmergencyAlert:       true,
		Timestamp:            "ts0",
	})
	raw := g.Raw()

	id, _ := raw["id"].(string)
	if id == "" {
		t.Fatal("group must get a generated id")
	}
	delete(raw, "id")
	// Generated like the id, and checked the same way: its presence is the
	// contract, its value is not.
	if episode, _ := raw["episode_id"].(string); episode == "" {
		t.Fatal("group must get a generated episode id")
	}
	delete(raw, "episode_id")
	want := map[string]any{
		"integration_id":        "int1",
		"route_id":              "rt1",
		"escalation_chain_id":   "ch1",
		"dedupe_key":            "dk1",
		"title":                 "CPU high",
		"severity":              "critical",
		"status":                StatusOpen,
		"labels":                map[string]any{"env": "prod"},
		"notification_channels": []any{"telegram"},
		"emergency_alert":       true,
		"alert_ids":             []any{},
		"alert_count":           0,
		"epic_sent_at":          nil,
		"current_step":          0,
		"repeat_count":          0,
		"next_run_at":           "ts0",
		"acknowledged_at":       nil,
		"resolved_at":           nil,
		"created_at":            "ts0",
		"updated_at":            "ts0",
		"last_received_at":      "ts0",
		"logs":                  []any{},
	}
	if !reflect.DeepEqual(raw, want) {
		t.Errorf("canonical shape mismatch:\n got %#v\nwant %#v", raw, want)
	}
}

func TestNewAlertGroupNilDefaults(t *testing.T) {
	raw := NewAlertGroup(NewAlertGroupParams{Timestamp: "ts0"}).Raw()
	if labels, ok := raw["labels"].(map[string]any); !ok || labels == nil {
		t.Errorf("nil Labels must become empty map, got %#v", raw["labels"])
	}
	if channels, ok := raw["notification_channels"].([]any); !ok || len(channels) != 0 {
		t.Errorf("nil NotificationChannels must become empty []any, got %#v", raw["notification_channels"])
	}
	if raw["next_run_at"] != "ts0" {
		t.Errorf("new group must be due immediately, next_run_at = %v", raw["next_run_at"])
	}
}

func TestAcknowledgeMutatesRawInPlace(t *testing.T) {
	g := WrapAlertGroup(openGroup())
	if err := g.Acknowledge("2026-06-12T00:00:00Z", "acked", testActor); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	// The transition must be visible through the wrapper's rebuilt map.
	raw := g.Raw()
	if raw["status"] != StatusAcknowledged {
		t.Errorf("status = %v", raw["status"])
	}
	if raw["acknowledged_at"] != "2026-06-12T00:00:00Z" {
		t.Errorf("acknowledged_at = %v", raw["acknowledged_at"])
	}
	if raw["next_run_at"] != nil {
		t.Errorf("next_run_at must be nil, got %v", raw["next_run_at"])
	}
	if entry := lastLog(t, raw); entry["type"] != "acknowledged" || entry["message"] != "acked" {
		t.Errorf("log entry = %#v", entry)
	}
}

func TestAcknowledgeResolvedFails(t *testing.T) {
	raw := openGroup()
	raw["status"] = StatusResolved
	err := WrapAlertGroup(raw).Acknowledge("ts", "acked", testActor)
	if !errors.Is(err, ErrAcknowledgeResolved) {
		t.Fatalf("err = %v", err)
	}
	if raw["status"] != StatusResolved {
		t.Error("failed transition must not mutate the group")
	}
}

func TestAcknowledgeAcknowledgedAllowed(t *testing.T) {
	raw := openGroup()
	raw["status"] = StatusAcknowledged
	if err := WrapAlertGroup(raw).Acknowledge("ts", "acked again", testActor); err != nil {
		t.Fatalf("re-acknowledge must be allowed: %v", err)
	}
}

func TestResolveFromAnyStatus(t *testing.T) {
	for _, status := range []string{StatusOpen, StatusAcknowledged, StatusSilenced, StatusResolved} {
		raw := openGroup()
		raw["status"] = status
		g := WrapAlertGroup(raw)
		g.Resolve("ts1", "done", testActor)
		raw = g.Raw()
		if raw["status"] != StatusResolved || raw["resolved_at"] != "ts1" || raw["next_run_at"] != nil {
			t.Errorf("resolve from %s: %#v", status, raw)
		}
	}
}

func TestUnresolveGuardAndReset(t *testing.T) {
	raw := openGroup()
	if err := WrapAlertGroup(raw).Unresolve("ts", testActor); !errors.Is(err, ErrUnresolveNotResolved) {
		t.Fatalf("unresolve open group: err = %v", err)
	}

	raw["status"] = StatusResolved
	raw["resolved_at"] = "old"
	raw["current_step"] = 3
	g := WrapAlertGroup(raw)
	if err := g.Unresolve("ts2", testActor); err != nil {
		t.Fatalf("unresolve: %v", err)
	}
	raw = g.Raw()
	if raw["status"] != StatusOpen || raw["resolved_at"] != nil || raw["acknowledged_at"] != nil {
		t.Errorf("unresolve state: %#v", raw)
	}
	if raw["current_step"] != 0 {
		t.Errorf("current_step must reset to 0, got %v", raw["current_step"])
	}
	if raw["next_run_at"] != "ts2" {
		t.Errorf("next_run_at must resume escalation, got %v", raw["next_run_at"])
	}
}

func TestUnacknowledgeGuard(t *testing.T) {
	raw := openGroup()
	if err := WrapAlertGroup(raw).Unacknowledge("ts", testActor); !errors.Is(err, ErrUnacknowledgeNotAcked) {
		t.Fatalf("unacknowledge open group: err = %v", err)
	}
	raw["status"] = StatusAcknowledged
	g := WrapAlertGroup(raw)
	if err := g.Unacknowledge("ts2", testActor); err != nil {
		t.Fatalf("unacknowledge: %v", err)
	}
	raw = g.Raw()
	if raw["status"] != StatusOpen || raw["next_run_at"] != "ts2" {
		t.Errorf("unacknowledge state: %#v", raw)
	}
}

func TestSilence(t *testing.T) {
	g := WrapAlertGroup(openGroup())
	if err := g.Silence("ts", "until", "silenced", 30, testActor); err != nil {
		t.Fatalf("silence: %v", err)
	}
	raw := g.Raw()
	if raw["status"] != StatusSilenced || raw["silenced_at"] != "ts" || raw["silenced_until"] != "until" {
		t.Errorf("silence state: %#v", raw)
	}
	if entry := lastLog(t, raw); entry["data"].(map[string]any)["duration_minutes"] != 30 {
		t.Errorf("silence log: %#v", entry)
	}

	resolved := openGroup()
	resolved["status"] = StatusResolved
	if err := WrapAlertGroup(resolved).Silence("ts", "", "silenced", 0, testActor); !errors.Is(err, ErrSilenceResolved) {
		t.Fatalf("silence resolved: err = %v", err)
	}
}

func TestReopenOnNewAlert(t *testing.T) {
	raw := openGroup()
	raw["status"] = StatusAcknowledged
	raw["acknowledged_at"] = "earlier"
	g := WrapAlertGroup(raw)
	if !g.ReopenOnNewAlert() {
		t.Fatal("acknowledged group must reopen")
	}
	raw = g.Raw()
	if raw["status"] != StatusOpen || raw["acknowledged_at"] != nil {
		t.Errorf("reopen state: %#v", raw)
	}
	if entry := lastLog(t, raw); entry["type"] != "group_reopened" {
		t.Errorf("log entry = %#v", entry)
	}

	// No-op for non-acknowledged statuses.
	for _, status := range []string{StatusOpen, StatusResolved, StatusSilenced} {
		ng := WrapAlertGroup(map[string]any{"id": "grp1", "status": status})
		if ng.ReopenOnNewAlert() {
			t.Errorf("group with status %s must not reopen", status)
		}
		if ng.Status() != status {
			t.Errorf("no-op must not mutate status, got %v", ng.Status())
		}
	}
}

func TestAppendLogCap(t *testing.T) {
	g := WrapAlertGroup(openGroup())
	for i := 0; i < maxGroupLogs+50; i++ {
		g.AppendLog("tick", "entry", nil)
	}
	logs, _ := g.Raw()["logs"].([]any)
	if len(logs) != maxGroupLogs {
		t.Fatalf("logs len = %d, want %d", len(logs), maxGroupLogs)
	}
}

func TestAppendLogNilDataBecomesEmptyMap(t *testing.T) {
	g := WrapAlertGroup(openGroup())
	g.AppendLog("tick", "entry", nil)
	if entry := lastLog(t, g.Raw()); entry["data"] == nil {
		t.Error("nil data must be stored as empty map")
	}
}

func TestAlertGroupTypedAccessors(t *testing.T) {
	raw := map[string]any{
		"id":                    "grp1",
		"integration_id":        "int1",
		"escalation_chain_id":   "ch1",
		"dedupe_key":            "dk1",
		"title":                 "CPU high",
		"severity":              "critical",
		"status":                StatusOpen,
		"next_run_at":           "ts1",
		"last_received_at":      "ts2",
		"created_at":            "ts0",
		"epic_sent_at":          "ts3",
		"current_step":          float64(2), // as decoded from jsonb
		"alert_count":           float64(5),
		"repeat_count":          float64(1),
		"emergency_alert":       true,
		"alert_ids":             []any{"a1", "a2"},
		"notification_channels": []any{"telegram", "email"},
		"labels":                map[string]any{"env": "prod"},
	}
	g := WrapAlertGroup(raw)
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"IntegrationID", g.IntegrationID(), "int1"},
		{"EscalationChainID", g.EscalationChainID(), "ch1"},
		{"DedupeKey", g.DedupeKey(), "dk1"},
		{"Title", g.Title(), "CPU high"},
		{"Severity", g.Severity(), "critical"},
		{"NextRunAt", g.NextRunAt(), "ts1"},
		{"LastReceivedAt", g.LastReceivedAt(), "ts2"},
		{"CreatedAt", g.CreatedAt(), "ts0"},
		{"EpicSentAt", g.EpicSentAt(), "ts3"},
		{"CurrentStep", g.CurrentStep(), 2},
		{"AlertCount", g.AlertCount(), 5},
		{"RepeatCount", g.RepeatCount(), 1},
		{"EmergencyAlert", g.EmergencyAlert(), true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	if !reflect.DeepEqual(g.AlertIDs(), []string{"a1", "a2"}) {
		t.Errorf("AlertIDs = %#v", g.AlertIDs())
	}
	if !reflect.DeepEqual(g.NotificationChannels(), []string{"telegram", "email"}) {
		t.Errorf("NotificationChannels = %#v", g.NotificationChannels())
	}
	if !reflect.DeepEqual(g.Labels(), map[string]any{"env": "prod"}) {
		t.Errorf("Labels = %#v", g.Labels())
	}
}

// fullRawGroup returns a complete canonical alert group map (all promoted keys
// plus the Extra-backed nullable/optional keys), as a real persisted row has.
// alert_count/current_step/repeat_count are float64 to mirror a jsonb load.
func fullRawGroup() map[string]any {
	return map[string]any{
		"id":                    "g",
		"route_id":              "rt",
		"dedupe_key":            "dk",
		"status":                StatusOpen,
		"title":                 "title",
		"severity":              "warn",
		"created_at":            "ts",
		"updated_at":            "ts",
		"last_received_at":      "ts",
		"alert_count":           float64(1),
		"current_step":          float64(0),
		"repeat_count":          float64(0),
		"labels":                map[string]any{},
		"alert_ids":             []any{"a1"},
		"logs":                  []any{},
		"integration_id":        "int1",
		"escalation_chain_id":   "ch1",
		"notification_channels": []any{"telegram"},
		"emergency_alert":       false,
		"epic_sent_at":          nil,
		"next_run_at":           "ts",
		"acknowledged_at":       nil,
		"resolved_at":           nil,
		"vendor_extra":          "keep",
	}
}

// TestAlertGroupSettersMatchDirectWrites proves each setter produces exactly the
// same serialized representation the engine wrote before via group["field"] =
// value, so the store's snapshot-diff sees the same bytes. The comparison is on
// MarshalData (not the map identity the old raw-backed wrapper exposed), since
// the typed-fields wrapper copies the source map on Wrap. It also verifies that
// fields not modeled by the wrapper ("vendor_extra") survive untouched.
func TestAlertGroupSettersMatchDirectWrites(t *testing.T) {
	apply := func(viaSetter func(AlertGroup), direct func(map[string]any)) {
		t.Helper()
		g := WrapAlertGroup(fullRawGroup())
		viaSetter(g)
		got, err := g.MarshalData()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b := fullRawGroup()
		direct(b)
		want, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal want: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("setter result mismatch:\n got %s\nwant %s", got, want)
		}
		var gm map[string]any
		if err := json.Unmarshal(got, &gm); err != nil {
			t.Fatalf("unmarshal got: %v", err)
		}
		if gm["vendor_extra"] != "keep" {
			t.Errorf("unknown key not preserved: %#v", gm["vendor_extra"])
		}
	}
	apply(func(g AlertGroup) { g.SetSeverity("crit") }, func(m map[string]any) { m["severity"] = "crit" })
	apply(func(g AlertGroup) { g.SetTitle("t") }, func(m map[string]any) { m["title"] = "t" })
	apply(func(g AlertGroup) { g.SetUpdatedAt("ts") }, func(m map[string]any) { m["updated_at"] = "ts" })
	apply(func(g AlertGroup) { g.SetLastReceivedAt("ts") }, func(m map[string]any) { m["last_received_at"] = "ts" })
	apply(func(g AlertGroup) { g.SetEpicSentAt("ts") }, func(m map[string]any) { m["epic_sent_at"] = "ts" })
	apply(func(g AlertGroup) { g.SetCurrentStep(3) }, func(m map[string]any) { m["current_step"] = 3 })
	apply(func(g AlertGroup) { g.SetRepeatCount(2) }, func(m map[string]any) { m["repeat_count"] = 2 })
	apply(func(g AlertGroup) { g.SetNextRunAt("ts") }, func(m map[string]any) { m["next_run_at"] = "ts" })
	apply(func(g AlertGroup) { g.ClearNextRunAt() }, func(m map[string]any) { m["next_run_at"] = nil })
	apply(func(g AlertGroup) { g.MarkEmergency() }, func(m map[string]any) { m["emergency_alert"] = true })
	apply(func(g AlertGroup) { g.SetLabels(map[string]any{"k": "v"}) }, func(m map[string]any) { m["labels"] = map[string]any{"k": "v"} })
	apply(func(g AlertGroup) { g.SetNotificationChannels([]string{"telegram"}) }, func(m map[string]any) { m["notification_channels"] = []any{"telegram"} })
	apply(func(g AlertGroup) { g.AddAlertID("a2") }, func(m map[string]any) { m["alert_ids"] = []any{"a1", "a2"} })
	apply(func(g AlertGroup) { g.IncAlertCount() }, func(m map[string]any) { m["alert_count"] = 2 })
}

// TestAlertGroupWrapIsByteIdentityForFullShapes is the strict byte-stability
// gate the typed-fields backing must satisfy: for a real persisted row shape,
// WrapAlertGroup(m).MarshalData() must equal json.Marshal(m) exactly — no key
// added, dropped, or retyped. Unlike the round-trip test (which only checks
// MarshalData is idempotent under decode), this compares against the original
// row, so it catches toMap emitting a promoted key the row did not have. It
// covers the only two shapes the engine persists: canonical (NewAlertGroup) and
// direct-paging (CreateDirectPageGroup).
func TestAlertGroupWrapIsByteIdentityForFullShapes(t *testing.T) {
	directPaging := map[string]any{
		"id":               "ag-direct1",
		"integration_id":   nil,
		"integration_type": "direct_paging",
		"integration_name": "Direct Paging",
		"route_id":         "",
		"dedupe_key":       "dpk1",
		"status":           StatusOpen,
		"title":            "manual page",
		"description":      "manual page",
		"severity":         "unknown",
		"labels":           map[string]any{},
		"alert_count":      float64(1),
		"next_run_at":      nil,
		"alert_ids":        []any{},
		"current_step":     float64(0),
		"repeat_count":     float64(0),
		"logs":             []any{},
		"created_at":       "ts1",
		"updated_at":       "ts1",
		"last_received_at": "ts1",
	}
	for name, m := range map[string]map[string]any{
		"canonical":     fullRawGroup(),
		"direct_paging": directPaging,
	} {
		want, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("%s: marshal source: %v", name, err)
		}
		got, err := WrapAlertGroup(m).MarshalData()
		if err != nil {
			t.Fatalf("%s: marshal wrapped: %v", name, err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s: WrapAlertGroup not byte-identity\nsource:  %s\nwrapped: %s", name, want, got)
		}
	}
}

// TestAlertGroupByteStableRoundTrip locks the current MarshalData contract:
// for any decoded group, a re-marshal produces byte-identical JSON. This is
// the precondition the store's snapshot-diff relies on (untouched rows skip
// upsert) and the invariant that any future "typed fields + Extra" refactor
// of AlertGroup must preserve. If this test breaks, the diff will mark all
// loaded groups as changed on every cycle and rewrite them unnecessarily.
func TestAlertGroupByteStableRoundTrip(t *testing.T) {
	// Canonical-shape group from NewAlertGroup.
	new1 := NewAlertGroup(NewAlertGroupParams{
		IntegrationID: "int1", RouteID: "rt1", EscalationChainID: "ch1",
		DedupeKey: "dk1", Title: "CPU high", Severity: "critical",
		Labels: map[string]any{"env": "prod"}, NotificationChannels: []string{"telegram"},
		EmergencyAlert: true, Timestamp: "ts0",
	})
	// Direct-paging shape (engine/ingest.go::CreateDirectPageGroup): different
	// keys, integration_id=nil, no escalation_chain_id, no logs/alert_ids prefix
	// — must round-trip just as cleanly.
	direct := WrapAlertGroup(map[string]any{
		"id":               "ag-direct1",
		"integration_id":   nil,
		"integration_type": "direct_paging",
		"integration_name": "Direct Paging",
		"route_id":         "",
		"dedupe_key":       "dpk1",
		"status":           StatusOpen,
		"title":            "manual page",
		"description":      "manual page",
		"severity":         "unknown",
		"labels":           map[string]any{},
		"alert_count":      1,
		"next_run_at":      nil,
		"alert_ids":        []any{},
		"current_step":     0,
		"repeat_count":     0,
		"logs":             []any{},
		"created_at":       "ts1",
		"updated_at":       "ts1",
		"last_received_at": "ts1",
	})
	// Silenced group with extra fields the wrapper does not model directly.
	silenced := openGroup()
	silenced["status"] = StatusSilenced
	silenced["silenced_at"] = "ts2"
	silenced["silenced_until"] = "ts3"
	silenced["vendor_extra"] = map[string]any{"k": "v"}
	silencedG := WrapAlertGroup(silenced)

	for name, g := range map[string]AlertGroup{
		"canonical":     new1,
		"direct_paging": direct,
		"silenced":      silencedG,
	} {
		// Marshal → Unmarshal (as the store does on load via decodeRecord)
		// → Marshal again must yield identical bytes. This is the contract
		// snapshot-diff relies on; the same property guides any future
		// typed-fields refactor (extra/unknown keys must be preserved).
		b1, err := g.MarshalData()
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b1, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		b2, err := WrapAlertGroup(m).MarshalData()
		if err != nil {
			t.Fatalf("%s: re-marshal: %v", name, err)
		}
		if !bytes.Equal(b1, b2) {
			t.Errorf("%s: round-trip not byte-stable\nbefore: %s\nafter:  %s", name, b1, b2)
		}
	}
}

func TestAlertGroupRecordImpl(t *testing.T) {
	g := NewAlertGroup(NewAlertGroupParams{
		IntegrationID: "int1", RouteID: "rt1", EscalationChainID: "ch1",
		DedupeKey: "dk1", Title: "t", Severity: "critical", Timestamp: "ts0",
	})
	if g.RecordID() != g.ID() || g.RecordID() == "" {
		t.Errorf("RecordID = %q, want non-empty == ID", g.RecordID())
	}
	// TypedValues must mirror the store's typedValues("alert_groups", ...) order.
	want := []any{"int1", "rt1", "ch1", "dk1", "open", "critical", "ts0", "ts0", nil, nil, 0, nil}
	if got := g.TypedValues(); !reflect.DeepEqual(got, want) {
		t.Errorf("TypedValues = %#v\nwant %#v", got, want)
	}
	// MarshalData is deterministic and equals json.Marshal(raw) (byte-identical
	// to the previous mapRecord representation → snapshot-diff unchanged).
	d1, err := g.MarshalData()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := g.MarshalData()
	raw, _ := json.Marshal(g.Raw())
	if !bytes.Equal(d1, d2) || !bytes.Equal(d1, raw) {
		t.Errorf("MarshalData not byte-stable vs raw")
	}
}

// TestTransitionsRecordActor pins the attribution the backlog item asked for:
// after a transition it must be possible to say who performed it, both from the
// group's own fields and from the timeline entry.
func TestTransitionsRecordActor(t *testing.T) {
	raw := openGroup()
	g := WrapAlertGroup(raw)
	if err := g.Acknowledge("2026-01-01T00:00:00+00:00", "ack", testActor); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	ref, ok := g.AcknowledgedBy().(map[string]any)
	if !ok {
		t.Fatalf("AcknowledgedBy() = %#v, want an actor reference", g.AcknowledgedBy())
	}
	if ref["id"] != testActor.ID || ref["name"] != testActor.DisplayName {
		t.Errorf("acknowledged_by = %#v, want id=%q name=%q", ref, testActor.ID, testActor.DisplayName)
	}
	if ref["role"] != string(testActor.Role) || ref["kind"] != testActor.Kind {
		t.Errorf("acknowledged_by = %#v, want role=%q kind=%q", ref, testActor.Role, testActor.Kind)
	}

	entry := lastLog(t, g.Raw())
	actorEntry, ok := entry["actor"].(map[string]any)
	if !ok {
		t.Fatalf("timeline entry has no actor: %#v", entry)
	}
	if actorEntry["id"] != testActor.ID {
		t.Errorf("timeline actor id = %v, want %q", actorEntry["id"], testActor.ID)
	}
}

// TestSystemTransitionsHaveNoAttribution: an unattended transition must not
// invent a principal. A null reads as "nobody did this"; a fake record would
// read as "somebody did".
func TestSystemTransitionsHaveNoAttribution(t *testing.T) {
	g := WrapAlertGroup(openGroup())
	g.Resolve("2026-01-01T00:00:00+00:00", "by source", authz.SystemActor)

	if ref := g.ResolvedBy(); ref != nil {
		t.Errorf("resolved_by = %#v, want nil for the system actor", ref)
	}
	entry := lastLog(t, g.Raw())
	actorEntry, ok := entry["actor"].(map[string]any)
	if !ok {
		t.Fatalf("timeline entry has no actor: %#v", entry)
	}
	if actorEntry["kind"] != authz.KindSystem {
		t.Errorf("timeline actor kind = %v, want %q", actorEntry["kind"], authz.KindSystem)
	}
}

// TestUnresolveClearsAttribution: reopening a group must not leave the previous
// resolver attached, or the UI would show a resolver for an open alert.
func TestUnresolveClearsAttribution(t *testing.T) {
	g := WrapAlertGroup(openGroup())
	g.Resolve("2026-01-01T00:00:00+00:00", "done", testActor)
	if g.ResolvedBy() == nil {
		t.Fatal("precondition: resolve should have recorded an actor")
	}
	if err := g.Unresolve("2026-01-01T01:00:00+00:00", testActor); err != nil {
		t.Fatalf("Unresolve: %v", err)
	}
	if ref := g.ResolvedBy(); ref != nil {
		t.Errorf("resolved_by after unresolve = %#v, want nil", ref)
	}
}

// A group can be lived through more than once. The episode is what separates
// those passes, and getting it wrong does not fail loudly: it silently averages
// two responses into one and reports a number nobody can reproduce.

func TestReopeningAnAcknowledgedGroupStartsANewEpisode(t *testing.T) {
	g := NewAlertGroup(NewAlertGroupParams{Timestamp: "ts0"})
	first := g.EpisodeID()
	if err := g.Acknowledge("ts1", "acked", authz.SystemActor); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	if !g.ReopenOnNewAlert() {
		t.Fatal("an acknowledged group did not reopen")
	}

	// Keeping the first episode would let the second acknowledgement overwrite
	// the first one's MTTA, and the incident that came back would disappear
	// into the one that preceded it.
	if g.EpisodeID() == first {
		t.Error("reopening kept the previous episode")
	}
	if g.EpisodeID() == "" {
		t.Error("reopening left the group without an episode")
	}
}

func TestUnresolvingStartsANewEpisode(t *testing.T) {
	g := NewAlertGroup(NewAlertGroupParams{Timestamp: "ts0"})
	g.Resolve("ts1", "done", authz.SystemActor)
	first := g.EpisodeID()

	if err := g.Unresolve("ts2", authz.SystemActor); err != nil {
		t.Fatalf("Unresolve: %v", err)
	}

	if g.EpisodeID() == first {
		t.Error("unresolving kept the previous episode, so the next resolve rewrites its MTTR")
	}
}

func TestAcknowledgingDoesNotChangeTheEpisode(t *testing.T) {
	g := NewAlertGroup(NewAlertGroupParams{Timestamp: "ts0"})
	first := g.EpisodeID()
	if err := g.Acknowledge("ts1", "acked", authz.SystemActor); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	// An acknowledgement is an event inside the episode, not a new one. A fresh
	// episode here would leave every episode with no acknowledgement in it.
	if g.EpisodeID() != first {
		t.Error("acknowledging started a new episode")
	}
}

func TestAGroupFromBeforeEpisodesGetsOneLazily(t *testing.T) {
	// Groups created before this shipped have no episode_id. They are given one
	// on their first transition rather than by a migration: the ones that matter
	// are the ones still being worked on.
	g := WrapAlertGroup(map[string]any{"id": "grp_old", "status": StatusOpen})
	if g.EpisodeID() != "" {
		t.Fatal("test fixture already has an episode")
	}

	assigned := g.EnsureEpisodeID()
	if assigned == "" {
		t.Fatal("EnsureEpisodeID returned nothing")
	}
	if g.EpisodeID() != assigned {
		t.Errorf("assignment did not stick: %q vs %q", g.EpisodeID(), assigned)
	}
	// Called twice, it must not invent a second episode for the same pass.
	if again := g.EnsureEpisodeID(); again != assigned {
		t.Errorf("EnsureEpisodeID is not idempotent: %q then %q", assigned, again)
	}
}
