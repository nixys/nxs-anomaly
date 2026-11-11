package store

import (
	"context"
	"time"
)

// wakeChannel is the pg_notify channel used to wake worker loops as soon as
// ingest schedules new work, instead of waiting out the poll interval.
const wakeChannel = "nxs_anomaly_wake"

// NotifyWake signals listening worker loops that new deliverable work exists.
// Best-effort: callers treat an error as "the worker will pick it up on the
// next poll tick".
func (s *pgStore) NotifyWake(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "SELECT pg_notify($1, '')", wakeChannel)
	return err
}

// WaitForWake blocks until a wake notification arrives or timeout elapses,
// returning true when woken early by a notification. A dedicated pooled
// connection holds the LISTEN registration across calls. Errors (lost
// connection, closed pool, cancelled context) degrade to plain timeout
// behaviour so worker loops fall back to interval polling.
func (s *pgStore) WaitForWake(ctx context.Context, timeout time.Duration) bool {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()

	if s.listenConn == nil {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			sleepCtx(ctx, timeout)
			return false
		}
		// A standby never receives the primary's NOTIFY: a listener there
		// would leave the worker on interval polling for the life of the
		// process. Refuse it and reset the pool (see failover.go).
		var inRecovery bool
		if err := conn.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil || inRecovery {
			conn.Release()
			if inRecovery {
				s.failover.observe(errStandby, "listener_on_standby")
			}
			sleepCtx(ctx, timeout)
			return false
		}
		if _, err := conn.Exec(ctx, "LISTEN "+wakeChannel); err != nil {
			conn.Release()
			sleepCtx(ctx, timeout)
			return false
		}
		s.listenConn = conn
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := s.listenConn.Conn().WaitForNotification(waitCtx); err == nil {
		s.drainWakesLocked(ctx)
		return true
	}
	if waitCtx.Err() != nil && ctx.Err() == nil {
		// Plain timeout — the listener connection stays registered.
		return false
	}
	// Connection problem or caller shutdown: close the connection so the pool
	// discards it (it has LISTEN state) and re-establish on the next call.
	s.dropListenerLocked()
	return false
}

// wakeDrainWindow is how long drainWakesLocked waits for one more queued wake,
// and wakeDrainBudget bounds the whole drain so a steady stream of ingests
// cannot keep the worker from starting its cycle.
const (
	wakeDrainWindow = 5 * time.Millisecond
	wakeDrainBudget = 100 * time.Millisecond
)

// drainWakesLocked consumes the wakes that queued up behind the one that woke
// us. Every ingest sends its own NOTIFY and PostgreSQL does not merge them
// across transactions, so without this a storm of N alerts leaves N wakes in
// the connection buffer and the worker runs N full cycles, one per wake, long
// after the storm is over. One cycle handles all the work they announce.
// A timeout only sets a read deadline (pgconn's default context handler), so
// the listener connection stays usable. Caller must hold listenMu.
func (s *pgStore) drainWakesLocked(ctx context.Context) {
	deadline := time.Now().Add(wakeDrainBudget)
	for time.Now().Before(deadline) {
		drainCtx, cancel := context.WithTimeout(ctx, wakeDrainWindow)
		_, err := s.listenConn.Conn().WaitForNotification(drainCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil && drainCtx.Err() == nil {
				// Not a timeout: the connection is broken; reconnect next call.
				s.dropListenerLocked()
			}
			return
		}
	}
}

// dropListenerLocked closes and releases the listener connection.
// Caller must hold listenMu.
func (s *pgStore) dropListenerLocked() {
	if s.listenConn == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = s.listenConn.Conn().Close(closeCtx)
	cancel()
	s.listenConn.Release()
	s.listenConn = nil
}

// sleepCtx sleeps for d or until ctx is done, preventing a busy loop when the
// listener connection cannot be established (e.g. database outage).
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
