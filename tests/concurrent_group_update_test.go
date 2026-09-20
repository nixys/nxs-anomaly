package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Two writers, one alert group. The operator's action and the worker's
// escalation hold different advisory locks — resolve_group and the
// integration's shard — so nothing serialized them: both loaded the row, both
// wrote the whole row back, and whichever committed last silently dropped the
// other's change. On a stand a resolve answered 200, was written to the audit
// trail, and left the group open and paging in 5 of 20 attempts.
//
// The fix locks the rows of alert_groups as the saving transaction reads them,
// so the two possible orderings are the only two outcomes and neither change
// disappears.
func TestConcurrentGroupWritesDoNotOverwriteEachOther(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})
	chain, err := eng.CreateEscalationChain(adminCtx, map[string]any{"name": "race-chain"})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(adminCtx, map[string]any{
		"name": "race-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if _, err := eng.IngestAlert(adminCtx, utils.StrVal(integ, "key"),
		map[string]any{"title": "race", "severity": "critical", "dedupe_key": "race"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	groups, err := st.ListCollection(ctx, "alert_groups")
	if err != nil || len(groups) != 1 {
		t.Fatalf("list groups: %v (%d groups)", err, len(groups))
	}
	groupID := utils.StrVal(groups[0], "id")

	// Each writer changes the group and holds its transaction open, so the
	// second one is guaranteed to overlap with the first.
	writer := func(mutate func(model.AlertGroup), hold time.Duration) error {
		_, err := st.UpdateCollectionsFiltered(ctx,
			[]store.LoadSpec{{Collection: "alert_groups", Filters: map[string]any{"id": groupID}}},
			[]string{"alert_groups"},
			func(state *store.State) (any, error) {
				g, ok := state.AlertGroups[groupID].(model.AlertGroup)
				if !ok {
					return nil, nil
				}
				mutate(g)
				time.Sleep(hold)
				return nil, nil
			}, 0)
		return err
	}
	ts := utils.ToISO(utils.UTCNow())

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	// The worker's escalation step: it reads the group first and commits last.
	go func() {
		defer wg.Done()
		errs[0] = writer(func(g model.AlertGroup) {
			g.AppendLog("escalation_step", "the worker advanced the chain", nil)
		}, 400*time.Millisecond)
	}()
	time.Sleep(80 * time.Millisecond)
	// The operator resolves the group while the worker still holds its transaction.
	go func() {
		defer wg.Done()
		errs[1] = writer(func(g model.AlertGroup) {
			g.Resolve(ts, "Resolved by the operator", authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})
		}, 0)
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	after, err := st.GetItem(ctx, "alert_groups", groupID)
	if err != nil || after == nil {
		t.Fatalf("read group back: %v", err)
	}
	if got := utils.StrVal(after, "status"); got != "resolved" {
		t.Errorf("status = %q, want resolved: the operator's resolve was overwritten by the worker", got)
	}
	logs := after["logs"]
	found := false
	for _, entry := range asAnySlice(logs) {
		if m, ok := entry.(map[string]any); ok && utils.StrVal(m, "type") == "escalation_step" {
			found = true
		}
	}
	if !found {
		t.Errorf("the worker's timeline entry is missing: its change was overwritten (%v)", logs)
	}
}

func asAnySlice(v any) []any {
	switch xs := v.(type) {
	case []any:
		return xs
	case []map[string]any:
		out := make([]any, len(xs))
		for i, x := range xs {
			out[i] = x
		}
		return out
	}
	return nil
}
