package tests

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Closing a group carries down to its alerts, and this is the test that proves
// the write itself: the status lives twice in the alerts table — inside the
// jsonb payload the API returns and in the typed column the list filter reads —
// and an update that moved only one of them would leave the two answering
// differently depending on which page asked.
func TestResolvingGroupClosesItsAlertsInPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})

	chain, err := eng.CreateEscalationChain(adminCtx, map[string]any{"name": "resolve-chain"})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(adminCtx, map[string]any{
		"name": "resolve-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}

	res, err := eng.IngestAlert(adminCtx, utils.StrVal(integ, "key"), map[string]any{
		"title": "disk full", "labels": map[string]any{"alertname": "disk"},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	groupID := utils.StrVal(res["group"].(map[string]any), "id")
	alertID := utils.StrVal(res["alert"].(map[string]any), "id")

	if _, err := eng.ResolveGroup(adminCtx, groupID); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// The payload, as every read path returns it.
	alert, err := st.GetItem(ctx, "alerts", alertID)
	if err != nil {
		t.Fatalf("get alert: %v", err)
	}
	if got := utils.StrVal(alert, "status"); got != model.AlertStatusResolved {
		t.Fatalf("alert status = %q, want %q", got, model.AlertStatusResolved)
	}

	// The typed column, as the alerts list filters on it. A payload updated
	// without the column would show the alert closed and still return it under
	// "status=firing".
	firing, _, err := st.ListCollectionPage(ctx, "alerts",
		map[string]any{"status": model.AlertStatusFiring}, 100, 0, store.SortSpec{})
	if err != nil {
		t.Fatalf("list firing alerts: %v", err)
	}
	if len(firing) != 0 {
		t.Fatalf("alerts still listed as firing after the group was resolved: %#v", firing)
	}

	// Reopening the group brings its alerts back with it.
	if _, err := eng.UnresolveGroup(adminCtx, groupID); err != nil {
		t.Fatalf("unresolve: %v", err)
	}
	alert, err = st.GetItem(ctx, "alerts", alertID)
	if err != nil {
		t.Fatalf("get alert after unresolve: %v", err)
	}
	if got := utils.StrVal(alert, "status"); got != model.AlertStatusFiring {
		t.Fatalf("alert status after unresolve = %q, want %q", got, model.AlertStatusFiring)
	}
}
