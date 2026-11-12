package store

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Failover handling.
//
// After a PostgreSQL failover the old primary usually comes back as a standby
// (Patroni and CloudNativePG demote it and rewind it). Connections the pool
// opened to it stay open: a ping still succeeds on a standby, so the pool's
// health check keeps them, and every write on them fails with SQLSTATE 25006
// read_only_sql_transaction until MaxConnLifetime retires them — up to an hour.
//
// Two things follow. A write failing with 25006 is the database being briefly
// unable to serve it, not a bad request, so IsUnavailable reports it and ingest
// answers 503 + Retry-After instead of a 500 the sender drops. And the first
// such error resets the pool, so every connection is reopened through the DSN —
// with a multi-host DSN and target_session_attrs=primary, to the new primary.
//
// Session-level advisory locks are the subtle part: PostgreSQL grants them on a
// hot standby too. A worker whose lock connection stayed on the demoted node
// would "hold" a shard there while another worker takes the same shard on the
// primary, and both would page. TryAdvisoryLock and the LISTEN connection
// therefore refuse a node in recovery.

// errStandby is returned when a dedicated connection landed on a node in
// recovery. It is classified as unavailable: the next attempt, after the pool
// reset, reaches the primary.
var errStandby = errors.New("database node is in recovery (standby); reconnecting to the primary")

// sqlstateReadOnly is read_only_sql_transaction: a write reached a standby.
const sqlstateReadOnly = "25006"

func isReadOnlyError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlstateReadOnly
}

// poolResetMinInterval debounces resets: during a failover every in-flight
// write fails at once, and one reset already recycles them all.
const poolResetMinInterval = 5 * time.Second

// failoverGuard resets the pool when a connection turns out to be on a standby.
// It is the pgx tracer of every pool connection; reset is set once the pool
// exists.
type failoverGuard struct {
	mu        sync.Mutex
	reset     func()
	lastReset time.Time
	now       func() time.Time
}

func newFailoverGuard() *failoverGuard {
	return &failoverGuard{now: time.Now}
}

func (g *failoverGuard) setReset(fn func()) {
	g.mu.Lock()
	g.reset = fn
	g.mu.Unlock()
}

// observe resets the pool on a read-only error, at most once per
// poolResetMinInterval. It returns whether it did.
func (g *failoverGuard) observe(err error, reason string) bool {
	if g == nil || err == nil || (!isReadOnlyError(err) && !errors.Is(err, errStandby)) {
		return false
	}
	g.mu.Lock()
	if g.reset == nil || g.now().Sub(g.lastReset) < poolResetMinInterval {
		g.mu.Unlock()
		return false
	}
	g.lastReset = g.now()
	reset := g.reset
	g.mu.Unlock()
	slog.Warn("db_pool_reset_standby",
		"reason", reason,
		"detail", "a connection reached a node in recovery; reopening all connections")
	reset()
	return true
}

// TraceQueryStart implements pgx.QueryTracer.
func (g *failoverGuard) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer.
func (g *failoverGuard) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	g.observe(data.Err, "read_only_sql_transaction")
}

// TraceBatchStart implements pgx.BatchTracer.
func (g *failoverGuard) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

// TraceBatchQuery implements pgx.BatchTracer.
func (g *failoverGuard) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	g.observe(data.Err, "read_only_sql_transaction")
}

// TraceBatchEnd implements pgx.BatchTracer.
func (g *failoverGuard) TraceBatchEnd(_ context.Context, _ *pgx.Conn, _ pgx.TraceBatchEndData) {}
