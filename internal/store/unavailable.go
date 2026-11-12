package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUnavailable reports whether err means "the database could not serve this
// right now", as opposed to "this request was wrong".
//
// The distinction is the difference between an alert being retried and an alert
// being dropped. Ingest clients — Alertmanager, Grafana, any webhook sender —
// decide from the status code whether to try again; a 500 reads as "your
// request is broken", while a 503 says "come back in a moment". Answering 500
// to a database restart therefore turns a blip into lost alerts, which is the
// worst failure this product has.
//
// Two classes count, both observed while restarting PostgreSQL under live
// ingest:
//
//   - the dial never completed — connection refused, reset, unresolvable host, a
//     server still starting up. Shared with the store's own connect retry.
//   - the server answered with a SQLSTATE that says it is going away or cannot
//     take the connection: class 57 (operator intervention, e.g. 57P01
//     "terminating connection due to administrator command", which is exactly
//     what a rolling restart produces) and class 08 (connection exception).
//
// Everything else — constraint violations, syntax errors, permission denials —
// is the caller's problem and stays a 500 or a 4xx, because retrying it would
// only produce the same answer.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if isRetryableConnectError(err) {
		return true
	}
	// A transaction PostgreSQL rolled back for colliding with another is
	// retryable in the same sense: the sender should come back, not give up.
	if IsTransactionConflict(err) {
		return true
	}
	// A write that reached a standby after a failover (see failover.go): the
	// pool is reset on it, and the retry reaches the new primary.
	if isReadOnlyError(err) || errors.Is(err, errStandby) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 {
		switch pgErr.Code[:2] {
		case "57", "08":
			return true
		}
	}
	return false
}

// IsTransactionConflict reports whether err is PostgreSQL rolling a
// transaction back because it collided with another one: 40P01
// deadlock_detected or 40001 serialization_failure. The whole transaction was
// undone, so running it again is safe and usually succeeds.
//
// Seen on a sandbox: deleting a user cascades to their notifications while the
// worker inserts new ones for them (resolve notices for 900 groups closed a
// moment earlier). Each waited for the other, PostgreSQL killed the delete, and
// the admin got "internal error" for a request that worked when repeated.
func IsTransactionConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

// retryConflicts runs fn up to three times while it fails with a transaction
// conflict, backing off briefly so the other transaction can finish.
func retryConflicts(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = fn(); !IsTransactionConflict(err) {
			return err
		}
		select {
		case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
		case <-ctx.Done():
			return err
		}
	}
	return err
}
