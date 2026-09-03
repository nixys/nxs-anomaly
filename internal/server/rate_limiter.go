package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
)

const bucketTTL = 5 * time.Minute
const evictEvery = 1000

// limiter is a token bucket keyed by string. Two implementations back it, and
// which one a limiter gets is a deliberate choice per limiter, not a config
// switch:
//
//   - rateLimiter keeps buckets in this process. The limit it enforces is
//     therefore per pod: with N replicas the deployment as a whole admits N
//     times the configured rate. That is the right trade for ingest, which is
//     the hot path and where the limiter exists to protect this process from
//     being swamped, not to enforce a cluster-wide quota.
//
//   - dbRateLimiter keeps buckets in PostgreSQL, so the limit is global however
//     many replicas run. That is the right trade for sign-in attempts, where a
//     per-pod limit means an attacker spreading guesses across pods gets N times
//     the attempts — the limit that most needs to be global was the one that
//     scaled with the deployment.
//
// See docs/SECURITY_PROFILE.md for the operator-facing version of this.
type limiter interface {
	allow(key string) bool
	refund(key string)
}

// rateLimiter is a token-bucket rate limiter keyed by string (IP or integration key).
type rateLimiter struct {
	mu        sync.Mutex
	rate      float64 // tokens per second
	capacity  float64
	buckets   map[string]*bucket
	callCount int
}

type bucket struct {
	tokens     float64
	last       time.Time
	lastAccess time.Time
}

func newRateLimiter(rate, capacity float64) *rateLimiter {
	return &rateLimiter{
		rate:     rate,
		capacity: capacity,
		buckets:  make(map[string]*bucket),
	}
}

// refund returns a token taken by allow. It exists for attempt limiters where
// only a *failed* attempt should count: the check has to happen before the
// expensive work (so a flood of wrong passwords is rejected cheaply), but a
// legitimate success must not spend anybody's budget. Never exceeds capacity.
func (rl *rateLimiter) refund(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok {
		return
	}
	b.tokens = min(rl.capacity, b.tokens+1.0)
}

func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.capacity, last: now, lastAccess: now}
		rl.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.lastAccess = now
	b.tokens = min(rl.capacity, b.tokens+elapsed*rl.rate)
	allowed := b.tokens >= 1.0
	if allowed {
		b.tokens -= 1.0
	}
	rl.callCount++
	if rl.callCount%evictEvery == 0 {
		for k, bkt := range rl.buckets {
			if now.Sub(bkt.lastAccess) > bucketTTL {
				delete(rl.buckets, k)
			}
		}
	}
	return allowed
}

var _ limiter = (*rateLimiter)(nil)
var _ limiter = (*dbRateLimiter)(nil)

// dbRateLimiter is a token bucket held in PostgreSQL, so every replica draws
// from the same budget. Used for sign-in attempts; see limiter above.
type dbRateLimiter struct {
	store    store.PostgreSQLStore
	rate     float64 // tokens per second
	capacity float64
	// prefix namespaces this limiter's keys inside the shared bucket table, so a
	// second limiter added later cannot silently share a budget with this one
	// just because both key on a client IP.
	prefix string
	// timeout bounds the limiter's own database call. A limiter that blocks for
	// as long as the request context allows would turn a slow database into a
	// slow login rather than a fast failure.
	timeout time.Duration
}

func newDBRateLimiter(s store.PostgreSQLStore, prefix string, rate, capacity float64) *dbRateLimiter {
	return &dbRateLimiter{store: s, prefix: prefix, rate: rate, capacity: capacity, timeout: 2 * time.Second}
}

// allow takes a token, or reports that there was none.
//
// A database error admits the request. That reads wrong for a security control,
// so it is worth being explicit: the endpoints behind this limiter cannot
// succeed without the database either — verifyCredentials reads the password
// hash from it. Failing closed would convert a database blip into a total
// sign-in outage while protecting nothing an attacker could otherwise reach.
func (rl *dbRateLimiter) allow(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), rl.timeout)
	defer cancel()
	allowed, err := rl.store.ConsumeRateToken(ctx, rl.prefix+key, rl.rate, rl.capacity)
	if err != nil {
		slog.Error("rate_limiter_unavailable",
			"err", err,
			"effect", "sign-in attempt admitted without a rate-limit check")
		return true
	}
	return allowed
}

// refund returns a token spent by allow. A failure here is not worth failing the
// request over: the caller has already been authenticated, and the cost is one
// token that refills on its own.
func (rl *dbRateLimiter) refund(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), rl.timeout)
	defer cancel()
	if err := rl.store.RefundRateToken(ctx, rl.prefix+key, rl.capacity); err != nil {
		slog.Warn("rate_limiter_refund_failed", "err", err)
	}
}
