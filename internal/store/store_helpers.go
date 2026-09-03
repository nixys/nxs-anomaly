package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// buildUpsertQuery returns the SQL and args for a single-item upsert.
//
// For notifications the query uses INSERT...SELECT WHERE NOT EXISTS to prevent
// duplicate-key violations on idempotency_key when the same escalation step
// fires more than once for the same alert group (REPEAT steps, concurrent ingest).
// The WHERE NOT EXISTS guard is skipped when the same notification id is being
// re-saved (status updates after delivery), because in that case no other row
// shares the same (idempotency_key, id != $1) pair, so NOT EXISTS is true and
// the row flows through to ON CONFLICT(id) DO UPDATE normally.
func buildUpsertQuery(collection string, item map[string]any) (string, []any, error) {
	rec, err := wrapRecord(collection, item)
	if err != nil {
		return "", nil, err
	}
	dataJSON, err := rec.MarshalData()
	if err != nil {
		return "", nil, err
	}
	return buildUpsertQueryData(collection, rec, dataJSON)
}

// buildUpsertQueryData is buildUpsertQuery with the row's JSON already marshaled,
// so callers that marshal for change detection don't marshal twice. It takes a
// Record so both map-backed and typed rows share one upsert path.
func buildUpsertQueryData(collection string, rec Record, dataJSON []byte) (string, []any, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return "", nil, fmt.Errorf("unknown collection: %s", collection)
	}
	id := rec.RecordID()
	if id == "" {
		return "", nil, fmt.Errorf("item missing id field")
	}
	cols := TypedColumns[collection]
	if len(cols) == 0 {
		return fmt.Sprintf("INSERT INTO %s(id,data) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data", table),
			[]any{id, dataJSON}, nil
	}
	colNames := []string{"id", "data"}
	colNames = append(colNames, cols...)
	placeholders := make([]string, len(colNames))
	for i := range colNames {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	updateClauses := make([]string, len(colNames)-1)
	for i, c := range colNames[1:] {
		updateClauses[i] = fmt.Sprintf("%s=EXCLUDED.%s", c, c)
	}
	args := []any{id, dataJSON}
	args = append(args, rec.TypedValues()...)

	if collection == "notifications" {
		// Find the 1-based placeholder index for idempotency_key.
		idemIdx := -1
		for i, c := range cols {
			if c == "idempotency_key" {
				idemIdx = 2 + i + 1 // $1=id, $2=data, then typed cols
				break
			}
		}
		if idemIdx > 0 {
			// INSERT...SELECT WHERE NOT EXISTS prevents a new row from being inserted
			// when a different row with the same idempotency_key already exists.
			// ON CONFLICT(id) DO UPDATE is still reached when re-saving the same row
			// (e.g. marking delivered), because the existing row has the SAME id,
			// so "id <> $1" is false and NOT EXISTS evaluates to true.
			q := fmt.Sprintf(
				`INSERT INTO %s(%s) SELECT %s `+
					`WHERE NOT EXISTS (SELECT 1 FROM %s WHERE idempotency_key=$%d AND idempotency_key IS NOT NULL AND id<>$1) `+
					`ON CONFLICT(id) DO UPDATE SET %s`,
				table, strings.Join(colNames, ","), strings.Join(placeholders, ","),
				table, idemIdx,
				strings.Join(updateClauses, ","),
			)
			return q, args, nil
		}
	}

	q := fmt.Sprintf(
		"INSERT INTO %s(%s) VALUES(%s) ON CONFLICT(id) DO UPDATE SET %s",
		table,
		strings.Join(colNames, ","),
		strings.Join(placeholders, ","),
		strings.Join(updateClauses, ","),
	)
	return q, args, nil
}

// snapshotCollection marshals every record to its canonical JSON form. Taken
// before a mutator runs so upsertCollection can tell which rows it changed.
func snapshotCollection(items map[string]Record) map[string][]byte {
	if len(items) == 0 {
		return nil
	}
	snap := make(map[string][]byte, len(items))
	for id, item := range items {
		if data, err := item.MarshalData(); err == nil {
			snap[id] = data
		}
	}
	return snap
}

// upsertCollection sends item upserts in a single pgx.Batch (one round-trip).
// Rows whose canonical JSON matches the baseline snapshot are skipped: the
// mutator did not change them, and rewriting them would waste WAL/index work
// and could clobber concurrent updates made under a different advisory lock key.
// Rows without a baseline entry (newly created, or from an unloaded collection)
// are always written.
func (s *pgStore) upsertCollection(ctx context.Context, tx pgx.Tx, collection string, items map[string]Record, baseline map[string][]byte) error {
	if len(items) == 0 {
		return nil
	}
	var batch pgx.Batch
	for id, item := range items {
		dataJSON, err := item.MarshalData()
		if err != nil {
			return err
		}
		if prev, ok := baseline[id]; ok && bytes.Equal(prev, dataJSON) {
			continue
		}
		q, args, err := buildUpsertQueryData(collection, item, dataJSON)
		if err != nil {
			return err
		}
		batch.Queue(q, args...)
	}
	if batch.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, &batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

func (s *pgStore) upsertItem(ctx context.Context, tx pgx.Tx, collection string, item map[string]any) error {
	q, args, err := buildUpsertQuery(collection, item)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, q, args...)
	return err
}

func scanRows(rows pgx.Rows) ([]map[string]any, error) {
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var dataJSON []byte
		if err := rows.Scan(&dataJSON); err != nil {
			return nil, err
		}
		var item map[string]any
		if err := json.Unmarshal(dataJSON, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func isNotFound(err error) bool {
	return err == pgx.ErrNoRows
}

// buildWhere builds a WHERE clause and args from a validated filter map.
// Values can be plain strings (equality) or slices (IN clause).
func buildWhere(collection string, filters map[string]any) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}
	allowed := map[string]bool{"id": true}
	for _, col := range TypedColumns[collection] {
		allowed[col] = true
	}
	var clauses []string
	var args []any
	// Sort keys for deterministic output.
	keys := make([]string, 0, len(filters))
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return "", nil, fmt.Errorf("unsupported filter column %q for collection %s", k, collection)
		}
		v := filters[k]
		switch val := v.(type) {
		case NotEqualFilter:
			args = append(args, val.Value)
			clauses = append(clauses, fmt.Sprintf("%s!=$%d", k, len(args)))
		case InOrNullFilter:
			// No values means "only the unassigned rows" — the correct answer
			// for someone who belongs to no team, not an empty result.
			if len(val.Values) == 0 {
				clauses = append(clauses, fmt.Sprintf("%s IS NULL", k))
				continue
			}
			phs := make([]string, len(val.Values))
			for i, item := range val.Values {
				args = append(args, item)
				phs[i] = fmt.Sprintf("$%d", len(args))
			}
			clauses = append(clauses, fmt.Sprintf("(%s IN (%s) OR %s IS NULL)", k, strings.Join(phs, ","), k))
		case []any:
			if len(val) == 0 {
				clauses = append(clauses, "FALSE")
				continue
			}
			phs := make([]string, len(val))
			for i, item := range val {
				args = append(args, item)
				phs[i] = fmt.Sprintf("$%d", len(args))
			}
			clauses = append(clauses, fmt.Sprintf("%s IN (%s)", k, strings.Join(phs, ",")))
		default:
			args = append(args, val)
			clauses = append(clauses, fmt.Sprintf("%s=$%d", k, len(args)))
		}
	}
	return strings.Join(clauses, " AND "), args, nil
}
