//go:build restoredrill

// Restore-drill seed/verify halves, driven by tests/restore_drill.sh. They are
// behind the `restoredrill` build tag so the normal suite never runs them (the
// drill deliberately does NOT clear the store, and runs either side of a
// destroy+restore of the database). See docs/BACKUP_RESTORE.md.
package tests

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

const drillCanary = "restore-drill-canary"

// TestRestoreDrillSeed provisions a minimal but complete alert flow — a user, an
// escalation chain, an integration and one ingested alert — then leaves it in the
// database to be backed up. It intentionally does not clear the store.
func TestRestoreDrillSeed(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()

	user, err := eng.CreateUser(ctx, map[string]any{"name": "Drill User", "username": drillCanary})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  drillCanary,
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": drillCanary,
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{"title": "pre-backup alert"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	t.Logf("seeded canary integration %s with one alert group", utils.StrVal(integ, "id"))
}

// TestRestoreDrillVerify runs after the database was destroyed and restored from
// the backup. It proves (1) the seeded data survived the restore and (2) the
// alert flow still works end-to-end on the restored database by ingesting a fresh
// alert and confirming a new group is created.
func TestRestoreDrillVerify(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()

	// (1) the canary integration and its pre-backup alert group must be present.
	integrations, err := st.ListCollection(ctx, "integrations")
	if err != nil {
		t.Fatalf("list integrations: %v", err)
	}
	var canary map[string]any
	for _, it := range integrations {
		if utils.StrVal(it, "name") == drillCanary {
			canary = it
			break
		}
	}
	if canary == nil {
		t.Fatal("restore lost the canary integration — backup/restore is not faithful")
	}
	before, err := st.ListCollection(ctx, "alert_groups")
	if err != nil {
		t.Fatalf("list alert_groups: %v", err)
	}
	if len(before) < 1 {
		t.Fatalf("restore lost the pre-backup alert group (have %d)", len(before))
	}

	// (2) the flow still works: a fresh ingest on the restored DB creates a group.
	if _, err := eng.IngestAlert(ctx, utils.StrVal(canary, "key"), map[string]any{"title": "post-restore alert"}); err != nil {
		t.Fatalf("post-restore ingest failed — alert flow broken: %v", err)
	}
	after, err := st.ListCollection(ctx, "alert_groups")
	if err != nil {
		t.Fatalf("list alert_groups after ingest: %v", err)
	}
	if len(after) <= len(before) {
		t.Fatalf("post-restore ingest created no new alert group (before=%d after=%d)", len(before), len(after))
	}
	t.Logf("restore verified: canary present, alert flow works (groups %d -> %d)", len(before), len(after))
}
