package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// The token bucket is one CTE doing refill, take and write in a single
// statement. Nothing about that is verified by the in-memory fake in
// internal/storetest, which models the intended behaviour rather than the SQL —
// so these run against a real database or skip.

func newRateLimitTestStore(t *testing.T) *pgStore {
	t.Helper()
	dsn := os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NXS_ANOMALY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	t.Setenv("NXS_ANOMALY_DB_DSN", dsn)
	st, err := NewPostgreSQLStore(ctx)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(st.Close)
	return st.(*pgStore)
}

func TestConsumeRateTokenExhaustsAndRefills(t *testing.T) {
	s := newRateLimitTestStore(t)
	ctx := context.Background()
	key := "test:exhaust:" + time.Now().Format(time.RFC3339Nano)

	// One token per second. The rate has to be slow relative to a round trip or
	// the bucket refills as fast as the loop below drains it — at 20/s the three
	// takes were silently topped up between calls and the fourth was allowed.
	// 500ms (2/s) turned out not to be enough margin either: on a loaded CI
	// runner four transactions (three consumes plus the burst check) can
	// themselves take a few hundred ms, eating into the refill window and
	// flaking the same way. 1s buys more headroom without the test taking long.
	const rate, capacity = 1.0, 3.0

	for i := range 3 {
		ok, err := s.ConsumeRateToken(ctx, key, rate, capacity)
		if err != nil {
			t.Fatalf("consume %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("attempt %d denied within the burst of %v", i+1, capacity)
		}
	}
	ok, err := s.ConsumeRateToken(ctx, key, rate, capacity)
	if err != nil {
		t.Fatalf("consume past burst: %v", err)
	}
	if ok {
		t.Error("attempt past the burst was allowed")
	}

	time.Sleep(1200 * time.Millisecond)

	ok, err = s.ConsumeRateToken(ctx, key, rate, capacity)
	if err != nil {
		t.Fatalf("consume after refill: %v", err)
	}
	if !ok {
		t.Error("bucket did not refill over time")
	}
}

// The refill must not push a bucket past its capacity, however long it idles —
// otherwise a quiet night would bank an unlimited burst for the morning.
func TestConsumeRateTokenCapsAtCapacity(t *testing.T) {
	s := newRateLimitTestStore(t)
	ctx := context.Background()
	key := "test:cap:" + time.Now().Format(time.RFC3339Nano)

	const rate, capacity = 5.0, 2.0

	if _, err := s.ConsumeRateToken(ctx, key, rate, capacity); err != nil {
		t.Fatalf("prime bucket: %v", err)
	}
	// Idle for a second: an uncapped refill would have banked five tokens on top
	// of the one already there.
	time.Sleep(time.Second)

	// Drain at rate 0, so what the loop counts is what the sleep accumulated and
	// not what the loop's own round trips earned back.
	allowed := 0
	for range 10 {
		ok, err := s.ConsumeRateToken(ctx, key, 0, capacity)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed > int(capacity) {
		t.Errorf("bucket handed out %d tokens, capacity is %v — refill is not capped", allowed, capacity)
	}
}

// The reason this table exists at all: concurrent callers must not both spend
// the same token. This is what a per-process map could not do across replicas,
// and what a read-then-write pair of statements would get wrong under load.
func TestConsumeRateTokenIsAtomicUnderConcurrency(t *testing.T) {
	s := newRateLimitTestStore(t)
	ctx := context.Background()
	key := "test:race:" + time.Now().Format(time.RFC3339Nano)

	// Rate 0: no refill, so the accounting is exact — exactly `capacity` of the
	// concurrent attempts may succeed, no matter how they interleave.
	const rate, capacity = 0.0, 5.0
	const goroutines = 40

	var wg sync.WaitGroup
	results := make([]bool, goroutines)
	errs := make([]error, goroutines)
	start := make(chan struct{})

	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.ConsumeRateToken(ctx, key, rate, capacity)
		}()
	}
	close(start)
	wg.Wait()

	allowed := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if results[i] {
			allowed++
		}
	}

	if allowed != int(capacity) {
		t.Errorf("%d of %d concurrent attempts were allowed, want exactly %v",
			allowed, goroutines, capacity)
	}
}

func TestRefundRateToken(t *testing.T) {
	s := newRateLimitTestStore(t)
	ctx := context.Background()
	key := "test:refund:" + time.Now().Format(time.RFC3339Nano)

	const rate, capacity = 0.0, 2.0

	for range 2 {
		if _, err := s.ConsumeRateToken(ctx, key, rate, capacity); err != nil {
			t.Fatalf("consume: %v", err)
		}
	}
	ok, err := s.ConsumeRateToken(ctx, key, rate, capacity)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if ok {
		t.Fatal("bucket should be empty")
	}

	if err := s.RefundRateToken(ctx, key, capacity); err != nil {
		t.Fatalf("refund: %v", err)
	}

	ok, err = s.ConsumeRateToken(ctx, key, rate, capacity)
	if err != nil {
		t.Fatalf("consume after refund: %v", err)
	}
	if !ok {
		t.Error("refunded token was not available")
	}

	// Refunding a key that was never used must not create a bucket or error.
	if err := s.RefundRateToken(ctx, "test:refund:absent", capacity); err != nil {
		t.Errorf("refund of an absent bucket: %v", err)
	}
}

func TestRefundRateTokenCapsAtCapacity(t *testing.T) {
	s := newRateLimitTestStore(t)
	ctx := context.Background()
	key := "test:refundcap:" + time.Now().Format(time.RFC3339Nano)

	const rate, capacity = 0.0, 2.0

	if _, err := s.ConsumeRateToken(ctx, key, rate, capacity); err != nil {
		t.Fatalf("prime bucket: %v", err)
	}
	for range 20 {
		if err := s.RefundRateToken(ctx, key, capacity); err != nil {
			t.Fatalf("refund: %v", err)
		}
	}

	allowed := 0
	for range 10 {
		ok, err := s.ConsumeRateToken(ctx, key, rate, capacity)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed > int(capacity) {
		t.Errorf("over-refunding left %d tokens, capacity is %v", allowed, capacity)
	}
}

func TestPruneRateBuckets(t *testing.T) {
	s := newRateLimitTestStore(t)
	ctx := context.Background()
	key := "test:prune:" + time.Now().Format(time.RFC3339Nano)

	if _, err := s.ConsumeRateToken(ctx, key, 1, 5); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// A cutoff before the bucket was touched must leave it alone.
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := s.PruneRateBuckets(ctx, past); err != nil {
		t.Fatalf("prune with past cutoff: %v", err)
	}
	ok, err := s.ConsumeRateToken(ctx, key, 0, 5)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if !ok {
		t.Fatal("bucket unexpectedly empty before pruning")
	}

	// A cutoff in the future sweeps it, and the next call starts from a full
	// bucket — a swept bucket and an absent one are the same thing.
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	removed, err := s.PruneRateBuckets(ctx, future)
	if err != nil {
		t.Fatalf("prune with future cutoff: %v", err)
	}
	if removed < 1 {
		t.Errorf("pruned %d rows, want at least the one just created", removed)
	}
}
