package tests

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The ordering is built as SQL — "ORDER BY <column> DESC NULLS LAST, id DESC" —
// and the in-memory doubles cannot tell a valid column list from an invalid
// one. This runs the real query against PostgreSQL for every column the alert
// pages offer, in both directions, so a name that does not exist as a column
// fails here rather than as a 500 on somebody's alert list.
func TestAlertGroupListOrderingInPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})

	chain, err := eng.CreateEscalationChain(adminCtx, map[string]any{"name": "sort-chain"})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(adminCtx, map[string]any{
		"name": "sort-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	key := utils.StrVal(integ, "key")

	// Three groups, ingested in order, so "newest first" has a known answer.
	titles := []string{"first", "second", "third"}
	for _, title := range titles {
		if _, err := eng.IngestAlert(adminCtx, key, map[string]any{
			"title": title, "severity": "critical",
			"labels": map[string]any{"alertname": title},
		}); err != nil {
			t.Fatalf("ingest %s: %v", title, err)
		}
		// last_received_at has second resolution, so ties would otherwise be
		// broken by id — which is exactly the ordering this replaces.
		time.Sleep(1100 * time.Millisecond)
	}

	page, err := eng.ListCollectionPage(adminCtx, "alert_groups", map[string]any{"limit": 100})
	if err != nil {
		t.Fatalf("default order: %v", err)
	}
	items := page["items"].([]map[string]any)
	if len(items) != 3 {
		t.Fatalf("listed %d groups, want 3", len(items))
	}
	if got := utils.StrVal(items[0], "title"); got != "third" {
		t.Errorf("first row = %q, want the newest group (\"third\")", got)
	}

	page, err = eng.ListCollectionPage(adminCtx, "alert_groups",
		map[string]any{"limit": 100, "sort": "last_received_at", "order": "asc"})
	if err != nil {
		t.Fatalf("ascending order: %v", err)
	}
	items = page["items"].([]map[string]any)
	if got := utils.StrVal(items[0], "title"); got != "first" {
		t.Errorf("first row ascending = %q, want the oldest group (\"first\")", got)
	}

	// All three are critical: within a severity the newest comes first, in
	// either direction. Ordered by random id instead, a critical that arrived a
	// second ago landed at the bottom of the "firing" view.
	for _, order := range []string{"desc", "asc"} {
		page, err = eng.ListCollectionPage(adminCtx, "alert_groups",
			map[string]any{"limit": 100, "sort": "severity", "order": order})
		if err != nil {
			t.Fatalf("severity %s: %v", order, err)
		}
		items = page["items"].([]map[string]any)
		if got := utils.StrVal(items[0], "title"); got != "third" {
			t.Errorf("first row by severity %s = %q, want the newest group (\"third\")", order, got)
		}
	}

	// Every column the UI offers, in both directions: the point is that the
	// query runs at all.
	for _, column := range []string{"last_received_at", "created_at", "severity", "status", "alert_count", "id"} {
		for _, order := range []string{"asc", "desc"} {
			if _, err := eng.ListCollectionPage(adminCtx, "alert_groups",
				map[string]any{"limit": 100, "sort": column, "order": order}); err != nil {
				t.Errorf("sort=%s order=%s: %v", column, order, err)
			}
		}
	}

	// And a column that does not exist is refused before it reaches SQL.
	if _, err := eng.ListCollectionPage(adminCtx, "alert_groups",
		map[string]any{"limit": 100, "sort": "no_such_column"}); err == nil {
		t.Error("an unknown sort column was accepted")
	}
}

// The reported symptom was "Запрос не выполнен Internal Error" from the delete
// button on the maintenance page. The engine allow-list is the fix; this proves
// the row actually leaves its own table, which the in-memory double cannot.
func TestDeleteMaintenanceWindowInPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})

	integ, err := eng.CreateIntegration(adminCtx, map[string]any{"name": "window-integration"})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	now := time.Now().UTC()
	window, err := eng.CreateMaintenanceWindow(adminCtx, map[string]any{
		"name":            "db upgrade",
		"integration_ids": []any{utils.StrVal(integ, "id")},
		"starts_at":       utils.ToISO(now),
		"ends_at":         utils.ToISO(now.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("create window: %v", err)
	}
	id := utils.StrVal(window, "id")

	if _, err := eng.DeleteEntity(adminCtx, "maintenance_windows", id); err != nil {
		t.Fatalf("delete window: %v", err)
	}
	page, err := eng.ListCollectionPage(adminCtx, "maintenance_windows", map[string]any{"limit": 100})
	if err != nil {
		t.Fatalf("list windows: %v", err)
	}
	for _, item := range page["items"].([]map[string]any) {
		if utils.StrVal(item, "id") == id {
			t.Fatal("deleted window still listed")
		}
	}
}
