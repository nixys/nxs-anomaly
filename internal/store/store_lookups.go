package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LastAlertReceivedAt returns when an integration last delivered an alert, and
// false when it never has.
//
// Read rather than stamped: keeping a "last seen" column on the integration
// would mean writing that row on every single ingest, which is the hottest path
// in the service. The answer is already in the alerts table, and
// nxs_anomaly_alerts_integration_received_idx is (integration_id, received_at
// desc) — exactly this query's shape. The silence alert the heartbeat raises on
// this integration is excluded: it is the service talking, not the source, and
// counting it closed the silence on the next pass.
func (s *pgStore) LastAlertReceivedAt(ctx context.Context, integrationID string) (time.Time, bool, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT received_at FROM nxs_anomaly_alerts
		 WHERE integration_id = $1 AND received_at IS NOT NULL
		   AND NOT (COALESCE(data->'labels'->>'alertname', '') = 'SourceSilent'
		            AND COALESCE(data->'labels'->>'integration_id', '') = $1)
		 ORDER BY received_at DESC LIMIT 1`, integrationID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if at == nil {
		return time.Time{}, false, nil
	}
	return *at, true, nil
}

// FindIntegrationByKey looks up an integration by its routing key.
// Soft-deleted integrations (deleted_at IS NOT NULL) are excluded.
func (s *pgStore) FindIntegrationByKey(ctx context.Context, key string) (map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT data FROM nxs_anomaly_integrations WHERE key=$1 AND deleted_at IS NULL LIMIT 1", key)
	if err != nil {
		return nil, err
	}
	items, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}

// FindMobileSessionByToken looks up a live mobile session by token hash.
func (s *pgStore) FindMobileSessionByToken(ctx context.Context, tokenHash string) (map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT data FROM nxs_anomaly_mobile_sessions
		  WHERE token=$1 AND revoked_at IS NULL AND expires_at > now() LIMIT 1`, tokenHash)
	if err != nil {
		return nil, err
	}
	items, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}

// CreateMobilePairingCode stores a pairing code hash. Expired codes are swept
// here rather than by the worker: they are few, and issuing a code is the only
// thing that adds to the table.
func (s *pgStore) CreateMobilePairingCode(ctx context.Context, codeHash, userID string, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx,
		"DELETE FROM nxs_anomaly_mobile_verification_tokens WHERE expires_at <= now()"); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO nxs_anomaly_mobile_verification_tokens (token, user_id, expires_at)
		 VALUES ($1, $2, $3)`, codeHash, userID, expiresAt)
	return err
}

// RedeemMobilePairingCode deletes the code and returns its user in one
// statement. An expired code is deleted too, but yields "".
func (s *pgStore) RedeemMobilePairingCode(ctx context.Context, codeHash string) (string, error) {
	var userID string
	var live bool
	err := s.pool.QueryRow(ctx,
		`DELETE FROM nxs_anomaly_mobile_verification_tokens WHERE token=$1
		 RETURNING user_id, expires_at > now()`, codeHash).Scan(&userID, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !live {
		return "", nil
	}
	return userID, nil
}

// FindActiveAlertGroup looks up an open alert group by integration+dedupe key.
func (s *pgStore) FindActiveAlertGroup(ctx context.Context, integrationID, dedupeKey string) (map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT data FROM nxs_anomaly_alert_groups
		 WHERE integration_id=$1 AND dedupe_key=$2 AND status NOT IN ('resolved','silenced')
		 LIMIT 1`,
		integrationID, dedupeKey)
	if err != nil {
		return nil, err
	}
	items, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}

// ListDueAlertGroups returns alert groups whose next_run_at <= nowISO,
// oldest first so a backlog larger than the batch limit cannot starve groups.
func (s *pgStore) ListDueAlertGroups(ctx context.Context, nowISO string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT data FROM nxs_anomaly_alert_groups WHERE next_run_at <= $1 AND status <> 'resolved' ORDER BY next_run_at LIMIT $2",
		nowISO, workerBatchLimit)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// ListDueNotificationBatches returns open batches whose flush_at <= nowISO,
// oldest first so a backlog larger than the batch limit cannot starve batches.
func (s *pgStore) ListDueNotificationBatches(ctx context.Context, nowISO string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT data FROM nxs_anomaly_notification_batches WHERE flush_at <= $1 AND status='open' ORDER BY flush_at LIMIT $2",
		nowISO, workerBatchLimit)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// OldestDueEscalationAgeSeconds reports how long the oldest currently-due alert
// group has been waiting to escalate: now - min(next_run_at) over open groups
// whose next_run_at is already past. Returns 0 when nothing is due. A rising
// value means the (single) escalation stage is falling behind — the signal that
// escalation-lock sharding is worth doing.
func (s *pgStore) OldestDueEscalationAgeSeconds(ctx context.Context) (float64, error) {
	var age float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(next_run_at))), 0)
		 FROM nxs_anomaly_alert_groups
		 WHERE status NOT IN ('resolved','silenced') AND next_run_at IS NOT NULL AND next_run_at <= now()`,
	).Scan(&age)
	if err != nil {
		return 0, err
	}
	if age < 0 {
		age = 0
	}
	return age, nil
}

// OldestPendingDeliveryAgeSeconds reports how long the oldest delivery_scheduled
// notification has been waiting: now - min(created_at). Returns 0 when none. A
// rising value means delivery throughput is not keeping up with ingest.
func (s *pgStore) OldestPendingDeliveryAgeSeconds(ctx context.Context) (float64, error) {
	var age float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))), 0)
		 FROM nxs_anomaly_notifications WHERE status='delivery_scheduled'`,
	).Scan(&age)
	if err != nil {
		return 0, err
	}
	if age < 0 {
		age = 0
	}
	return age, nil
}

// ClaimDeliverableNotifications atomically claims a batch of delivery_scheduled
// notifications for this worker: the inner SELECT ... FOR UPDATE SKIP LOCKED picks
// rows no other transaction holds (oldest first, matching the 0008 partial index),
// and the UPDATE flips them to 'delivering' with claimed_at/claimed_by recorded in
// data so the reaper can recover them if this worker dies. RETURNING gives the
// claimed rows for delivery — each row is therefore handed to exactly one worker.
func (s *pgStore) ClaimDeliverableNotifications(ctx context.Context, workerID, nowISO string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE nxs_anomaly_notifications SET
		    status='delivering',
		    data = data || jsonb_build_object('status','delivering','claimed_at',$1::text,'claimed_by',$2::text)
		 WHERE id IN (
		    SELECT id FROM nxs_anomaly_notifications
		    WHERE status='delivery_scheduled'
		    ORDER BY created_at, id
		    LIMIT $3
		    FOR UPDATE SKIP LOCKED
		 )
		 RETURNING data`,
		nowISO, workerID, workerBatchLimit)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// ClaimRetryableNotifications atomically claims due retry_scheduled notifications,
// moving them to 'retrying'. See ClaimDeliverableNotifications.
func (s *pgStore) ClaimRetryableNotifications(ctx context.Context, workerID, nowISO string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE nxs_anomaly_notifications SET
		    status='retrying',
		    data = data || jsonb_build_object('status','retrying','claimed_at',$4::text,'claimed_by',$2::text)
		 WHERE id IN (
		    SELECT id FROM nxs_anomaly_notifications
		    WHERE status='retry_scheduled' AND next_retry_at <= $1
		    ORDER BY next_retry_at
		    LIMIT $3
		    FOR UPDATE SKIP LOCKED
		 )
		 RETURNING data`,
		nowISO, workerID, workerBatchLimit, nowISO)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// ReclaimStaleClaims resets notifications stuck in a transient claim status whose
// claimed_at predates cutoffISO (the owning worker crashed mid-delivery) back to
// their pending status so a healthy worker re-picks them next cycle. The partial
// index nxs_anomaly_notifications_claimed_idx keeps the scanned set small.
func (s *pgStore) ReclaimStaleClaims(ctx context.Context, cutoffISO string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE nxs_anomaly_notifications SET
		    status = CASE WHEN status='delivering' THEN 'delivery_scheduled' ELSE 'retry_scheduled' END,
		    data = data || jsonb_build_object('status',
		        CASE WHEN status='delivering' THEN 'delivery_scheduled' ELSE 'retry_scheduled' END)
		 WHERE status IN ('delivering','retrying')
		   AND (data->>'claimed_at') IS NOT NULL
		   AND (data->>'claimed_at')::timestamptz < $1::timestamptz`,
		cutoffISO)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ListCollectionPage returns paginated items and total count with optional filters.
// Collections with collectionDefaultWhere (e.g. integrations) automatically
// exclude soft-deleted rows.
func (s *pgStore) ListCollectionPage(ctx context.Context, collection string, filters map[string]any, limit, offset int, sort SortSpec) ([]map[string]any, int, error) {
	table, ok := EntityTables[collection]
	if !ok {
		return nil, 0, fmt.Errorf("unknown collection: %s", collection)
	}

	sort, err := ResolveSort(collection, sort)
	if err != nil {
		return nil, 0, err
	}
	where, args, err := buildWhere(collection, filters)
	if err != nil {
		return nil, 0, err
	}
	// Prepend the default WHERE clause (e.g. soft-delete filter) before user filters.
	if dw, ok := collectionDefaultWhere[collection]; ok {
		if where == "" {
			where = dw
		} else {
			where = dw + " AND " + where
		}
	}
	base := fmt.Sprintf("FROM %s", table)
	if where != "" {
		base += " WHERE " + where
	}

	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) "+base, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf("SELECT data %s%s LIMIT $%d OFFSET $%d", base, orderClause(collection, sort), len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	items, err := scanRows(rows)
	return items, total, err
}

// ListItemsIn returns items from a collection where field is in values.
func (s *pgStore) ListItemsIn(ctx context.Context, collection, field string, values []any) ([]map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	table, ok := EntityTables[collection]
	if !ok {
		return nil, fmt.Errorf("unknown collection: %s", collection)
	}
	allowed := map[string]bool{"id": true}
	for _, col := range TypedColumns[collection] {
		allowed[col] = true
	}
	if !allowed[field] {
		return nil, fmt.Errorf("unsupported field %q for collection %s", field, collection)
	}
	placeholders := make([]string, len(values))
	for i := range values {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	q := fmt.Sprintf("SELECT data FROM %s WHERE %s IN (%s)",
		table, field, strings.Join(placeholders, ","))
	rows, err := s.pool.Query(ctx, q, values...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// ListUnresolvedAlertGroups returns the unresolved alert groups whose field is
// in values. Only id and escalation_chain_id are accepted: both are indexed for
// unresolved groups (the primary key; migration 0032).
func (s *pgStore) ListUnresolvedAlertGroups(ctx context.Context, field string, values []any) ([]map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if field != "id" && field != "escalation_chain_id" {
		return nil, fmt.Errorf("unsupported field %q for unresolved alert groups", field)
	}
	placeholders := make([]string, len(values))
	for i := range values {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	q := fmt.Sprintf("SELECT data FROM %s WHERE %s IN (%s) AND status <> 'resolved'",
		EntityTables["alert_groups"], field, strings.Join(placeholders, ","))
	rows, err := s.pool.Query(ctx, q, values...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// PageUnresolvedAlertGroups: see the Store interface. Counting and ordering
// happen in the database — migration 0033 indexes unresolved groups by their
// latest alert — so a chat's "status" does not read every open group.
func (s *pgStore) PageUnresolvedAlertGroups(ctx context.Context, hiddenIntegrations []string, limit, offset int) ([]map[string]any, int, error) {
	where := "status <> 'resolved'"
	args := []any{}
	if len(hiddenIntegrations) > 0 {
		args = append(args, hiddenIntegrations)
		where += " AND (integration_id IS NULL OR NOT (integration_id = ANY($1)))"
	}
	table := EntityTables["alert_groups"]
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(
		"SELECT data FROM %s WHERE %s ORDER BY last_received_at DESC NULLS LAST, id LIMIT $%d OFFSET $%d",
		table, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	items, err := scanRows(rows)
	return items, total, err
}

// ListItemsByIDs returns items from a collection matching the given IDs.
func (s *pgStore) ListItemsByIDs(ctx context.Context, collection string, ids []string) ([]map[string]any, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	values := make([]any, len(ids))
	for i, id := range ids {
		values[i] = id
	}
	return s.ListItemsIn(ctx, collection, "id", values)
}

// QueryHistoryGroups queries alert groups with rich filters for the history view.
func (s *pgStore) QueryHistoryGroups(ctx context.Context, filters map[string]any, limit, offset int) ([]map[string]any, int, error) {
	where, args := buildHistoryWhere(filters)
	base := "FROM nxs_anomaly_alert_groups"
	if where != "" {
		base += " WHERE " + where
	}

	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) "+base, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf("SELECT data %s ORDER BY id DESC LIMIT $%d OFFSET $%d", base, len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	items, err := scanRows(rows)
	return items, total, err
}

// DeleteOldChatopsMessages removes chatops messages older than cutoffISO.
// created_at is read from the JSONB data field (no typed column required).
func (s *pgStore) DeleteOldChatopsMessages(ctx context.Context, cutoffISO string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM nxs_anomaly_chatops_messages
		 WHERE (data->>'created_at')::timestamptz < $1::timestamptz`,
		cutoffISO)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func buildHistoryWhere(filters map[string]any) (string, []any) {
	var clauses []string
	var args []any
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if v, ok := filters["integration_id"]; ok && v != nil && v != "" {
		clauses = append(clauses, "integration_id="+addArg(v))
	}
	// integration_ids narrows the history to the integrations a team-scoped
	// caller may reach. An empty list means they may reach none, which is an
	// empty result rather than an unrestricted one — the same shape as
	// team_user_ids below, and for the same reason.
	if raw, ok := filters["integration_ids"].([]any); ok {
		if len(raw) == 0 {
			clauses = append(clauses, "FALSE")
		} else {
			phs := make([]string, len(raw))
			for i, v := range raw {
				phs[i] = addArg(v)
			}
			clauses = append(clauses, "integration_id IN ("+strings.Join(phs, ",")+")")
		}
	}
	if v, ok := filters["severity"]; ok && v != nil && v != "" {
		// Same level-not-spelling rule as buildWhere: history filtered by
		// "critical" must show the incident its source called "sev1".
		family := SeverityFamily(fmt.Sprint(v))
		phs := make([]string, len(family))
		for i, alias := range family {
			phs[i] = addArg(alias)
		}
		clauses = append(clauses, "severity IN ("+strings.Join(phs, ",")+")")
	}
	if v, ok := filters["status"]; ok && v != nil && v != "" {
		clauses = append(clauses, "status="+addArg(v))
	}
	if v, ok := filters["from_at"]; ok && v != nil && v != "" {
		clauses = append(clauses, "last_received_at >= "+addArg(v))
	}
	if v, ok := filters["to_at"]; ok && v != nil && v != "" {
		clauses = append(clauses, "last_received_at <= "+addArg(v))
	}
	if v, ok := filters["channel"]; ok && v != nil && v != "" {
		ph := addArg(v)
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM nxs_anomaly_notifications n WHERE n.alert_group_id = nxs_anomaly_alert_groups.id AND n.channel = "+ph+")")
	}
	if v, ok := filters["user_id"]; ok && v != nil && v != "" {
		ph := addArg(v)
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM nxs_anomaly_notifications n WHERE n.alert_group_id = nxs_anomaly_alert_groups.id AND n.user_id = "+ph+")")
	}
	if raw, ok := filters["team_user_ids"].([]any); ok {
		if len(raw) == 0 {
			clauses = append(clauses, "FALSE")
		} else {
			phs := make([]string, len(raw))
			for i, v := range raw {
				phs[i] = addArg(v)
			}
			clauses = append(clauses,
				"EXISTS (SELECT 1 FROM nxs_anomaly_notifications n WHERE n.alert_group_id = nxs_anomaly_alert_groups.id AND n.user_id IN ("+strings.Join(phs, ",")+"))")
		}
	}
	return strings.Join(clauses, " AND "), args
}

// archivalBatchSize bounds the ids handled per archival transaction: a single
// unbounded IN list would exceed PostgreSQL's 65535-parameter limit on a large
// backlog (e.g. enabling TTL on a long-lived install) and produce one huge
// transaction.
const archivalBatchSize = 1000

// archivalMaxBatchesPerCall bounds how long one worker cycle spends archiving;
// any remaining backlog continues on the next cycle.
const archivalMaxBatchesPerCall = 10

// DeleteOldResolvedGroups removes resolved groups older than cutoffISO and
// returns the count. Deletion happens oldest-first in batches, each in its own
// short transaction.
func (s *pgStore) DeleteOldResolvedGroups(ctx context.Context, cutoffISO string) (int, error) {
	total := 0
	for i := 0; i < archivalMaxBatchesPerCall; i++ {
		n, err := s.deleteOldResolvedGroupsBatch(ctx, cutoffISO)
		total += n
		if err != nil {
			return total, err
		}
		if n < archivalBatchSize {
			break
		}
	}
	return total, nil
}

func (s *pgStore) deleteOldResolvedGroupsBatch(ctx context.Context, cutoffISO string) (int, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Get IDs to cascade-delete their alerts and notifications.
	rows, err := tx.Query(ctx,
		"SELECT id FROM nxs_anomaly_alert_groups WHERE status='resolved' AND resolved_at < $1 ORDER BY resolved_at LIMIT $2",
		cutoffISO, archivalBatchSize)
	if err != nil {
		return 0, err
	}
	var ids []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(ids) == 0 {
		return 0, tx.Commit(ctx)
	}

	placeholders := make([]string, len(ids))
	for i := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	inList := strings.Join(placeholders, ",")

	// Delete delivery_attempts first: they reference notifications via notification_id.
	if _, err := tx.Exec(ctx,
		fmt.Sprintf("DELETE FROM nxs_anomaly_notification_delivery_attempts WHERE notification_id IN (SELECT id FROM nxs_anomaly_notifications WHERE alert_group_id IN (%s))", inList), ids...); err != nil {
		return 0, err
	}
	for _, tbl := range []string{"nxs_anomaly_alerts", "nxs_anomaly_notifications"} {
		if _, err := tx.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE alert_group_id IN (%s)", tbl, inList), ids...); err != nil {
			return 0, err
		}
	}

	tag, err := tx.Exec(ctx,
		fmt.Sprintf("DELETE FROM nxs_anomaly_alert_groups WHERE id IN (%s)", inList), ids...)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
