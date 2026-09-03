package store

import "context"

// store_rate_limit.go backs the cluster-wide token bucket added by migration
// 0024. It exists for one limiter — sign-in attempts — where a per-process limit
// silently multiplies by the replica count. See the migration for the reasoning
// and docs/SECURITY_PROFILE.md for which limiters do and do not use it.

// ConsumeRateToken refills the bucket for key at ratePerSecond (capped at
// capacity), then takes one token if there is one, and reports whether it could.
//
// The row is created first and then locked FOR UPDATE, in one transaction. Both
// halves are load-bearing:
//
//   - Without the lock, two pods read the same token count and both spend it.
//   - Without creating the row first there is nothing to lock, and the very
//     first burst against a new key is unlimited: every concurrent caller finds
//     no row, assumes a full bucket, and is admitted. That is the burst that
//     matters for a brute-force limiter, and an earlier single-statement version
//     of this failed exactly there — forty concurrent attempts against a burst of
//     five let nineteen through.
//
// Callers on different keys lock different rows and do not contend.
func (s *pgStore) ConsumeRateToken(ctx context.Context, key string, ratePerSecond, capacity float64) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rolled back only if Commit did not run

	if _, err := tx.Exec(ctx,
		`INSERT INTO nxs_anomaly_rate_buckets (bucket_key, tokens, updated_at)
		 VALUES ($1, $2, now()) ON CONFLICT (bucket_key) DO NOTHING`,
		key, capacity); err != nil {
		return false, err
	}

	// Refill by however long the bucket sat idle, capped at capacity. Computed in
	// SQL rather than in Go so the elapsed time is measured against the database
	// clock — API replicas do not necessarily agree on what time it is.
	var tokens float64
	if err := tx.QueryRow(ctx,
		`SELECT LEAST($2::float8, tokens + EXTRACT(EPOCH FROM (now() - updated_at)) * $3::float8)
		 FROM nxs_anomaly_rate_buckets WHERE bucket_key = $1 FOR UPDATE`,
		key, capacity, ratePerSecond).Scan(&tokens); err != nil {
		return false, err
	}

	allowed := tokens >= 1
	if allowed {
		tokens--
	}
	if _, err := tx.Exec(ctx,
		"UPDATE nxs_anomaly_rate_buckets SET tokens = $2, updated_at = now() WHERE bucket_key = $1",
		key, tokens); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return allowed, nil
}

// RefundRateToken returns one token to key's bucket, never exceeding capacity.
// It is what makes the limiter charge only failed attempts: the budget is spent
// before the password is checked, so a flood is rejected cheaply, and a
// legitimate sign-in hands its token back. Refunding a bucket that does not
// exist is a no-op, not an error — the row may already have been swept.
func (s *pgStore) RefundRateToken(ctx context.Context, key string, capacity float64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE nxs_anomaly_rate_buckets
		 SET tokens = LEAST($2::float8, tokens + 1)
		 WHERE bucket_key = $1`,
		key, capacity)
	return err
}

// PruneRateBuckets deletes buckets untouched since cutoff. Callers pass a cutoff
// at least as old as a full refill takes (capacity / rate), because past that
// point a stored bucket and a missing one behave identically.
func (s *pgStore) PruneRateBuckets(ctx context.Context, cutoffISO string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM nxs_anomaly_rate_buckets WHERE updated_at < $1::timestamptz", cutoffISO)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
