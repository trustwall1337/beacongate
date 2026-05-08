package admin

import (
	"sync"
	"time"
)

// failureLimiter throttles auth-failure attempts per remote IP. After
// `threshold` failures within `window`, further attempts from that IP get
// 429 instead of even hitting the auth check. Successful auth resets the
// counter for that IP.
//
// This is intentionally simple and in-memory — sized for hundreds of
// admin clients, not millions. A single-process server only.
type failureLimiter struct {
	threshold int
	window    time.Duration

	mu      sync.Mutex
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	failures    int
	firstFail   time.Time
	lockedUntil time.Time
}

func newFailureLimiter(threshold int, window time.Duration) *failureLimiter {
	return &failureLimiter{
		threshold: threshold,
		window:    window,
		entries:   map[string]*limiterEntry{},
	}
}

// allowed reports whether a request from ip should reach the auth check.
// When a lockout is in effect, returns false plus the duration left.
func (l *failureLimiter) allowed(ip string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if e == nil {
		return true, 0
	}
	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}
	if !e.firstFail.IsZero() && now.Sub(e.firstFail) > l.window {
		// Window expired; clear counter.
		delete(l.entries, ip)
	}
	return true, 0
}

// recordFailure increments the counter for ip and locks it for `window` if
// the threshold is reached.
func (l *failureLimiter) recordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if e == nil {
		e = &limiterEntry{firstFail: now}
		l.entries[ip] = e
	}
	if now.Sub(e.firstFail) > l.window {
		// New window — reset counter.
		e.firstFail = now
		e.failures = 0
	}
	e.failures++
	if e.failures >= l.threshold {
		e.lockedUntil = now.Add(l.window)
	}
}

// recordSuccess clears any failure count for ip.
func (l *failureLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}
