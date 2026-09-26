package tests

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// A chat's status and alerts count and page the open groups in PostgreSQL
// (the in-memory doubles cannot check the SQL): hidden integrations are left
// out, resolved groups too, newest alert first, and the total counts them all.
func TestUnresolvedGroupPagesInPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)
	adminCtx := authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})

	chain, err := eng.CreateEscalationChain(adminCtx, map[string]any{"name": "page-chain"})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integration := func(name string) map[string]any {
		i, err := eng.CreateIntegration(adminCtx, map[string]any{"name": name, "routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true, "escalation_chain_id": chain["id"]}}})
		if err != nil {
			t.Fatalf("create integration: %v", err)
		}
		return i
	}
	seen, hidden := integration("seen"), integration("hidden")
	ingest := func(integ map[string]any, title string) {
		if _, err := eng.IngestAlert(adminCtx, utils.StrVal(integ, "key"), map[string]any{
			"title": title, "severity": "critical", "labels": map[string]any{"alertname": title}}); err != nil {
			t.Fatalf("ingest %s: %v", title, err)
		}
		time.Sleep(1100 * time.Millisecond) // last_received_at has second resolution
	}
	ingest(seen, "old")
	ingest(hidden, "elsewhere")
	ingest(seen, "middle")
	ingest(seen, "resolved later")
	ingest(seen, "new")
	page, err := eng.ListCollectionPage(adminCtx, "alert_groups", map[string]any{"limit": 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, g := range page["items"].([]map[string]any) {
		if utils.StrVal(g, "title") == "resolved later" {
			if _, err := eng.ResolveGroup(adminCtx, utils.StrVal(g, "id")); err != nil {
				t.Fatalf("resolve: %v", err)
			}
		}
	}

	rows, total, err := st.PageUnresolvedAlertGroups(ctx, []string{utils.StrVal(hidden, "id")}, 2, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	var titles []string
	for _, r := range rows {
		titles = append(titles, utils.StrVal(r, "title"))
	}
	if total != 3 || len(titles) != 2 || titles[0] != "new" || titles[1] != "middle" {
		t.Errorf("first page = %v of %d, want [new middle] of 3", titles, total)
	}
	rows, _, err = st.PageUnresolvedAlertGroups(ctx, []string{utils.StrVal(hidden, "id")}, 2, 2)
	if err != nil || len(rows) != 1 || utils.StrVal(rows[0], "title") != "old" {
		t.Errorf("second page = %v (%v), want [old]", rows, err)
	}
	if _, total, _ = st.PageUnresolvedAlertGroups(ctx, nil, 0, 0); total != 4 {
		t.Errorf("nothing hidden: total %d, want 4", total)
	}
}
