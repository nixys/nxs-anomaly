package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Deployment metadata: the single row of nxs_anomaly_metadata (id = 1, created
// by migration 0001 and unused until now).
//
// This is for facts about the installation itself rather than about any domain
// object — when a backup was last reported, which readiness blockers an operator
// has accepted. Such facts have no natural entity to hang off, and inventing a
// collection for a handful of keys would mean a table, a State field and a place
// in every load/save path for no gain.
//
// The row is written with a read-modify-write inside one transaction and merged
// key by key, so two writers touching different keys do not clobber each other.

// GetMetadata returns the deployment metadata document, or an empty (non-nil)
// map when the row does not exist yet.
func (s *pgStore) GetMetadata(ctx context.Context) (map[string]any, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM nxs_anomaly_metadata WHERE id = 1`).Scan(&raw)
	if err != nil {
		if isNotFound(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode metadata: %w", err)
		}
	}
	return out, nil
}

// PatchMetadata merges patch into the metadata document and returns the result.
// A nil value deletes its key. The read-modify-write happens under a row lock in
// one transaction, so concurrent patches of different keys both survive.
func (s *pgStore) PatchMetadata(ctx context.Context, patch map[string]any) (map[string]any, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin metadata patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE serialises concurrent patches; the row may not exist yet, which
	// is not an error — the upsert below creates it.
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT data FROM nxs_anomaly_metadata WHERE id = 1 FOR UPDATE`).Scan(&raw)
	if err != nil && !isNotFound(err) {
		return nil, fmt.Errorf("lock metadata: %w", err)
	}
	doc := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("decode metadata: %w", err)
		}
	}
	for k, v := range patch {
		if v == nil {
			delete(doc, k)
			continue
		}
		doc[k] = v
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode metadata: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO nxs_anomaly_metadata (id, data) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		encoded); err != nil {
		return nil, fmt.Errorf("write metadata: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit metadata patch: %w", err)
	}
	return doc, nil
}
