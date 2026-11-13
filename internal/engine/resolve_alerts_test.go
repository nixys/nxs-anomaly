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

// ctxRecordingStore remembers whether the alert status write saw a context
// that was already cancelled.
type ctxRecordingStore struct {
	*memStore
	sawCancelled bool
}

func (s *ctxRecordingStore) SetAlertStatusForGroups(ctx context.Context, groupIDs []string, status string) (int, error) {
	if ctx.Err() != nil {
		s.sawCancelled = true
		return 0, ctx.Err()
	}
	return s.memStore.SetAlertStatusForGroups(ctx, groupIDs, status)
}

// TestAlertStatusSyncOutlivesTheRequest: on the 1.9.13 stand a group was
// reopened, the client hung up before the follow-up write, and its alerts kept
// reading "resolved" (alert_status_sync_failed: context canceled). The write
// comes after the commit, so it must not depend on the client staying around.
func TestAlertStatusSyncOutlivesTheRequest(t *testing.T) {
	st := &ctxRecordingStore{memStore: newMemStore()}
	e := &Engine{store: st}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.syncAlertStatusForGroups(ctx, []string{"g1"}, model.AlertStatusFiring)
	if st.sawCancelled {
		t.Fatal("the alert status write ran on the request's cancelled context")
	}
}

// failingSyncStore drops every post-commit alert status write, the way the
// 10-second write timed out on a bulk resolve of 200 large groups.
type failingSyncStore struct{ *memStore }

func (s *failingSyncStore) SetAlertStatusForGroups(context.Context, []string, string) (int, error) {
	return 0, context.DeadlineExceeded
}

// TestRepairSweepResolvesAlertsTheSyncMissed: on the 1.10.2 stand the sync
// timed out and nothing retried it, so 26 000 alerts of 400 closed groups read
// "firing" for good. The worker's archival stage now finishes the write.
func TestRepairSweepResolvesAlertsTheSyncMissed(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	id := ingestOne(t, e, "disk full")["id"].(string)
	open := ingestOne(t, e, "cpu hot")["id"].(string)

	e.store = &failingSyncStore{memStore: ms}
	if _, err := e.BulkResolveGroups(adminCtx(), []string{id}); err != nil {
		t.Fatalf("BulkResolveGroups: %v", err)
	}
	// Asserted first so the test cannot pass by the sync having worked.
	requireAllStatuses(t, ms, id, model.AlertStatusFiring)

	e.repairResolvedGroupAlerts(context.Background())
	requireAllStatuses(t, ms, id, model.AlertStatusResolved)
	// An open group's firing alerts are live state, not a missed write.
	requireAllStatuses(t, ms, open, model.AlertStatusFiring)
}
