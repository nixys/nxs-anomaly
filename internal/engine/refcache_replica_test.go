package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Every API replica has its own reference cache, and a write only drops the
// cache of the replica that made it. A replica that loaded its references a
// moment before somebody created a chain or a user on another replica used to
// route the next alert against that stale copy: the chain was "not found"
// (missing_chain) or the user "no longer existed", and both outcomes are final —
// the group stayed open with nobody paged, for good. Reproduced on the CE stand
// 3 times out of 3 with two API replicas, the chart's default.
//
// A reference that a paging decision needs and the cache does not have must be
// looked up in the database before it is declared gone.

// warm loads every reference collection into e's cache, as a replica that has
// just served an alert has.
func warm(t *testing.T, e *Engine) {
	t.Helper()
	for _, name := range []string{"users", "teams", "schedules", "escalation_chains", "integrations",
		"chatops_channels", "mobile_devices", "maintenance_windows"} {
		if _, err := e.refCollection(context.Background(), name); err != nil {
			t.Fatalf("warm %s: %v", name, err)
		}
	}
}

func groupLogTypes(t *testing.T, e *Engine, groupID string) []string {
	t.Helper()
	g, err := e.store.GetItem(context.Background(), "alert_groups", groupID)
	if err != nil || g == nil {
		t.Fatalf("group %s: %v", groupID, err)
	}
	var types []string
	for _, entry := range asMaps(g["logs"]) {
		types = append(types, utils.StrVal(entry, "type"))
	}
	return types
}

func notificationsFor(t *testing.T, e *Engine, groupID string) int {
	t.Helper()
	all, err := e.store.ListCollection(context.Background(), "notifications")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range all {
		if utils.StrVal(x, "alert_group_id") == groupID {
			n++
		}
	}
	return n
}

func TestIngestOnAnotherReplicaSeesAChainAndUserCreatedMomentsAgo(t *testing.T) {
	ctx := context.Background()
	shared := storetest.New()
	writer, reader := New(shared), New(shared)
	warm(t, reader)

	user, err := writer.CreateUser(ctx, map[string]any{"name": "new", "username": "new", "on_duty": true,
		"notification_targets": []any{map[string]any{"type": "log", "target": "new"}}})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := writer.CreateEscalationChain(ctx, map[string]any{"name": "new",
		"steps": []any{map[string]any{"kind": StepNotifyUser, "user_ids": []any{user["id"]}}}})
	if err != nil {
		t.Fatal(err)
	}
	integ, err := writer.CreateIntegration(ctx, map[string]any{"name": "new", "default_chain_id": chain["id"]})
	if err != nil {
		t.Fatal(err)
	}

	res, err := reader.IngestAlert(ctx, integ["key"].(string), map[string]any{"title": "disk full", "severity": "critical", "dedupe_key": "dk"})
	if err != nil {
		t.Fatal(err)
	}
	gid := groupIDOf(res)
	if gid == "" {
		t.Fatalf("no group in %v", res)
	}
	types := groupLogTypes(t, reader, gid)
	for _, bad := range []string{"missing_chain", "notify_skipped_unknown_users"} {
		for _, got := range types {
			if got == bad {
				t.Fatalf("the replica routed against its stale cache: timeline %v", types)
			}
		}
	}
	if n := notificationsFor(t, reader, gid); n == 0 {
		t.Fatalf("nobody was paged; timeline %v", types)
	}
}

func TestEscalationOnAWarmWorkerSeesAUserAddedMomentsAgo(t *testing.T) {
	ctx := context.Background()
	shared := storetest.New()
	api, worker := New(shared), New(shared)

	first, err := api.CreateUser(ctx, map[string]any{"name": "first", "username": "first", "on_duty": true,
		"notification_targets": []any{map[string]any{"type": "log", "target": "first"}}})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := api.CreateEscalationChain(ctx, map[string]any{"name": "c", "steps": []any{
		map[string]any{"kind": StepWait, "delay_minutes": 1},
		map[string]any{"kind": StepNotifyUser, "user_ids": []any{first["id"]}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	integ, err := api.CreateIntegration(ctx, map[string]any{"name": "i", "default_chain_id": chain["id"]})
	if err != nil {
		t.Fatal(err)
	}
	res, err := api.IngestAlert(ctx, integ["key"].(string), map[string]any{"title": "t", "dedupe_key": "dk"})
	if err != nil {
		t.Fatal(err)
	}
	gid := groupIDOf(res)

	warm(t, worker)
	// Somebody takes the page over on the API replica while the worker's copy
	// is warm, and the wait runs out.
	second, err := api.CreateUser(ctx, map[string]any{"name": "second", "username": "second", "on_duty": true,
		"notification_targets": []any{map[string]any{"type": "log", "target": "second"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{"name": "c", "steps": []any{
		map[string]any{"kind": StepWait, "delay_minutes": 1},
		map[string]any{"kind": StepNotifyUser, "user_ids": []any{second["id"]}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Collections are cached and expire one by one: the worker's chains can be
	// fresher than its users.
	worker.refCache.invalidate("escalation_chains")
	g, err := shared.GetItem(ctx, "alert_groups", gid)
	if err != nil {
		t.Fatal(err)
	}
	g["next_run_at"] = "2020-01-01T00:00:00+00:00"
	if err := shared.UpsertItem(ctx, "alert_groups", g); err != nil {
		t.Fatal(err)
	}

	if _, err := worker.ProcessDueEscalations(ctx); err != nil {
		t.Fatal(err)
	}
	types := groupLogTypes(t, worker, gid)
	for _, got := range types {
		if got == "notify_skipped_unknown_users" || got == "missing_chain" {
			t.Fatalf("the worker escalated against its stale cache: timeline %v", types)
		}
	}
	if notificationsFor(t, worker, gid) == 0 {
		t.Fatalf("the step paged nobody; timeline %v", types)
	}
}

func groupIDOf(ingest map[string]any) string {
	g, _ := ingest["group"].(map[string]any)
	return utils.StrVal(g, "id")
}

type countingStore struct {
	*storetest.Store
	lists map[string]int
}

func (c *countingStore) ListCollection(ctx context.Context, name string) ([]map[string]any, error) {
	c.lists[name]++
	return c.Store.ListCollection(ctx, name)
}

// A reference that really is gone must not turn the cache off: it is re-read
// once, and the verified load then serves every alert until it expires.
func TestAReferenceThatIsReallyGoneIsReloadedOncePerTTL(t *testing.T) {
	ctx := context.Background()
	base := storetest.New()
	cs := &countingStore{Store: base, lists: map[string]int{}}
	e := New(cs)
	if err := base.UpsertItem(ctx, "escalation_chains", map[string]any{"id": "c1", "name": "c1",
		"steps": []any{map[string]any{"kind": StepNotifyUser, "user_ids": []any{"usr_gone"}}}}); err != nil {
		t.Fatal(err)
	}
	integ, err := e.CreateIntegration(ctx, map[string]any{"name": "i", "default_chain_id": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := e.IngestAlert(ctx, integ["key"].(string), map[string]any{"title": "t", "dedupe_key": "dk" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if got := cs.lists["users"]; got != 2 {
		t.Errorf("users were listed %d times for 5 alerts, want 2 (the load and one reload for the missing id)", got)
	}
}
