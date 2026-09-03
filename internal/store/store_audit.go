package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// pgExecer is satisfied by both *pgxpool.Pool and pgx.Tx, so an audit event can
// be inserted either standalone (its own statement) or inside an existing
// transaction — the latter is what makes a state transition and its audit record
// atomic (see updateCollections).
type pgExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertAuditEventExec appends one event via the given executor.
func insertAuditEventExec(ctx context.Context, ex pgExecer, ev AuditEvent) error {
	payload := ev.Data
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal audit data: %w", err)
	}
	// occurred_at is passed as NULL when unset so the column default applies and
	// the database clock, not the caller's, decides the ordering.
	var occurredAt any
	if ev.OccurredAt != "" {
		occurredAt = ev.OccurredAt
	}
	_, err = ex.Exec(ctx,
		`INSERT INTO nxs_anomaly_audit_events
		   (id, occurred_at, actor_id, actor_kind, actor_name, actor_role,
		    action, entity_type, entity_id, request_ip, request_id, trace_id, data)
		 VALUES($1, COALESCE($2::timestamptz, now()), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		ev.ID, occurredAt, ev.ActorID, ev.ActorKind, ev.ActorName, ev.ActorRole,
		ev.Action, ev.EntityType, ev.EntityID, ev.RequestIP, ev.RequestID, ev.TraceID, raw)
	return err
}

// AuditEvent is one append-only record of a mutating operation.
//
// It is a plain struct rather than a map[string]any (the convention for the
// State collections) because the audit table is never loaded into State,
// never mutated, and its shape is fixed: keeping it typed makes it impossible
// to write an event with a misspelled field.
type AuditEvent struct {
	ID         string
	OccurredAt string // RFC3339; empty means "let the database stamp it"
	ActorID    string
	ActorKind  string
	ActorName  string
	ActorRole  string
	Action     string
	EntityType string
	EntityID   string
	RequestIP  string
	// RequestID ties every event produced by one request together, and ties
	// them to the access log line and the error response for that request.
	RequestID string
	// TraceID ties the event to the distributed trace it happened in, which
	// spans processes the request id cannot reach — the worker cycle that
	// escalates a group is not the request that ingested it. Empty when tracing
	// is disabled. See migration 0025.
	TraceID string
	Data    map[string]any
}

// InsertAuditEvent appends one event as its own statement. There is deliberately
// no update or delete counterpart: the table's trigger rejects both. State
// transitions use the transactional path (updateCollections) instead, so the
// event cannot survive without its operation or vice versa.
func (s *pgStore) InsertAuditEvent(ctx context.Context, ev AuditEvent) error {
	return insertAuditEventExec(ctx, s.pool, ev)
}

// PruneAuditEvents deletes events older than cutoffISO, returning how many went.
//
// The table's trigger rejects DELETE, so this transaction disables it for the
// duration. That is a deliberate, narrow hole in the immutability guarantee,
// and it is worth being precise about what it does and does not weaken:
//
//   - the guarantee that *the API* cannot rewrite history is unchanged; no
//     request path reaches this method, only the worker's retention sweep;
//   - what it gives up is the database-level guarantee against the service's
//     own credentials. That was never absolute anyway — the service owns its
//     schema and connects as the table owner, so it could always have dropped
//     the trigger.
//
// The alternative was unbounded growth with pruning left as an undocumented
// manual ALTER, which in practice means it happens under time pressure, by
// hand, on a table nobody wants to be wrong about.
//
// Retention is off unless configured, so a deployment that wants the trigger
// never touched simply does not set it.
func (s *pgStore) PruneAuditEvents(ctx context.Context, cutoffISO string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rolled back only if Commit did not run

	// Scoped to this transaction: a crash mid-prune leaves the trigger enabled,
	// because the ALTER rolls back with everything else.
	if _, err := tx.Exec(ctx,
		"ALTER TABLE nxs_anomaly_audit_events DISABLE TRIGGER nxs_anomaly_audit_events_append_only"); err != nil {
		return 0, fmt.Errorf("disable append-only trigger: %w", err)
	}
	tag, err := tx.Exec(ctx,
		"DELETE FROM nxs_anomaly_audit_events WHERE occurred_at < $1::timestamptz", cutoffISO)
	if err != nil {
		return 0, fmt.Errorf("prune audit events: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"ALTER TABLE nxs_anomaly_audit_events ENABLE TRIGGER nxs_anomaly_audit_events_append_only"); err != nil {
		return 0, fmt.Errorf("re-enable append-only trigger: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ListAuditEvents returns events newest-first together with the total matching
// count. Supported filters: actor_id, entity_type, entity_id, action, request_id,
// trace_id.
func (s *pgStore) ListAuditEvents(ctx context.Context, filters map[string]any, limit, offset int) ([]map[string]any, int, error) {
	where := []string{"TRUE"}
	args := []any{}
	for _, col := range []string{"actor_id", "entity_type", "entity_id", "action", "request_id", "trace_id"} {
		v, ok := filters[col]
		if !ok {
			continue
		}
		s, _ := v.(string)
		if s == "" {
			continue
		}
		args = append(args, s)
		where = append(where, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM nxs_anomaly_audit_events WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx,
		`SELECT id, occurred_at, actor_id, actor_kind, actor_name, actor_role,
		        action, entity_type, entity_id, request_ip, request_id, trace_id, data
		   FROM nxs_anomaly_audit_events
		  WHERE `+clause+
			fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var (
			id, actorID, actorKind, actorName, actorRole string
			action, entityType, entityID, requestIP      string
			requestID, traceID                           string
			occurredAt                                   time.Time
			raw                                          []byte
		)
		if err := rows.Scan(&id, &occurredAt, &actorID, &actorKind, &actorName, &actorRole,
			&action, &entityType, &entityID, &requestIP, &requestID, &traceID, &raw); err != nil {
			return nil, 0, err
		}
		data := map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &data)
		}
		out = append(out, map[string]any{
			"id":          id,
			"occurred_at": utils.ToISO(occurredAt),
			"actor_id":    actorID,
			"actor_kind":  actorKind,
			"actor_name":  actorName,
			"actor_role":  actorRole,
			"action":      action,
			"entity_type": entityType,
			"entity_id":   entityID,
			"request_ip":  requestIP,
			"request_id":  requestID,
			"trace_id":    traceID,
			"data":        data,
		})
	}
	return out, total, rows.Err()
}
