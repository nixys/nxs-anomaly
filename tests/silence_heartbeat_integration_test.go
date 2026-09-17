package tests

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Defects that lived in SQL, so they are checked against PostgreSQL.

func silenceFixture(t *testing.T, ctx context.Context, eng interface {
	CreateUser(context.Context, map[string]any) (map[string]any, error)
	CreateEscalationChain(context.Context, map[string]any) (map[string]any, error)
	CreateIntegration(context.Context, map[string]any) (map[string]any, error)
}, suffix string, heartbeat map[string]any) (string, string) {
	t.Helper()
	user, err := eng.CreateUser(ctx, map[string]any{"name": "Silence " + suffix, "username": "silence-" + suffix})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	chain, err := eng.CreateEscalationChain(ctx, map[string]any{"name": "silence-" + suffix,
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}}})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	payload := map[string]any{"name": "silence-" + suffix, "routes": []any{map[string]any{
		"name": "all", "match_type": "all", "is_default": true, "escalation_chain_id": chain["id"]}}}
	if heartbeat != nil {
		payload["heartbeat"] = heartbeat
	}
	integ, err := eng.CreateIntegration(ctx, payload)
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	return utils.StrVal(integ, "id"), utils.StrVal(integ, "key")
}

func TestExpiredSilenceIsPickedUpByTheWorker(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	_, key := silenceFixture(t, ctx, eng, "expiry-"+utils.MakeID("t"), nil)

	res, err := eng.IngestAlert(ctx, key, map[string]any{"title": "silenced then back", "severity": "critical", "dedupe_key": "sil"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	groupID := utils.StrVal(res["group"].(map[string]any), "id")
	if _, err := eng.SilenceGroup(ctx, groupID, 1); err != nil {
		t.Fatalf("silence: %v", err)
	}
	// Move the end of the silence into the past, as the clock would.
	group, err := st.GetItem(ctx, "alert_groups", groupID)
	if err != nil {
		t.Fatal(err)
	}
	past := utils.ToISO(time.Now().UTC().Add(-time.Second))
	group["silenced_until"] = past
	group["next_run_at"] = past
	if err := st.UpsertItem(ctx, "alert_groups", group); err != nil {
		t.Fatal(err)
	}
	before, _ := st.ListCollection(ctx, "notifications")

	if _, err := eng.ProcessDueEscalations(ctx); err != nil {
		t.Fatalf("escalations: %v", err)
	}
	group, _ = st.GetItem(ctx, "alert_groups", groupID)
	if utils.StrVal(group, "status") != "open" {
		t.Errorf("status = %v after the silence ended, want open", group["status"])
	}
	after, _ := st.ListCollection(ctx, "notifications")
	if len(after) <= len(before) {
		t.Errorf("no notification after the silence ended (%d before, %d after)", len(before), len(after))
	}
}

func TestSilentSourceStaysSilentAcrossCycles(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	integID, key := silenceFixture(t, ctx, eng, "hb-"+utils.MakeID("t"), map[string]any{"interval_seconds": 60})

	if _, err := eng.IngestAlert(ctx, key, map[string]any{"title": "tick", "dedupe_key": "tick"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	alerts, _ := st.ListCollection(ctx, "alerts")
	for _, a := range alerts {
		if utils.StrVal(a, "integration_id") == integID {
			a["received_at"] = utils.ToISO(time.Now().UTC().Add(-10 * time.Minute))
			if err := st.UpsertItem(ctx, "alerts", a); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := eng.ProcessSourceHeartbeats(ctx); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	groups, _ := st.ListCollection(ctx, "alert_groups")
	var silent []map[string]any
	for _, g := range groups {
		labels, _ := g["labels"].(map[string]any)
		if utils.StrVal(g, "integration_id") == integID && labels["alertname"] == "SourceSilent" {
			silent = append(silent, g)
		}
	}
	if len(silent) != 1 || utils.StrVal(silent[0], "status") != "open" {
		t.Errorf("SourceSilent groups after three cycles: %v, want one open group", silent)
	}
}

func TestSoftDeleteTimestampIsRFC3339(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	integID, _ := silenceFixture(t, ctx, eng, "del-"+utils.MakeID("t"), nil)
	if _, err := eng.DeleteEntity(ctx, "integrations", integID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	item, err := st.GetItem(ctx, "integrations", integID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, utils.StrVal(item, "deleted_at")); err != nil {
		t.Errorf("deleted_at = %q is not RFC 3339: %v", item["deleted_at"], err)
	}
}

// A replica that starts while another one migrates waits for the migration
// lock. The wait used to run under statement_timeout, so after 30 seconds the
// replica exited instead of waiting.
func TestStartupWaitsForAMigrationLockLongerThanStatementTimeout(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	st.Close()

	holder, err := pgx.Connect(ctx, os.Getenv("NXS_ANOMALY_DB_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(ctx) //nolint:errcheck
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock(72544000)"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(3 * time.Second)
		_, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock(72544000)")
	}()

	t.Setenv("NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS", "1")
	started := time.Now()
	second, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		t.Fatalf("store init while another replica holds the migration lock: %v", err)
	}
	defer second.Close()
	if waited := time.Since(started); waited < 2*time.Second {
		t.Errorf("init returned after %s, before the lock was released", waited)
	}
}
