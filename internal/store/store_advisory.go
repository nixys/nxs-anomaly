package store

import "context"

// TryAdvisoryLock attempts a non-blocking session-level advisory lock using
// pg_try_advisory_lock. On success it acquires a dedicated connection from the
// pool (so the lock survives across transactions) and returns an unlock function
// that releases both the lock and the connection. Returns (false, nil, nil) when
// the lock is already held by another session — the caller should skip the
// critical section and retry on the next cycle.
func (s *pgStore) TryAdvisoryLock(ctx context.Context, key int64) (bool, func(), error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, nil, err
	}
	// Advisory locks are granted on a hot standby too: a lock taken on a node
	// demoted by a failover excludes nobody on the primary (see failover.go).
	// The CASE keeps a standby from granting it at all.
	var ok, inRecovery bool
	if err := conn.QueryRow(ctx,
		"SELECT pg_is_in_recovery(), CASE WHEN pg_is_in_recovery() THEN false ELSE pg_try_advisory_lock($1) END",
		key).Scan(&inRecovery, &ok); err != nil {
		conn.Release()
		return false, nil, err
	}
	if inRecovery {
		conn.Release()
		s.failover.observe(errStandby, "advisory_lock_on_standby")
		return false, nil, errStandby
	}
	if !ok {
		conn.Release()
		return false, nil, nil
	}
	unlock := func() {
		// Use Background so unlock succeeds even if the original ctx was cancelled.
		conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key) //nolint:errcheck
		conn.Release()
	}
	return true, unlock, nil
}
