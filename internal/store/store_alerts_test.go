package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The alert status writes are plain SQL that the in-memory fakes only model,
// so these run against a real database or skip (newRateLimitTestStore).

// newAlertsTestStore returns a store whose alerts and alert_groups tables live
// in a schema of their own. The repair sweep works on the whole table, and
// other packages' tests share the database and run workers and collection
// saves against the same tables at the same time; in the shared tables the
// seeded group was gone before the sweep looked for it.
func newAlertsTestStore(t *testing.T) *pgStore {
	t.Helper()
	shared := newRateLimitTestStore(t) // migrates, or skips without a database
	ctx := context.Background()
	schema := fmt.Sprintf("t_alerts_%d", time.Now().UnixNano())
	for _, q := range []string{
		"CREATE SCHEMA " + schema,
		"CREATE TABLE " + schema + ".nxs_anomaly_alerts (LIKE public.nxs_anomaly_alerts INCLUDING ALL)",
		"CREATE TABLE " + schema + ".nxs_anomaly_alert_groups (LIKE public.nxs_anomaly_alert_groups INCLUDING ALL)",
	} {
		if _, err := shared.pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = shared.pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg := shared.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &pgStore{pool: pool}
}

// seedGroupAlerts inserts one group with n alerts, all firing, and returns the
// group id.
func seedGroupAlerts(t *testing.T, s *pgStore, prefix, groupStatus string, n int) string {
	t.Helper()
	ctx := context.Background()
	gid := prefix + "-g"
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO nxs_anomaly_alert_groups (id, data, status)
		VALUES ($1, jsonb_build_object('id', $1::text, 'status', $2::text), $2)`, gid, groupStatus); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO nxs_anomaly_alerts (id, data, status, alert_group_id)
		SELECT $1 || '-a' || i, jsonb_build_object('id', $1 || '-a' || i, 'status', 'firing', 'alert_group_id', $2::text), 'firing', $2
		  FROM generate_series(1, $3) AS i`, prefix, gid, n); err != nil {
		t.Fatalf("insert alerts: %v", err)
	}
	return gid
}

func countAlerts(t *testing.T, s *pgStore, gid, status string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM nxs_anomaly_alerts
		 WHERE alert_group_id = $1 AND status = $2 AND data->>'status' = $2`, gid, status).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// More rows than one chunk: every one of them must change, and the count
// returned must cover all chunks.
func TestSetAlertStatusForGroupsWritesEveryChunk(t *testing.T) {
	s := newAlertsTestStore(t)
	prefix := fmt.Sprintf("t-chunk-%d", time.Now().UnixNano())
	n := alertStatusChunk*2 + 7
	gid := seedGroupAlerts(t, s, prefix, "resolved", n)

	got, err := s.SetAlertStatusForGroups(context.Background(), []string{gid}, "resolved")
	if err != nil {
		t.Fatalf("SetAlertStatusForGroups: %v", err)
	}
	if got != n {
		t.Fatalf("changed %d rows, want %d", got, n)
	}
	if c := countAlerts(t, s, gid, "resolved"); c != n {
		t.Fatalf("%d of %d alerts resolved", c, n)
	}
}

// The repair sweep resolves the firing alerts of resolved groups, in batches,
// and leaves an open group's firing alerts alone.
func TestRepairResolvedGroupAlerts(t *testing.T) {
	s := newAlertsTestStore(t)
	prefix := fmt.Sprintf("t-repair-%d", time.Now().UnixNano())
	closed := seedGroupAlerts(t, s, prefix+"-closed", "resolved", 25)
	open := seedGroupAlerts(t, s, prefix+"-open", "firing", 5)

	ctx := context.Background()
	total := 0
	for i := 0; i < 100; i++ {
		n, err := s.RepairResolvedGroupAlerts(ctx, 10)
		if err != nil {
			t.Fatalf("RepairResolvedGroupAlerts: %v", err)
		}
		if n > 10 {
			t.Fatalf("one pass changed %d rows past its limit of 10", n)
		}
		total += n
		if n == 0 {
			break
		}
	}
	if c := countAlerts(t, s, closed, "resolved"); c != 25 {
		t.Fatalf("%d of 25 alerts of the resolved group repaired (passes changed %d)", c, total)
	}
	if c := countAlerts(t, s, open, "firing"); c != 5 {
		t.Fatalf("open group: %d of 5 alerts still firing", c)
	}
}
