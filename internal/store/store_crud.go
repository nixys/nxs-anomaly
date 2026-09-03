package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// collectionDefaultWhere holds extra WHERE clauses applied automatically to
// list/page queries for specific collections (e.g. excluding soft-deleted rows).
var collectionDefaultWhere = map[string]string{
	"integrations": "deleted_at IS NULL",
}

// UpdateCollections loads collections, runs mutator under advisory lock, saves dirty collections.
// Only rows actually changed by the mutator are written back: a canonical-JSON
// snapshot of each loaded row is taken before the mutator runs, and rows whose
// serialized form is unchanged afterwards are skipped at save time.
func (s *pgStore) UpdateCollections(ctx context.Context, loadCollections, saveCollections []string, mutator func(*State) (any, error), lockKey int64) (any, error) {
	specs := make([]LoadSpec, len(loadCollections))
	for i, col := range loadCollections {
		specs[i] = LoadSpec{Collection: col}
	}
	return s.UpdateCollectionsFiltered(ctx, specs, saveCollections, mutator, lockKey)
}

// UpdateCollectionsFiltered is UpdateCollections with per-collection load
// filters: each LoadSpec with non-nil Filters loads only the matching rows
// (buildWhere-validated typed columns), so mutators that touch a known subset
// of rows don't pull whole tables into memory under the advisory lock.
// The dirty-diff save semantics make partial loads safe: rows that were not
// loaded have no baseline, and mutators never delete from state maps, so
// unloaded rows are simply never written back.
func (s *pgStore) UpdateCollectionsFiltered(ctx context.Context, loads []LoadSpec, saveCollections []string, mutator func(*State) (any, error), lockKey int64) (any, error) {
	return s.updateCollections(ctx, loads, saveCollections, nil, mutator, lockKey)
}

// UpdateCollectionsWriteAll is UpdateCollectionsFiltered for hot paths that load
// exactly the rows they rewrite: collections named in writeAll skip the
// pre-mutation baseline snapshot and are written unconditionally, instead of
// being marshaled twice (once for the baseline diff, once at save). This is safe
// only when every loaded row of those collections is mutated — the dirty diff
// would then write all of them anyway, so the result is byte-identical with one
// fewer marshal pass. Used by delivery/retry, which finalize every claimed row.
func (s *pgStore) UpdateCollectionsWriteAll(ctx context.Context, loads []LoadSpec, saveCollections, writeAll []string, mutator func(*State) (any, error), lockKey int64) (any, error) {
	set := make(map[string]bool, len(writeAll))
	for _, c := range writeAll {
		set[c] = true
	}
	return s.updateCollections(ctx, loads, saveCollections, set, mutator, lockKey)
}

// updateCollections is the shared body behind UpdateCollections(Filtered/WriteAll).
// A lockKey <= 0 skips the advisory lock entirely: hot paths whose rows are
// already claimed exclusively (delivery/retry, via FOR UPDATE SKIP LOCKED) don't
// need a global serialization lock around save. Collections in the writeAll set
// skip the baseline snapshot and are written unconditionally.
func (s *pgStore) updateCollections(ctx context.Context, loads []LoadSpec, saveCollections []string, writeAll map[string]bool, mutator func(*State) (any, error), lockKey int64) (any, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if lockKey > 0 {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", lockKey)); err != nil {
			return nil, fmt.Errorf("acquire lock %d: %w", lockKey, err)
		}
	}

	state := NewState()
	if err := s.loadPartialStateTx(ctx, tx, loads, state); err != nil {
		return nil, err
	}

	// Snapshot save collections before the mutator runs (mutators modify items
	// in place), so unchanged rows can be detected and skipped at save time.
	// writeAll collections skip this: their rows are all rewritten, so the
	// baseline would never spare a write and only doubles the marshal work.
	baselines := make(map[string]map[string][]byte, len(saveCollections))
	for _, col := range saveCollections {
		if writeAll[col] {
			continue
		}
		baselines[col] = snapshotCollection(state.recordsOf(col))
	}

	result, err := mutator(state)
	if err != nil {
		return nil, err
	}

	for _, col := range saveCollections {
		if err := s.upsertCollection(ctx, tx, col, state.recordsOf(col), baselines[col]); err != nil {
			return nil, fmt.Errorf("save %s: %w", col, err)
		}
	}

	// Audit records the mutator enrolled go in the same transaction as the state
	// change: either both commit or neither does. A DB failure here rolls the
	// whole operation back rather than leaving a committed change unaudited.
	for _, ev := range state.AuditEvents {
		if err := insertAuditEventExec(ctx, tx, ev); err != nil {
			return nil, fmt.Errorf("insert audit event: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *pgStore) loadPartialStateTx(ctx context.Context, tx pgx.Tx, loads []LoadSpec, state *State) error {
	for _, spec := range loads {
		col := spec.Collection
		table, ok := EntityTables[col]
		if !ok {
			continue
		}
		q := fmt.Sprintf("SELECT id, data FROM %s", table)
		var args []any
		if len(spec.Filters) > 0 {
			where, whereArgs, err := buildWhere(col, spec.Filters)
			if err != nil {
				return fmt.Errorf("load %s: %w", col, err)
			}
			q += " WHERE " + where
			args = whereArgs
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("load %s: %w", col, err)
		}
		recs := make(map[string]Record)
		for rows.Next() {
			var id string
			var dataJSON []byte
			if err := rows.Scan(&id, &dataJSON); err != nil {
				rows.Close()
				return err
			}
			rec, err := decodeRecord(col, id, dataJSON)
			if err != nil {
				rows.Close()
				return err
			}
			recs[id] = rec
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(recs) >= loadCollectionMaxRows {
			slog.Warn("collection_load_limit_reached",
				"collection", col,
				"rows", len(recs),
				"threshold", loadCollectionMaxRows,
			)
		}
		state.setRecordsOf(col, recs)
	}
	return nil
}

// ReadCollections returns a snapshot of the named collections. Rows are decoded
// through decodeRecord so typed collections (alert_groups, alerts, notifications,
// notification_batches) land in their typed State fields just like they would
// under loadPartialStateTx; map-backed collections are unwrapped to raw maps via
// setRecordsOf and SetCollection — same behavior as before.
func (s *pgStore) ReadCollections(ctx context.Context, collections []string) (*State, error) {
	state := NewState()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	for _, col := range collections {
		table, ok := EntityTables[col]
		if !ok {
			continue
		}
		rows, err := conn.Query(ctx, fmt.Sprintf("SELECT id, data FROM %s", table))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", col, err)
		}
		recs := make(map[string]Record)
		for rows.Next() {
			var id string
			var dataJSON []byte
			if err := rows.Scan(&id, &dataJSON); err != nil {
				rows.Close()
				return nil, err
			}
			rec, err := decodeRecord(col, id, dataJSON)
			if err != nil {
				rows.Close()
				return nil, err
			}
			recs[id] = rec
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		state.setRecordsOf(col, recs)
	}
	return state, nil
}

// ListCollection returns all items in a collection.
// For collections with collectionDefaultWhere (e.g. integrations), soft-deleted
// rows are automatically excluded.
func (s *pgStore) ListCollection(ctx context.Context, collection string) ([]map[string]any, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", collection)
	}
	q := fmt.Sprintf("SELECT data FROM %s", table)
	if w, ok := collectionDefaultWhere[collection]; ok {
		q += " WHERE " + w
	}
	q += " ORDER BY id"
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// GetItem returns a single item by ID, or nil if not found.
func (s *pgStore) GetItem(ctx context.Context, collection, id string) (map[string]any, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", collection)
	}
	var dataJSON []byte
	err := s.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT data FROM %s WHERE id=$1", table), id,
	).Scan(&dataJSON)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal(dataJSON, &item); err != nil {
		return nil, err
	}
	return item, nil
}

// UpsertItem inserts or updates an item (must have an "id" field).
func (s *pgStore) UpsertItem(ctx context.Context, collection string, item map[string]any) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.upsertItem(ctx, tx, collection, item); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteItem removes an item and returns it, or nil if not found.
func (s *pgStore) DeleteItem(ctx context.Context, collection, id string) (map[string]any, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", collection)
	}
	var dataJSON []byte
	err := s.pool.QueryRow(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE id=$1 RETURNING data", table), id,
	).Scan(&dataJSON)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal(dataJSON, &item); err != nil {
		return nil, err
	}
	return item, nil
}

// SoftDeleteItem sets deleted_at = NOW() on an item and returns it.
// The item must have a typed "deleted_at" column in TypedColumns.
// Returns nil (not found) if the item does not exist.
func (s *pgStore) SoftDeleteItem(ctx context.Context, collection, id string) (map[string]any, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", collection)
	}
	now := utils.ToISO(utils.UTCNow())
	// Update both the typed column and the JSONB data field.
	var dataJSON []byte
	err := s.pool.QueryRow(ctx,
		fmt.Sprintf(`UPDATE %s SET deleted_at=$1::timestamptz, data=jsonb_set(data, '{deleted_at}', to_jsonb($1::text)) WHERE id=$2 RETURNING data`, table),
		now, id,
	).Scan(&dataJSON)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal(dataJSON, &item); err != nil {
		return nil, err
	}
	return item, nil
}

// UpsertItemAudited is UpsertItem that also writes an audit event in the same
// transaction, so a single-item write and its audit record are atomic.
func (s *pgStore) UpsertItemAudited(ctx context.Context, collection string, item map[string]any, ev AuditEvent) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.upsertItem(ctx, tx, collection, item); err != nil {
		return err
	}
	if err := insertAuditEventExec(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteItemAudited is DeleteItem that writes an audit event in the same
// transaction. When nothing was deleted (already gone) no audit is written.
func (s *pgStore) DeleteItemAudited(ctx context.Context, collection, id string, ev AuditEvent) (map[string]any, error) {
	return s.deleteAudited(ctx, collection, id, ev, false)
}

// SoftDeleteItemAudited is SoftDeleteItem with an atomic audit event.
func (s *pgStore) SoftDeleteItemAudited(ctx context.Context, collection, id string, ev AuditEvent) (map[string]any, error) {
	return s.deleteAudited(ctx, collection, id, ev, true)
}

func (s *pgStore) deleteAudited(ctx context.Context, collection, id string, ev AuditEvent, soft bool) (map[string]any, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", collection)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var dataJSON []byte
	if soft {
		now := utils.ToISO(utils.UTCNow())
		err = tx.QueryRow(ctx,
			fmt.Sprintf(`UPDATE %s SET deleted_at=$1::timestamptz, data=jsonb_set(data, '{deleted_at}', to_jsonb($1::text)) WHERE id=$2 RETURNING data`, table),
			now, id).Scan(&dataJSON)
	} else {
		err = tx.QueryRow(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE id=$1 RETURNING data", table), id).Scan(&dataJSON)
	}
	if err != nil {
		if isNotFound(err) {
			return nil, nil // nothing changed → no audit
		}
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal(dataJSON, &item); err != nil {
		return nil, err
	}
	if err := insertAuditEventExec(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return item, nil
}

// NotEqualFilter wraps a value for a != comparison in CountCollection filters.
type NotEqualFilter struct{ Value any }

// InOrNullFilter matches rows whose column is one of Values *or* is NULL.
//
// It exists for team scoping, where "unassigned" and "assigned to a team of
// mine" are equally visible. Expressing that as two separate filters is not
// possible — the filter map is a conjunction — and expressing it in the caller
// would mean building SQL outside buildWhere, which is the one place that
// validates column names.
type InOrNullFilter struct{ Values []any }

// CountCollection returns the number of items matching optional structured filters.
func (s *pgStore) CountCollection(ctx context.Context, collection string, filters map[string]any) (int, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return 0, fmt.Errorf("unknown collection: %s", collection)
	}
	where, args, err := buildWhere(collection, filters)
	if err != nil {
		return 0, err
	}
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if where != "" {
		q += " WHERE " + where
	}
	var count int
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// CollectionsHaveAny returns true if any of the named collections is non-empty.
// All tables are checked in a single UNION ALL query (one round-trip).
func (s *pgStore) CollectionsHaveAny(ctx context.Context, collections []string) (bool, error) {
	var parts []string
	for _, col := range collections {
		table, ok := EntityTables[col]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("SELECT 1 FROM %s", table))
	}
	if len(parts) == 0 {
		return false, nil
	}
	q := "SELECT EXISTS (" + strings.Join(parts, " UNION ALL ") + " LIMIT 1)"
	var exists bool
	if err := s.pool.QueryRow(ctx, q).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// ClearCollections deletes all rows from the named collections.
func (s *pgStore) ClearCollections(ctx context.Context, collections []string) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, col := range collections {
		table, ok := EntityTables[col]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			return fmt.Errorf("clear %s: %w", col, err)
		}
	}
	return tx.Commit(ctx)
}
