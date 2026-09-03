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
