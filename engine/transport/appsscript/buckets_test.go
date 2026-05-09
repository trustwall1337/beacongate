package appsscript

import (
	"testing"
	"time"
)

// TestBucketGroupingByAccount verifies that endpoints are grouped by
// account label at pool construction. 4 deployments × 2 accounts
// produces 2 buckets of 2 endpoints each.
func TestBucketGroupingByAccount(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2", "id3", "id4"},
		[]string{"alpha", "alpha", "beta", "beta"},
	)
	if got, want := len(pool.buckets), 2; got != want {
		t.Fatalf("buckets: got %d, want %d", got, want)
	}
	if got, want := len(pool.buckets[0]), 2; got != want {
		t.Errorf("alpha bucket size: got %d, want %d", got, want)
	}
	if got, want := len(pool.buckets[1]), 2; got != want {
		t.Errorf("beta bucket size: got %d, want %d", got, want)
	}
	if got := pool.eps[pool.buckets[0][0]].account; got != "alpha" {
		t.Errorf("first bucket should be alpha, got %q", got)
	}
	if got := pool.eps[pool.buckets[1][0]].account; got != "beta" {
		t.Errorf("second bucket should be beta, got %q", got)
	}
}

// TestBucketSingleAnonymousBucket verifies that bare-string deployments
// (no account label) collapse into a single anonymous bucket.
func TestBucketSingleAnonymousBucket(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2", "id3"},
		[]string{"", "", ""},
	)
	if got, want := len(pool.buckets), 1; got != want {
		t.Fatalf("buckets: got %d, want %d (all unlabeled should collapse)", got, want)
	}
	if got, want := len(pool.buckets[0]), 3; got != want {
		t.Errorf("anonymous bucket size: got %d, want %d", got, want)
	}
}

// TestBucketPickRotatesAccounts verifies that pick rotates BUCKETS
// before re-picking from the same one. 4 deployments × 2 accounts:
// first 2 picks should hit each account once, not the same one twice.
func TestBucketPickRotatesAccounts(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2", "id3", "id4"},
		[]string{"alpha", "alpha", "beta", "beta"},
	)
	now := time.Now()

	first := pool.pick(now)
	second := pool.pick(now)

	if first == -1 || second == -1 {
		t.Fatalf("unexpected -1: first=%d second=%d", first, second)
	}
	firstAcct := pool.eps[first].account
	secondAcct := pool.eps[second].account
	if firstAcct == secondAcct {
		t.Errorf("two consecutive picks landed in same bucket %q (idx=%d,%d) — bucket rotation broken", firstAcct, first, second)
	}
}

// TestBucketPickCyclesWithinBucket verifies that 4 picks across 2
// accounts × 2 deployments hit each deployment exactly once before
// repeating.
func TestBucketPickCyclesWithinBucket(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2", "id3", "id4"},
		[]string{"alpha", "alpha", "beta", "beta"},
	)
	now := time.Now()

	seen := make(map[int]int)
	for i := 0; i < 4; i++ {
		idx := pool.pick(now)
		if idx == -1 {
			t.Fatalf("pick %d returned -1", i)
		}
		seen[idx]++
	}
	for idx := 0; idx < 4; idx++ {
		if seen[idx] != 1 {
			t.Errorf("idx %d picked %d times; want exactly 1 (round-robin should cycle)", idx, seen[idx])
		}
	}
}

// TestBucketFailoverPrefersSameBucket verifies the same-bucket
// failover preference: when an attempt against deployment X in
// bucket A fails, pickFallback returns another endpoint in bucket A
// before crossing into bucket B.
func TestBucketFailoverPrefersSameBucket(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2", "id3", "id4"},
		[]string{"alpha", "alpha", "beta", "beta"},
	)
	now := time.Now()

	// Primary = id1 (bucket alpha, idx 0). Fallback should be id2
	// (bucket alpha, idx 1), NOT id3/id4 in beta.
	fallback := pool.pickFallback(0, now)
	if fallback == -1 {
		t.Fatal("expected fallback, got -1")
	}
	if got := pool.eps[fallback].account; got != "alpha" {
		t.Errorf("same-bucket failover broken: fallback for idx 0 (alpha) is %q (idx %d), want alpha", got, fallback)
	}
}

// TestBucketFailoverCrossesWhenBucketDrained verifies that when the
// primary's bucket has no other live endpoints (all blacklisted),
// pickFallback crosses into another bucket.
func TestBucketFailoverCrossesWhenBucketDrained(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2", "id3"},
		[]string{"alpha", "alpha", "beta"},
	)
	now := time.Now()
	// Blacklist id2 (the only other alpha endpoint).
	pool.recordFailure(1, now, true) // idx 1 = id2 in alpha

	fallback := pool.pickFallback(0, now) // primary = id1 in alpha
	if fallback == -1 {
		t.Fatal("expected cross-bucket fallback, got -1")
	}
	if got := pool.eps[fallback].account; got != "beta" {
		t.Errorf("cross-bucket fallback: got bucket %q (idx %d), want beta", got, fallback)
	}
}

// TestBucketPickSkipsBlacklisted verifies that pick still skips
// blacklisted endpoints inside a bucket.
func TestBucketPickSkipsBlacklisted(t *testing.T) {
	pool := newEndpointPool(
		[]string{"id1", "id2"},
		[]string{"alpha", "alpha"},
	)
	now := time.Now()
	pool.recordFailure(0, now, true) // blacklist idx 0 (long TTL)

	// Next pick should return idx 1, not idx 0.
	idx := pool.pick(now)
	if idx != 1 {
		t.Errorf("expected idx 1 (idx 0 is blacklisted), got %d", idx)
	}
}
