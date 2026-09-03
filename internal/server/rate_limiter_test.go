package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// failingStore makes the rate-limit calls fail while leaving the rest of the
// fake store intact, so the limiter's database-unavailable branch can be driven
// without a broken store everywhere else.
type failingStore struct {
	*storetest.Store
}

func (failingStore) ConsumeRateToken(context.Context, string, float64, float64) (bool, error) {
	return false, fmt.Errorf("connection refused")
}

func (failingStore) RefundRateToken(context.Context, string, float64) error {
	return fmt.Errorf("connection refused")
}

var _ store.PostgreSQLStore = failingStore{}

func TestDBRateLimiterExhaustsBurst(t *testing.T) {
	rl := newDBRateLimiter(storetest.New(), "login:", loginRatePerSecond, loginBurst)

	for i := range int(loginBurst) {
		if !rl.allow("10.0.0.1") {
			t.Fatalf("attempt %d denied, the burst is %v", i+1, loginBurst)
		}
	}
	if rl.allow("10.0.0.1") {
		t.Error("attempt past the burst was allowed")
	}
	// A different client has its own bucket.
	if !rl.allow("10.0.0.2") {
		t.Error("a second address was denied on its first attempt")
	}
}

// This is the behaviour the PostgreSQL-backed limiter exists for. Two limiters
// over one store stand in for two API replicas: previously each kept its own map
// and the deployment admitted N times the configured attempts, so spreading
// guesses across pods multiplied an attacker's budget by the replica count.
func TestDBRateLimiterIsSharedAcrossReplicas(t *testing.T) {
	st := storetest.New()
	podA := newDBRateLimiter(st, "login:", loginRatePerSecond, loginBurst)
	podB := newDBRateLimiter(st, "login:", loginRatePerSecond, loginBurst)

	allowed := 0
	for range int(loginBurst) * 2 {
		if podA.allow("10.0.0.1") {
			allowed++
		}
		if podB.allow("10.0.0.1") {
			allowed++
		}
	}

	if allowed != int(loginBurst) {
		t.Errorf("two replicas admitted %d attempts, want %d — the budget is not shared", allowed, int(loginBurst))
	}
}

// The in-memory limiter does not share, and that is deliberate for ingest. Pinned
// so the difference stays a decision rather than an accident.
func TestInMemoryRateLimiterIsPerProcess(t *testing.T) {
	podA := newRateLimiter(loginRatePerSecond, loginBurst)
	podB := newRateLimiter(loginRatePerSecond, loginBurst)

	for range int(loginBurst) {
		podA.allow("10.0.0.1")
	}

	if !podB.allow("10.0.0.1") {
		t.Error("the second process shared the first one's bucket; it is supposed to keep its own")
	}
}

func TestDBRateLimiterRefund(t *testing.T) {
	rl := newDBRateLimiter(storetest.New(), "login:", loginRatePerSecond, loginBurst)

	for range int(loginBurst) {
		rl.allow("10.0.0.1")
	}
	if rl.allow("10.0.0.1") {
		t.Fatal("bucket should be empty")
	}

	rl.refund("10.0.0.1")

	if !rl.allow("10.0.0.1") {
		t.Error("refunded token was not available")
	}
	if rl.allow("10.0.0.1") {
		t.Error("refund returned more than one token")
	}
}

// Refunds must not let a bucket exceed its burst, or a run of successful
// sign-ins would bank credit for a later flood.
func TestDBRateLimiterRefundCapsAtBurst(t *testing.T) {
	rl := newDBRateLimiter(storetest.New(), "login:", loginRatePerSecond, loginBurst)

	rl.allow("10.0.0.1") // create the bucket
	for range 20 {
		rl.refund("10.0.0.1")
	}

	allowed := 0
	for range int(loginBurst) * 3 {
		if rl.allow("10.0.0.1") {
			allowed++
		}
	}
	if allowed > int(loginBurst) {
		t.Errorf("bucket held %d tokens after over-refunding, burst is %v", allowed, loginBurst)
	}
}

// Two limiters with different prefixes must not draw from one budget even when
// they key on the same client address.
func TestDBRateLimiterPrefixIsolatesBudgets(t *testing.T) {
	st := storetest.New()
	login := newDBRateLimiter(st, "login:", loginRatePerSecond, loginBurst)
	other := newDBRateLimiter(st, "other:", loginRatePerSecond, loginBurst)

	for range int(loginBurst) {
		login.allow("10.0.0.1")
	}
	if login.allow("10.0.0.1") {
		t.Fatal("login bucket should be empty")
	}

	if !other.allow("10.0.0.1") {
		t.Error("a limiter with a different prefix was denied from the login bucket")
	}
}

// A database failure admits the request rather than locking everyone out. The
// endpoints behind this limiter need the database to succeed anyway, so failing
// closed would trade a blip for a total sign-in outage and protect nothing.
func TestDBRateLimiterFailsOpen(t *testing.T) {
	rl := newDBRateLimiter(failingStore{storetest.New()}, "login:", loginRatePerSecond, loginBurst)

	for i := range int(loginBurst) * 3 {
		if !rl.allow("10.0.0.1") {
			t.Fatalf("attempt %d denied while the store was failing; the limiter must fail open", i+1)
		}
	}
	// A failing refund must not panic or block either.
	rl.refund("10.0.0.1")
}

func TestPruneRateBucketsRemovesStaleOnly(t *testing.T) {
	st := storetest.New()
	rl := newDBRateLimiter(st, "login:", loginRatePerSecond, loginBurst)
	rl.allow("10.0.0.1")

	// Nothing is old enough yet.
	removed, err := st.PruneRateBuckets(t.Context(), "2000-01-01T00:00:00+00:00")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruned %d buckets with an ancient cutoff, want 0", removed)
	}

	removed, err = st.PruneRateBuckets(t.Context(), "2999-01-01T00:00:00+00:00")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d buckets, want 1", removed)
	}
}
