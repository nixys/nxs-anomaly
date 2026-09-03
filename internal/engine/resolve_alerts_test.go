package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Closing a group used to leave its alerts reading "firing" for good: an alert
// was written once at ingest and nothing afterwards touched its status. The
// operator saw the group close and the alerts it contained stay active, on the
// alerts list and on the group's own alerts tab.

// alertStatuses returns the statuses of the alerts belonging to a group.
func alertStatuses(ms *memStore, groupID string) []string {
	var out []string
	for _, row := range ms.data["alerts"] {
		if utils.StrVal(row, "alert_group_id") == groupID {
			out = append(out, utils.StrVal(row, "status"))
		}
	}
	return out
}

func requireAllStatuses(t *testing.T, ms *memStore, groupID, want string) {
	t.Helper()
	got := alertStatuses(ms, groupID)
	if len(got) == 0 {
		t.Fatalf("group %s has no alerts to check", groupID)
	}
	for _, s := range got {
		if s != want {
			t.Fatalf("alert statuses = %v, want every one %q", got, want)
		}
	}
}

func TestResolvingGroupClosesItsAlerts(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	group := ingestOne(t, e, "disk full")
	id := group["id"].(string)
	requireAllStatuses(t, ms, id, model.AlertStatusFiring)

	if _, err := e.ResolveGroup(adminCtx(), id); err != nil {
		t.Fatalf("ResolveGroup: %v", err)
	}
	requireAllStatuses(t, ms, id, model.AlertStatusResolved)
}

func TestUnresolvingGroupReopensItsAlerts(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	id := ingestOne(t, e, "disk full")["id"].(string)

	if _, err := e.ResolveGroup(adminCtx(), id); err != nil {
		t.Fatalf("ResolveGroup: %v", err)
	}
	// Asserted before the reopen so this test cannot pass by the alerts having
	// never been closed in the first place.
	requireAllStatuses(t, ms, id, model.AlertStatusResolved)
	if _, err := e.UnresolveGroup(adminCtx(), id); err != nil {
		t.Fatalf("UnresolveGroup: %v", err)
	}
	// Symmetric on purpose: a reopened group is being worked again, and an
	// alerts list that still calls its alerts closed says the opposite.
	requireAllStatuses(t, ms, id, model.AlertStatusFiring)
}

func TestBulkResolveClosesTheAlertsOfEveryGroup(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	first := ingestOne(t, e, "disk full")["id"].(string)
	second := ingestOne(t, e, "cpu hot")["id"].(string)

	if _, err := e.BulkResolveGroups(adminCtx(), []string{first, second}); err != nil {
		t.Fatalf("BulkResolveGroups: %v", err)
	}
	requireAllStatuses(t, ms, first, model.AlertStatusResolved)
	requireAllStatuses(t, ms, second, model.AlertStatusResolved)
}

// A resolving event from the source closes the group, but only the event that
// just arrived carries the resolved status — the alerts already in the group
// are the ones that would otherwise stay firing.
func TestSourceResolveClosesTheEarlierAlertsToo(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	id := ingestOne(t, e, "disk full")["id"].(string)

	res, err := e.IngestAlert(context.Background(), "key-1", map[string]any{
		"title": "disk full", "severity": "critical", "status": "resolved",
		"labels": map[string]any{"alertname": "disk full"},
	})
	if err != nil {
		t.Fatalf("IngestAlert (resolving): %v", err)
	}
	if utils.StrVal(res, "result") != "resolved" {
		t.Fatalf("ingest result = %v, want resolved", res["result"])
	}
	requireAllStatuses(t, ms, id, model.AlertStatusResolved)
}
