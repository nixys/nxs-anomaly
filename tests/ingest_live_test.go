package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/server"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The regressions for LIVE-01 (a repeat firing executing the escalation step a
// WAIT was still counting down to) and LIVE-02 (parallel ingests overwriting
// each other's aggregate group state) run against a real PostgreSQL and over
// real HTTP, because neither bug is visible anywhere else: the first needs the
// group row to survive between two requests, and the second only happens when
// two requests are inside the ingest at the same time. An in-memory store with
// no advisory lock and a handler called directly reproduce neither.

// liveIngestServer serves the production router over the integration database.
// No worker loop is started: these tests are about what ingest does on its own,
// and a worker advancing the chain in the background would make "the WAIT was
// skipped" and "the WAIT elapsed" indistinguishable.
func liveIngestServer(t *testing.T, ctx context.Context) (*httptest.Server, *engine.Engine, store.PostgreSQLStore) {
	t.Helper()
	st, eng := newIntegrationEngine(t, ctx)
	t.Cleanup(st.Close)
	clearStore(t, ctx, st)
	hs := httptest.NewServer(server.NewTestHandler(st, eng))
	t.Cleanup(hs.Close)
	return hs, eng, st
}

// liveIntegration creates a chain with the given steps and an integration
// routed to it, and returns the integration key.
func liveIntegration(t *testing.T, eng *engine.Engine, name string, steps []any) string {
	t.Helper()
	ctx := authz.NewContext(context.Background(),
		authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})
	chain, err := eng.CreateEscalationChain(ctx, map[string]any{"name": name + "-chain", "steps": steps})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": name,
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	return utils.StrVal(integ, "key")
}

// fireErr posts one firing alert. It returns an error instead of failing the
// test so that it can also be called from the parallel goroutines below, where
// t.Fatalf is not allowed.
func fireErr(hs *httptest.Server, key, title string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{
		"title":  title,
		"labels": map[string]any{"alertname": title},
	})
	resp, err := http.Post(hs.URL+"/integrations/v1/webhook/"+key, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("post alert: status=%d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode ingest result: %w", err)
	}
	return out, nil
}

// fire posts one firing alert and returns the decoded ingest result.
func fire(t *testing.T, hs *httptest.Server, key, title string) map[string]any {
	t.Helper()
	out, err := fireErr(hs, key, title)
	if err != nil {
		t.Fatalf("fire %q: %v", title, err)
	}
	return out
}

// fireGroupID posts one firing alert and returns the id of the group it landed on.
func fireGroupID(t *testing.T, hs *httptest.Server, key, title string) string {
	t.Helper()
	return utils.StrVal(fire(t, hs, key, title)["group"].(map[string]any), "id")
}

func liveGroup(t *testing.T, ctx context.Context, st store.PostgreSQLStore, id string) map[string]any {
	t.Helper()
	g, err := st.GetItem(ctx, "alert_groups", id)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if g == nil {
		t.Fatalf("group %s not found", id)
	}
	return g
}

// LIVE-01. The chain waits ten minutes and then resolves. A second firing of
// the same alert arriving half a second later must not be what makes the ten
// minutes elapse: before the fix it ran the step after the WAIT, so the group
// opened and closed within the same second and the third firing had to open a
// new group for an incident that was still going on.
func TestRepeatFiringDoesNotExecuteTheStepAWaitIsCountingDownTo(t *testing.T) {
	ctx := context.Background()
	hs, eng, st := liveIngestServer(t, ctx)
	key := liveIntegration(t, eng, "live01-wait", []any{
		map[string]any{"kind": "WAIT", "delay_minutes": 10},
		map[string]any{"kind": "RESOLVE"},
	})

	first := fire(t, hs, key, "live01 disk full")
	groupID := utils.StrVal(first["group"].(map[string]any), "id")
	scheduled := utils.StrVal(first["group"].(map[string]any), "next_run_at")
	if scheduled == "" {
		t.Fatal("first firing did not schedule the WAIT")
	}

	second := fire(t, hs, key, "live01 disk full")
	if got := utils.StrVal(second["group"].(map[string]any), "id"); got != groupID {
		t.Fatalf("second firing grouped into %s, want %s", got, groupID)
	}

	g := liveGroup(t, ctx, st, groupID)
	if got := utils.StrVal(g, "status"); got != model.StatusOpen {
		t.Fatalf("group status = %q after a repeat firing, want %q — the RESOLVE behind the WAIT ran early", got, model.StatusOpen)
	}
	if got := utils.StrVal(g, "next_run_at"); got != scheduled {
		t.Errorf("next_run_at = %q, want the first firing's %q: the timer must not be restarted or consumed by a repeat", got, scheduled)
	}
	if got := utils.IntVal(g, "current_step"); got != 1 {
		t.Errorf("current_step = %d, want 1: the chain must still be parked behind the WAIT", got)
	}

	// And the third firing joins the same group rather than opening a new one,
	// which is what an operator sees as "one incident became three".
	third := fire(t, hs, key, "live01 disk full")
	if got := utils.StrVal(third["group"].(map[string]any), "id"); got != groupID {
		t.Fatalf("third firing opened group %s, want %s", got, groupID)
	}
	if got := utils.IntVal(liveGroup(t, ctx, st, groupID), "alert_count"); got != 3 {
		t.Errorf("alert_count = %d after three firings, want 3", got)
	}
}

// LIVE-01, second half. Acknowledging is an operator saying they have the
// alert; the source restating the same alert must not undo that and start
// paging again. The opposite policy exists and a deployment opts into it, so
// both directions are asserted here — the default is the interesting one,
// because before the fix there was no default, only the reopen.
func TestRepeatFiringKeepsAnAcknowledgedGroupAcknowledged(t *testing.T) {
	ctx := context.Background()
	hs, eng, st := liveIngestServer(t, ctx)
	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})
	key := liveIntegration(t, eng, "live01-ack", []any{
		map[string]any{"kind": "WAIT", "delay_minutes": 10},
		map[string]any{"kind": "RESOLVE"},
	})

	groupID := fireGroupID(t, hs, key, "live01 ack")
	if _, err := eng.AcknowledgeGroup(adminCtx, groupID); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	fire(t, hs, key, "live01 ack")

	g := liveGroup(t, ctx, st, groupID)
	if got := utils.StrVal(g, "status"); got != model.StatusAcknowledged {
		t.Fatalf("status = %q after the same alert fired again, want %q", got, model.StatusAcknowledged)
	}
	if got := utils.StrVal(g, "next_run_at"); got != "" {
		t.Errorf("next_run_at = %q on an acknowledged group: escalation was resumed behind the operator", got)
	}
	// The alert itself is still recorded and counted; only the paging decision
	// changed.
	if got := utils.IntVal(g, "alert_count"); got != 2 {
		t.Errorf("alert_count = %d, want 2: the repeat must still be attached", got)
	}
}

// The opt-in policy, so the flag is known to be wired rather than merely
// documented.
func TestReopenAckedOnNewAlertPolicyReopensWhenEnabled(t *testing.T) {
	t.Setenv("NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT", "true")
	ctx := context.Background()
	hs, eng, st := liveIngestServer(t, ctx)
	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})
	key := liveIntegration(t, eng, "live01-ack-optin", []any{
		map[string]any{"kind": "WAIT", "delay_minutes": 10},
		map[string]any{"kind": "RESOLVE"},
	})

	groupID := fireGroupID(t, hs, key, "live01 ack optin")
	if _, err := eng.AcknowledgeGroup(adminCtx, groupID); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	fire(t, hs, key, "live01 ack optin")

	g := liveGroup(t, ctx, st, groupID)
	if got := utils.StrVal(g, "status"); got != model.StatusOpen {
		t.Fatalf("status = %q with the reopen policy enabled, want %q", got, model.StatusOpen)
	}
	// A reopened group restarts its chain, so it is parked behind the WAIT
	// again rather than falling straight through to the RESOLVE.
	if utils.StrVal(g, "next_run_at") == "" {
		t.Error("reopened group has no next_run_at: escalation was not restarted")
	}
	if got := utils.IntVal(g, "current_step"); got != 1 {
		t.Errorf("current_step = %d, want 1", got)
	}
}

// LIVE-01, third part. A chain that resolves the group during the ingest closes
// it without any source resolve event, and the alerts underneath it have to
// follow. Before the fix only the "resolved" ingest result was carried down, so
// a group closed by its own escalation kept firing alerts forever.
func TestEscalationResolveDuringIngestClosesTheAlertsToo(t *testing.T) {
	ctx := context.Background()
	hs, eng, st := liveIngestServer(t, ctx)
	key := liveIntegration(t, eng, "live01-resolve", []any{
		map[string]any{"kind": "RESOLVE"},
	})

	res := fire(t, hs, key, "live01 immediate resolve")
	groupID := utils.StrVal(res["group"].(map[string]any), "id")
	alertID := utils.StrVal(res["alert"].(map[string]any), "id")

	if got := utils.StrVal(liveGroup(t, ctx, st, groupID), "status"); got != model.StatusResolved {
		t.Fatalf("group status = %q, want %q", got, model.StatusResolved)
	}
	alert, err := st.GetItem(ctx, "alerts", alertID)
	if err != nil {
		t.Fatalf("get alert: %v", err)
	}
	if got := utils.StrVal(alert, "status"); got != model.AlertStatusResolved {
		t.Fatalf("alert status = %q on a group its own chain resolved, want %q", got, model.AlertStatusResolved)
	}
}

// LIVE-02. Twenty alerts of the same incident arriving at once must leave a
// group that counts twenty. Before the fix each request built its update on a
// copy of the group read before the advisory lock, so requests that overlapped
// wrote back a count and an id list that never saw each other's work: twenty
// accepted events left a group claiming ten.
func TestParallelIngestKeepsTheGroupAggregateConsistent(t *testing.T) {
	ctx := context.Background()
	hs, eng, st := liveIngestServer(t, ctx)
	// One notify step and nothing else: no WAIT to park behind, no automatic
	// resolve, so nothing but the aggregate state is under test.
	key := liveIntegration(t, eng, "live02-parallel", []any{
		map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{}},
	})

	// One alert first, so the group is known by id: what the parallel batch has
	// to preserve is this group's aggregate, and finding it afterwards by
	// searching would hide a second group opened by a lost update.
	groupID := fireGroupID(t, hs, key, "live02 same incident")

	const parallel = 20
	const workers = 4
	work := make(chan int, parallel)
	for i := 0; i < parallel; i++ {
		work <- i
	}
	close(work)
	errs := make(chan error, parallel)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range work {
				if _, err := fireErr(hs, key, "live02 same incident"); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("parallel ingest: %v", err)
	}

	const expected = parallel + 1 // the batch plus the alert that opened the group
	g := liveGroup(t, ctx, st, groupID)
	if got := utils.IntVal(g, "alert_count"); got != expected {
		t.Errorf("alert_count = %d after %d accepted events, want %d: concurrent ingests overwrote each other", got, expected, expected)
	}
	ids, _ := g["alert_ids"].([]any)
	if len(ids) != expected {
		t.Errorf("alert_ids holds %d ids, want %d", len(ids), expected)
	}
	// The alert rows themselves were never the thing that got lost; asserting
	// them keeps the two halves honest — a count that matches a table with
	// missing rows would be consistent and wrong.
	alerts, _, err := st.ListCollectionPage(ctx, "alerts",
		map[string]any{"alert_group_id": groupID}, 200, 0, store.SortSpec{})
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(alerts) != expected {
		t.Errorf("%d alert rows stored, want %d", len(alerts), expected)
	}
}
