package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// initialize runs migrations under advisory lock 72544000.
func (s *pgStore) initialize(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(72544000)"); err != nil {
		return fmt.Errorf("acquire init lock: %w", err)
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock(72544000)") //nolint:errcheck

	return s.runMigrations(ctx, conn.Conn())
}

// runMigrations applies the embedded migration set. The fs.FS is taken as a
// parameter (defaulting to migrationsFS) so tests can drive the runner with a
// synthetic migration set — e.g. to exercise the no-transaction path.
func (s *pgStore) runMigrations(ctx context.Context, conn *pgx.Conn) error {
	return s.runMigrationsFS(ctx, conn, migrationsFS)
}

func (s *pgStore) runMigrationsFS(ctx context.Context, conn *pgx.Conn, migs fs.FS) error {
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS nxs_anomaly_schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	files, err := fs.Glob(migs, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, f := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(f, "migrations/"), ".sql")

		var exists bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM nxs_anomaly_schema_migrations WHERE version=$1)", version,
		).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}

		raw, err := fs.ReadFile(migs, f)
		if err != nil {
			return err
		}
		sql := string(raw)

		// A migration whose first line is the marker `-- nxs:no-transaction` runs
		// outside a transaction, so it may use CREATE INDEX CONCURRENTLY / other
		// statements that PostgreSQL forbids inside a transaction block (needed to
		// add indexes on large tables without an ACCESS EXCLUSIVE lock). Such a
		// migration MUST be idempotent (use IF NOT EXISTS): it is not atomic, so a
		// failure can leave it partially applied and unrecorded.
		if strings.HasPrefix(strings.TrimSpace(sql), "-- nxs:no-transaction") {
			if err := s.runMigrationNoTx(ctx, conn, version, sql); err != nil {
				return err
			}
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		// Exempt DDL from the connection's statement_timeout: a large CREATE INDEX
		// or ALTER TABLE on an existing table can legitimately run long. SET LOCAL
		// scopes this to the migration transaction only.
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = 0"); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("migration %s: disable statement_timeout: %w", version, err)
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO nxs_anomaly_schema_migrations(version) VALUES($1)", version,
		); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// runMigrationNoTx runs a migration outside any transaction (for statements like
// CREATE INDEX CONCURRENTLY that cannot run in a transaction block) and records
// the version on success. statement_timeout is disabled for the session so a long
// index build is not killed.
func (s *pgStore) runMigrationNoTx(ctx context.Context, conn *pgx.Conn, version, sql string) error {
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return fmt.Errorf("migration %s: disable statement_timeout: %w", version, err)
	}
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("migration %s (no-transaction): %w", version, err)
	}
	if _, err := conn.Exec(ctx,
		"INSERT INTO nxs_anomaly_schema_migrations(version) VALUES($1)", version,
	); err != nil {
		return fmt.Errorf("migration %s: record version: %w", version, err)
	}
	return nil
}
