package store

import "context"

// store_retention.go holds the deletes that exist only to bound how long data is
// kept. They are called from the worker's retention sweep and from nowhere else;
// no request path reaches them.
//
// Each one deletes in bounded batches for the same reason DeleteOldResolvedGroups
// does: the first sweep after an operator sets a horizon on a long-lived
// installation can face millions of rows, and one unbounded DELETE would hold a
// transaction open for minutes and bloat the table it is trying to shrink.
// Whatever is left over continues on the next cycle, a few seconds later.

// retentionBatchSize bounds the rows one statement deletes.
const retentionBatchSize = 1000

// retentionMaxBatchesPerCall bounds how long one worker cycle spends on a single
// category, so a large backlog in one table cannot starve the others.
const retentionMaxBatchesPerCall = 10

// deleteInBatches runs sql (which must delete at most $2 rows and take the
// cutoff as $1) until it stops finding rows or the per-cycle budget is spent.
func (s *pgStore) deleteInBatches(ctx context.Context, sql, cutoffISO string) (int, error) {
	total := 0
	for i := 0; i < retentionMaxBatchesPerCall; i++ {
		tag, err := s.pool.Exec(ctx, sql, cutoffISO, retentionBatchSize)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < retentionBatchSize {
			break
		}
	}
	return total, nil
}

// DeleteOldNotifications removes notifications that have finished their delivery
// state machine and are older than the cutoff.
//
// Only terminal rows are eligible. A notification still in flight
// (delivery_scheduled, retry_scheduled, delivering, retrying, batched) is work
// the worker has not completed, and deleting it would silently drop a page
// somebody is waiting for — a retention horizon is about history, not about the
// queue. Their delivery attempts go with them through the FK's ON DELETE CASCADE.
func (s *pgStore) DeleteOldNotifications(ctx context.Context, cutoffISO string) (int, error) {
	return s.deleteInBatches(ctx, `
		DELETE FROM nxs_anomaly_notifications
		 WHERE id IN (
		   SELECT id FROM nxs_anomaly_notifications
		    WHERE status IN ('delivered','failed','skipped')
		      AND updated_at < $1::timestamptz
		    ORDER BY updated_at
		    LIMIT $2
		 )`, cutoffISO)
}

// DeleteOldDeliveryAttempts removes per-attempt provider records older than the
// cutoff, independently of the notifications they belong to.
//
// Independent on purpose: the attempt detail (response excerpts, error strings,
// timings) is the most verbose and the shortest-lived thing here, and an
// installation reasonably keeps "who was paged and whether it worked" far longer
// than "what the provider's body said on the third try".
func (s *pgStore) DeleteOldDeliveryAttempts(ctx context.Context, cutoffISO string) (int, error) {
	return s.deleteInBatches(ctx, `
		DELETE FROM nxs_anomaly_notification_delivery_attempts
		 WHERE id IN (
		   SELECT id FROM nxs_anomaly_notification_delivery_attempts
		    WHERE created_at < $1::timestamptz
		    ORDER BY created_at
		    LIMIT $2
		 )`, cutoffISO)
}

// DeleteOldWebSessions removes browser sessions created before the cutoff,
// whether or not they are still live.
//
// Distinct from DeleteExpiredWebSessions, which removes sessions that have
// lapsed. This one bounds the client IP and user agent a session row carries by
// age, so a long-lived session does not keep a person's address on file
// indefinitely. A still-valid session deleted here signs that browser out, which
// is the intended consequence of an installation choosing a horizon shorter than
// its session lifetime.
func (s *pgStore) DeleteOldWebSessions(ctx context.Context, cutoffISO string) (int, error) {
	return s.deleteInBatches(ctx, `
		DELETE FROM nxs_anomaly_web_sessions
		 WHERE id IN (
		   SELECT id FROM nxs_anomaly_web_sessions
		    WHERE created_at < $1::timestamptz
		    ORDER BY created_at
		    LIMIT $2
		 )`, cutoffISO)
}
