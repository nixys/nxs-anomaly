package store

import (
	"errors"

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
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 {
		switch pgErr.Code[:2] {
		case "57", "08":
			return true
		}
	}
	return false
}
