package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"testing/fstest"
	"time"
)

// TestRunMigrationsNoTransaction exercises the `-- nxs:no-transaction` migration
// path against a real database: a CREATE INDEX CONCURRENTLY migration (forbidden
// inside a transaction) is applied via runMigrationNoTx, its version is recorded,
// the index ends up valid, and a second run is an idempotent no-op.
func TestRunMigrationsNoTransaction(t *testing.T) {
	dsn := os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NXS_ANOMALY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	t.Setenv("NXS_ANOMALY_DB_DSN", dsn)
	st, err := NewPostgreSQLStore(ctx)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	// Registered as a cleanup rather than deferred so it runs AFTER the cleanup
	// below. A deferred Close runs when this function returns, which is before
	// any t.Cleanup — so the cleanup found a closed pool, bailed out at Acquire
	// and dropped nothing, leaving the synthetic table, its index and two
	// test_<ts>_* rows in nxs_anomaly_schema_migrations behind. Invisible on the
	// throwaway container CI uses, permanent on a database that gets reused.
	// Cleanups run LIFO, so this one goes first to run last.
	t.Cleanup(func() { st.Close() })
	s := st.(*pgStore)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	ts := time.Now().UnixNano()
	normalVer := fmt.Sprintf("test_%d_a_normal", ts)
	notxVer := fmt.Sprintf("test_%d_b_notx", ts)
	tbl := fmt.Sprintf("nxs_anomaly_test_notx_%d", ts)
	idx := fmt.Sprintf("nxs_anomaly_test_notx_idx_%d", ts)

	migs := fstest.MapFS{
		"migrations/" + normalVer + ".sql": {Data: []byte(
			fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (id text);", tbl))},
		"migrations/" + notxVer + ".sql": {Data: []byte(
			fmt.Sprintf("-- nxs:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s (id);", idx, tbl))},
	}

	t.Cleanup(func() {
		c, err := s.pool.Acquire(ctx)
		if err != nil {
			// Reported, not swallowed: a cleanup that cannot even connect leaves
			// artefacts in a shared database, and staying quiet about it is how
			// that went unnoticed in the first place.
			t.Errorf("cleanup could not acquire a connection, artefacts left behind: %v", err)
			return
		}
		defer c.Release()
		for _, stmt := range []string{"DROP INDEX IF EXISTS " + idx, "DROP TABLE IF EXISTS " + tbl} {
			if _, err := c.Exec(ctx, stmt); err != nil {
				t.Errorf("cleanup %q: %v", stmt, err)
			}
		}
		if _, err := c.Exec(ctx,
			"DELETE FROM nxs_anomaly_schema_migrations WHERE version IN ($1,$2)", normalVer, notxVer); err != nil {
			t.Errorf("cleanup of synthetic migration rows: %v", err)
		}
	})

	if err := s.runMigrationsFS(ctx, conn.Conn(), migs); err != nil {
		t.Fatalf("runMigrationsFS: %v", err)
	}

	// Both versions recorded.
	for _, v := range []string{normalVer, notxVer} {
		var exists bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM nxs_anomaly_schema_migrations WHERE version=$1)", v).Scan(&exists); err != nil {
			t.Fatalf("check version %s: %v", v, err)
		}
		if !exists {
			t.Errorf("migration version %s was not recorded", v)
		}
	}

	// The CONCURRENTLY index exists and is valid (a failed concurrent build leaves
	// an INVALID index; this proves the no-transaction path completed it).
	var valid bool
	if err := conn.QueryRow(ctx,
		"SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname=$1", idx).Scan(&valid); err != nil {
		t.Fatalf("index %s not found: %v", idx, err)
	}
	if !valid {
		t.Errorf("index %s is not valid after no-transaction migration", idx)
	}

	// Second run is an idempotent no-op (version-exists guard skips both; no
	// duplicate-key error from re-inserting the version).
	if err := s.runMigrationsFS(ctx, conn.Conn(), migs); err != nil {
		t.Fatalf("second runMigrationsFS (idempotency): %v", err)
	}
}
