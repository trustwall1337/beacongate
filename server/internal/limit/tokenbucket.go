// Package limit provides per-key token-bucket rate limiting for the
// BeaconGate server's HTTP endpoints. Plan D1 calls for a per-IP cap
// on the /tunnel endpoint to bound the cost a single peer with the
// shared key can impose on the server even when its requests are
// otherwise valid (defense-in-depth alongside the per-client session
// cap and the replay cache's RATE_PRESSURE rejection).
//
// Workstream A9 invariant relevance: hot path. A bucket lookup is
// O(1) under a per-key mutex; the global map is only locked briefly
// to find/create the per-key bucket.
package limit

import (
	"sync"
	"time"
)

// TokenBucket is a per-key rate limiter. Refills tokens at `rate`
// per second up to `burst`. Each Allow call consumes one token; if
// none are available, returns false.
//
// Tracking is keyed by string (operator passes the IP). Idle keys
// stay in memory until Cleanup is called periodically; fleets with
// huge IP churn should run Cleanup on a 5-minute ticker.
type TokenBucket struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// New returns a TokenBucket. rate is tokens per second; burst is the
// maximum tokens carriable.
func New(rate float64, burst int) *TokenBucket {
	if rate < 0 {
		rate = 0
	}
	if burst < 1 {
		burst = 1
	}
	return &TokenBucket{
		rate:    rate,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
	}
}

// Allow consumes one token from the bucket for key. Returns true if
// allowed, false if the bucket is empty.
func (t *TokenBucket) Allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.buckets[key]
	if !ok {
		// New key: start with a full bucket so a first request is
		// always allowed.
		t.buckets[key] = &bucket{tokens: t.burst - 1, lastRefill: now}
		return true
	}
	// Refill since last access.
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * t.rate
		if b.tokens > t.burst {
			b.tokens = t.burst
		}
		b.lastRefill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Cleanup drops bucket entries that have been idle for longer than
// idleTTL. Call periodically to bound memory under IP churn.
func (t *TokenBucket) Cleanup(now time.Time, idleTTL time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, b := range t.buckets {
		if now.Sub(b.lastRefill) > idleTTL {
			delete(t.buckets, k)
		}
	}
}

// Size returns the number of tracked keys (for stats).
func (t *TokenBucket) Size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buckets)
}
