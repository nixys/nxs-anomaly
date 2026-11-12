package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func readOnlyErr() error {
	return fmt.Errorf("insert alert: %w", &pgconn.PgError{Code: sqlstateReadOnly, Message: "cannot execute INSERT in a read-only transaction"})
}

func TestIsUnavailableReadOnly(t *testing.T) {
	if !IsUnavailable(readOnlyErr()) {
		t.Fatal("25006 read_only_sql_transaction must be unavailable (503), not a 500 the sender drops")
	}
	if !IsUnavailable(fmt.Errorf("lock: %w", errStandby)) {
		t.Fatal("errStandby must be unavailable")
	}
	if IsUnavailable(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("a unique violation is the caller's problem")
	}
}

func TestFailoverGuardResetsOnReadOnly(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	g := newFailoverGuard()
	g.now = func() time.Time { return now }
	resets := 0
	g.setReset(func() { resets++ })

	// Unrelated errors and success never reset.
	g.TraceQueryEnd(t.Context(), nil, pgx.TraceQueryEndData{})
	g.TraceQueryEnd(t.Context(), nil, pgx.TraceQueryEndData{Err: errors.New("boom")})
	g.TraceQueryEnd(t.Context(), nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505"}})
	if resets != 0 {
		t.Fatalf("resets = %d, want 0", resets)
	}

	// The first read-only error resets; a burst inside the window does not.
	g.TraceQueryEnd(t.Context(), nil, pgx.TraceQueryEndData{Err: readOnlyErr()})
	g.TraceBatchQuery(t.Context(), nil, pgx.TraceBatchQueryData{Err: readOnlyErr()})
	if g.observe(errStandby, "test") {
		t.Fatal("no reset expected inside the debounce window")
	}
	if resets != 1 {
		t.Fatalf("resets = %d, want 1 inside the debounce window", resets)
	}

	// After the window, the next one resets again.
	now = now.Add(poolResetMinInterval)
	if !g.observe(errStandby, "test") {
		t.Fatal("expected a reset after the debounce window")
	}
	if resets != 2 {
		t.Fatalf("resets = %d, want 2", resets)
	}
}

func TestFailoverGuardNilAndUnwired(t *testing.T) {
	var g *failoverGuard
	if g.observe(readOnlyErr(), "nil") {
		t.Fatal("nil guard must be a no-op")
	}
	if newFailoverGuard().observe(readOnlyErr(), "unwired") {
		t.Fatal("guard without a pool must not report a reset")
	}
}
