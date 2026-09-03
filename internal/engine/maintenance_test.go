package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// A maintenance window is a promise that nobody gets woken for work we are
// doing on purpose. Two things have to hold for it to be worth having: during
// the window nothing pages, and outside it everything still does. The second is
// the one that is easy to break and impossible to notice.

// seedWindow adds a window covering the given integrations over an offset range
// from now.
func seedWindow(ms *memStore, id string, from, to time.Duration, integrationIDs ...string) {
	now := utils.UTCNow()
	ids := make([]any, 0, len(integrationIDs))
	for _, iid := range integrationIDs {
		ids = append(ids, iid)
	}
	ms.seed("maintenance_windows", map[string]any{
		"id":              id,
		"name":            "planned work",
		"integration_ids": ids,
		"starts_at":       utils.ToISO(now.Add(from)),
		"ends_at":         utils.ToISO(now.Add(to)),
	})
}

// ingestStore seeds one integration whose default route pages immediately.
func ingestStore() *memStore {
	ms := newMemStore()
	ms.seed("users", map[string]any{
		"id": "usr-1", "name": "Alice", "on_duty": true, "telegram_id": "42",
	})
	ms.seed("escalation_chains", map[string]any{
		"id": "chain-1", "name": "chain",
		"steps": []any{map[string]any{"kind": StepNotifyUser, "user_ids": []any{"usr-1"}}},
	})
	ms.seed("integrations", map[string]any{
		"id": "int-1", "name": "prod", "key": "key-1", "routing_key": "key-1",
		"group_by": []any{"alertname"},
		"routes": []any{map[string]any{
			"id": "r1", "name": "default", "match_type": "all",
			"is_default": true, "escalation_chain_id": "chain-1",
		}},
	})
	return ms
}

func ingestOne(t *testing.T, e *Engine, title string) map[string]any {
	t.Helper()
	res, err := e.IngestAlert(context.Background(), "key-1", map[string]any{
		"title": title, "severity": "critical",
		"labels": map[string]any{"alertname": title},
	})
	if err != nil {
		t.Fatalf("IngestAlert: %v", err)
	}
	group, _ := res["group"].(map[string]any)
	if group == nil {
		t.Fatalf("ingest returned no group: %v", res)
	}
	return group
}

func TestAlertDuringMaintenanceIsRecordedButNotEscalated(t *testing.T) {
	ms := ingestStore()
	seedWindow(ms, "mnt-1", -time.Hour, time.Hour, "int-1")
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")

	if got := utils.StrVal(group, "status"); got != model.StatusSilenced {
		t.Fatalf("group status = %q, want %q", got, model.StatusSilenced)
	}
	// Recorded, not dropped: after the window this is how somebody finds out
	// that the deploy broke something unrelated at 02:40.
	if len(ms.data["alerts"]) != 1 {
		t.Errorf("stored %d alert(s), want 1", len(ms.data["alerts"]))
	}
	if n := len(ms.data["notifications"]); n != 0 {
		t.Errorf("created %d notification(s) during maintenance, want 0", n)
	}
	// next_run_at is what the worker's due-scan reads; a silenced group is
	// excluded by status, but leaving a due time set would page the moment the
	// silence expires for a group nobody is looking at.
	if until := utils.StrVal(group, "silenced_until"); until == "" {
		t.Error("silenced_until is empty, so the silence would never end")
	}
}

func TestAlertOutsideMaintenanceStillPages(t *testing.T) {
	ms := ingestStore()
	// The window ended a minute ago. This is the assertion that makes the
	// feature safe to ship: suppression that does not stop is indistinguishable
	// from a broken alerting pipeline, and both are silent.
	seedWindow(ms, "mnt-1", -2*time.Hour, -time.Minute, "int-1")
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")

	if got := utils.StrVal(group, "status"); got != model.StatusOpen {
		t.Fatalf("group status = %q, want %q", got, model.StatusOpen)
	}
	if n := len(ms.data["notifications"]); n == 0 {
		t.Error("no notification created outside the window")
	}
}

func TestWindowForAnotherIntegrationDoesNotSilence(t *testing.T) {
	ms := ingestStore()
	seedWindow(ms, "mnt-1", -time.Hour, time.Hour, "int-other")
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")

	// A window names the integrations it covers. Covering everything by accident
	// is the failure that produces a deployment which pages nobody.
	if got := utils.StrVal(group, "status"); got != model.StatusOpen {
		t.Fatalf("group status = %q, want %q", got, model.StatusOpen)
	}
}

func TestFutureWindowDoesNotSilenceYet(t *testing.T) {
	ms := ingestStore()
	seedWindow(ms, "mnt-1", time.Hour, 2*time.Hour, "int-1")
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")

	if got := utils.StrVal(group, "status"); got != model.StatusOpen {
		t.Fatalf("group status = %q, want %q", got, model.StatusOpen)
	}
}

func TestSilenceLastsUntilTheWindowThatEndsLast(t *testing.T) {
	ms := ingestStore()
	// A long window for a datacentre move, a short one for a service inside it.
	seedWindow(ms, "mnt-short", -time.Hour, 10*time.Minute, "int-1")
	seedWindow(ms, "mnt-long", -time.Hour, 4*time.Hour, "int-1")
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")

	until, err := utils.ParseDatetime(utils.StrVal(group, "silenced_until"))
	if err != nil {
		t.Fatalf("silenced_until unparseable: %v", err)
	}
	// Taking the first window found would end the silence three hours early,
	// mid-maintenance, which is worse than having no window at all: it pages
	// during the noisiest part of the work.
	if remaining := time.Until(until); remaining < 3*time.Hour {
		t.Errorf("silence ends in %s, want the later window's ~4h", remaining.Round(time.Minute))
	}
}

func TestMalformedWindowSuppressesNothing(t *testing.T) {
	ms := ingestStore()
	ms.seed("maintenance_windows", map[string]any{
		"id": "mnt-bad", "name": "typo",
		"integration_ids": []any{"int-1"},
		"starts_at":       "not a timestamp",
		"ends_at":         "also not",
	})
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")

	// Failing open is the deliberate choice: a row nobody can read must not
	// silently swallow pages.
	if got := utils.StrVal(group, "status"); got != model.StatusOpen {
		t.Fatalf("group status = %q, want %q", got, model.StatusOpen)
	}
}

func TestHeartbeatIgnoresASourceUnderMaintenance(t *testing.T) {
	ms := heartbeatStore(t, 60, 10*time.Minute)
	seedWindow(ms, "mnt-1", -time.Hour, time.Hour, "int-1")
	e := crudEngine(ms)

	silent, err := e.ProcessSourceHeartbeats(context.Background())
	if err != nil {
		t.Fatalf("ProcessSourceHeartbeats: %v", err)
	}
	// A source taken down for planned work is a silent source. This is the case
	// the feature exists for: without it, every maintenance raises SourceSilent
	// at the moment the people who would answer it are causing it.
	if silent != 0 {
		t.Errorf("reported %d silent source(s) under maintenance, want 0", silent)
	}
	if raised := silenceAlerts(ms); len(raised) != 0 {
		t.Errorf("raised %d SourceSilent alert(s) under maintenance, want 0", len(raised))
	}
}

func TestHeartbeatReportsSilenceOnceTheWindowHasPassed(t *testing.T) {
	ms := heartbeatStore(t, 60, 10*time.Minute)
	seedWindow(ms, "mnt-1", -2*time.Hour, -time.Minute, "int-1")
	e := crudEngine(ms)

	silent, err := e.ProcessSourceHeartbeats(context.Background())
	if err != nil {
		t.Fatalf("ProcessSourceHeartbeats: %v", err)
	}
	// A source that never came back after the maintenance is exactly what the
	// dead-man switch is for, and the window must not keep hiding it.
	if silent != 1 {
		t.Fatalf("reported %d silent source(s) after the window, want 1", silent)
	}
	if raised := silenceAlerts(ms); len(raised) != 1 {
		t.Errorf("raised %d SourceSilent alert(s), want 1", len(raised))
	}
}

// ── Validation ────────────────────────────────────────────────────────────────

func TestWindowMustEndAfterItStarts(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	now := utils.UTCNow()

	_, err := e.CreateMaintenanceWindow(context.Background(), map[string]any{
		"name":            "backwards",
		"integration_ids": []any{"int-1"},
		"starts_at":       utils.ToISO(now.Add(time.Hour)),
		"ends_at":         utils.ToISO(now),
	})
	// A window that ends before it starts covers nothing, which would be
	// discovered during the maintenance it was supposed to cover.
	if err == nil {
		t.Fatal("accepted a window ending before it starts")
	}
}

func TestWindowMustNameAnIntegration(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	now := utils.UTCNow()

	for _, tc := range []struct {
		name string
		ids  any
	}{
		{"absent", nil},
		{"empty", []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"name":      "everything",
				"starts_at": utils.ToISO(now),
				"ends_at":   utils.ToISO(now.Add(time.Hour)),
			}
			if tc.ids != nil {
				payload["integration_ids"] = tc.ids
			}
			// "Empty means everything" would make one typo silence the whole
			// deployment, and the only symptom is nobody being paged.
			if _, err := e.CreateMaintenanceWindow(context.Background(), payload); err == nil {
				t.Fatal("accepted a window covering no integration")
			}
		})
	}
}

func TestWindowRejectsAnUnknownIntegration(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	now := utils.UTCNow()

	_, err := e.CreateMaintenanceWindow(context.Background(), map[string]any{
		"name":            "typo",
		"integration_ids": []any{"int-1", "int-typo"},
		"starts_at":       utils.ToISO(now),
		"ends_at":         utils.ToISO(now.Add(time.Hour)),
	})
	// A mistyped id silences nothing and says nothing; the window looks correct
	// in the list right up until the maintenance pages everyone.
	if err == nil {
		t.Fatal("accepted a window naming an unknown integration")
	}
}

func TestCreatedWindowSilencesImmediately(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	now := utils.UTCNow()

	if _, err := e.CreateMaintenanceWindow(context.Background(), map[string]any{
		"name":            "rolling restart",
		"integration_ids": []any{"int-1"},
		"starts_at":       utils.ToISO(now.Add(-time.Minute)),
		"ends_at":         utils.ToISO(now.Add(time.Hour)),
	}); err != nil {
		t.Fatalf("CreateMaintenanceWindow: %v", err)
	}

	// End to end through the public API rather than a seeded row: a window that
	// is created but not consulted is the whole feature failing quietly.
	group := ingestOne(t, e, "disk full")
	if got := utils.StrVal(group, "status"); got != model.StatusSilenced {
		t.Fatalf("group status = %q, want %q", got, model.StatusSilenced)
	}
}

// epicStore seeds an integration that pages a named person as soon as a group
// accumulates one alert — the storm signal, which is the one notification that
// does not go through the escalation chain and so is not stopped by silencing.
func epicStore() *memStore {
	ms := ingestStore()
	integ := ms.data["integrations"]["int-1"]
	integ["notification_policy"] = map[string]any{
		"epic_user_id":         "usr-1",
		"epic_threshold_count": 1,
	}
	return ms
}

func epicNotifications(ms *memStore) int {
	n := 0
	for _, row := range ms.data["notifications"] {
		if reason, _ := row["reason"].(string); len(reason) >= 4 && reason[:4] == "epic" {
			n++
		}
	}
	return n
}

func TestEpicThresholdStillFiresOutsideAWindow(t *testing.T) {
	ms := epicStore()
	e := crudEngine(ms)

	ingestOne(t, e, "disk full")

	if got := epicNotifications(ms); got == 0 {
		t.Fatal("no epic notification outside a window — the control for the test below")
	}
}

func TestEpicThresholdIsSuppressedDuringMaintenance(t *testing.T) {
	ms := epicStore()
	seedWindow(ms, "mnt-1", -time.Hour, time.Hour, "int-1")
	e := crudEngine(ms)

	ingestOne(t, e, "disk full")

	// The epic threshold pages a named person directly, bypassing the chain, so
	// silencing the group does not stop it. A storm is exactly what maintenance
	// produces, and this is the one page a window would otherwise still deliver.
	if got := epicNotifications(ms); got != 0 {
		t.Errorf("sent %d epic notification(s) during maintenance, want 0", got)
	}
}

// A window is planned work, and plans get cancelled. The UI has always offered
// the delete button; the engine's allow-list did not carry the collection, so
// the router's DELETE route reached a bare error and the browser was told
// "Internal Error" for an operation the API documents.
func TestDeleteMaintenanceWindow(t *testing.T) {
	ms := newMemStore()
	seedWindow(ms, "mnt-1", -time.Hour, time.Hour, "int-1")
	e := crudEngine(ms)

	res, err := e.DeleteEntity(context.Background(), "maintenance_windows", "mnt-1")
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if res["deleted"] != true {
		t.Errorf("deleted = %v, want true", res["deleted"])
	}
	if _, err := e.GetItem(context.Background(), "maintenance_windows", "mnt-1"); err == nil {
		t.Error("window still readable after delete")
	}
}
