package engine

import (
	"testing"
	"time"
)

func TestCircuitBreakerNilDisabled(t *testing.T) {
	var cb *circuitBreaker // disabled
	now := time.Now()
	if !cb.Allow("k", now) {
		t.Fatal("nil breaker must always allow")
	}
	cb.Record("k", false, now) // must not panic
}

func TestCircuitBreakerThresholdZeroDisabled(t *testing.T) {
	if newCircuitBreaker(0, time.Second) != nil {
		t.Fatal("threshold 0 must return nil (disabled)")
	}
}

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	cb := newCircuitBreaker(3, 30*time.Second)
	now := time.Now()
	key := "webhook:https://x"

	for i := 0; i < 2; i++ {
		if !cb.Allow(key, now) {
			t.Fatalf("call %d should be allowed before threshold", i)
		}
		cb.Record(key, false, now)
	}
	// Third failure trips the breaker.
	if !cb.Allow(key, now) {
		t.Fatal("third call should still be allowed (breaker not yet open)")
	}
	cb.Record(key, false, now)

	if cb.Allow(key, now) {
		t.Fatal("breaker should be open after reaching threshold")
	}
	// A different key is unaffected.
	if !cb.Allow("webhook:https://y", now) {
		t.Fatal("unrelated key must not be affected")
	}
}

func TestCircuitBreakerHalfOpenTrialAndClose(t *testing.T) {
	cb := newCircuitBreaker(1, 10*time.Second)
	now := time.Now()
	key := "telegram:123"

	cb.Record(key, false, now) // threshold 1 → opens immediately
	if cb.Allow(key, now) {
		t.Fatal("breaker should be open")
	}

	// After cooldown, a single trial is allowed.
	later := now.Add(11 * time.Second)
	if !cb.Allow(key, later) {
		t.Fatal("half-open trial should be allowed after cooldown")
	}
	// Concurrent caller during the trial window stays blocked (re-armed).
	if cb.Allow(key, later) {
		t.Fatal("second concurrent call during trial must be blocked")
	}
	// Trial succeeds → breaker closes.
	cb.Record(key, true, later)
	if !cb.Allow(key, later) {
		t.Fatal("breaker should be closed after a successful trial")
	}
}

func TestCircuitBreakerHalfOpenTrialFailureReopens(t *testing.T) {
	cb := newCircuitBreaker(1, 10*time.Second)
	now := time.Now()
	key := "email:a@b"

	cb.Record(key, false, now)
	later := now.Add(11 * time.Second)
	if !cb.Allow(key, later) {
		t.Fatal("trial should be allowed")
	}
	cb.Record(key, false, later) // trial fails → reopen
	if cb.Allow(key, later) {
		t.Fatal("breaker should reopen after a failed trial")
	}
}

func TestCircuitBreakerSuccessResets(t *testing.T) {
	cb := newCircuitBreaker(3, time.Second)
	now := time.Now()
	key := "webhook:z"
	cb.Record(key, false, now)
	cb.Record(key, false, now)
	cb.Record(key, true, now) // success clears the failure streak
	cb.Record(key, false, now)
	cb.Record(key, false, now)
	if !cb.Allow(key, now) {
		t.Fatal("breaker should remain closed: streak was reset by the success")
	}
}
