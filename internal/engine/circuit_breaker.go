package engine

import (
	"sync"
	"time"
)

// circuitBreaker is a per-key (channel+target) breaker that short-circuits
// delivery attempts to a provider that is failing, so a dead endpoint does not
// burn worker-cycle latency and DB connections on doomed HTTP calls every tick.
//
// State per key is implicit:
//   - closed  — no entry, or openUntil zero: calls allowed, failures counted.
//   - open    — openUntil in the future: calls blocked.
//   - half-open — cooldown elapsed: Allow lets a single trial through and re-arms
//     the cooldown so concurrent callers stay blocked until the trial's outcome is
//     recorded; the trial's Record either closes (success) or re-opens (failure).
//
// A nil *circuitBreaker is the disabled state: Allow always returns true and
// Record is a no-op, so callers need no nil checks beyond construction.
type circuitBreaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	states    map[string]*breakerEntry
}

type breakerEntry struct {
	failures  int
	openUntil time.Time
}

// newCircuitBreaker returns a breaker, or nil (disabled) when threshold <= 0.
func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	if threshold <= 0 {
		return nil
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &circuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		states:    map[string]*breakerEntry{},
	}
}

// Allow reports whether a call for key may proceed at time now.
func (c *circuitBreaker) Allow(key string, now time.Time) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.states[key]
	if e == nil || e.openUntil.IsZero() {
		return true
	}
	if now.Before(e.openUntil) {
		return false
	}
	// Cooldown elapsed: allow one trial, re-arm so others wait for its result.
	e.openUntil = now.Add(c.cooldown)
	return true
}

// Record updates the breaker for key with the outcome of a call at time now.
func (c *circuitBreaker) Record(key string, success bool, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if success {
		delete(c.states, key)
		return
	}
	e := c.states[key]
	if e == nil {
		e = &breakerEntry{}
		c.states[key] = e
	}
	e.failures++
	if e.failures >= c.threshold {
		e.openUntil = now.Add(c.cooldown)
	}
}
