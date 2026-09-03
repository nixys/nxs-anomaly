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
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		conn.Release()
		return false, nil, err
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
