package limit

import (
	"testing"
	"time"
)

func TestAllowFirstRequest(t *testing.T) {
	tb := New(10, 5)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if !tb.Allow("ip-a", now) {
			t.Fatalf("first %d requests should pass burst, got reject at %d", 5, i)
		}
	}
	if tb.Allow("ip-a", now) {
		t.Fatalf("6th request at same instant should be rejected (burst exhausted)")
	}
}

func TestRefillRestoresTokens(t *testing.T) {
	tb := New(10, 5) // 10 tokens/sec
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = tb.Allow("ip-a", now)
	}
	// Wait 200ms → 2 tokens refilled.
	now = now.Add(200 * time.Millisecond)
	if !tb.Allow("ip-a", now) {
		t.Fatalf("after 200ms (2 tokens refilled), request should pass")
	}
	if !tb.Allow("ip-a", now) {
		t.Fatalf("second post-refill request should also pass")
	}
	if tb.Allow("ip-a", now) {
		t.Fatalf("third post-refill request should be rejected")
	}
}

func TestKeysIndependent(t *testing.T) {
	tb := New(10, 1)
	now := time.Now()
	if !tb.Allow("ip-a", now) {
		t.Fatal("a")
	}
	if tb.Allow("ip-a", now) {
		t.Fatal("a should be exhausted")
	}
	if !tb.Allow("ip-b", now) {
		t.Fatalf("b should be independent of a")
	}
}

func TestCleanupDropsIdle(t *testing.T) {
	tb := New(1, 1)
	now := time.Now()
	tb.Allow("ip-a", now)
	tb.Allow("ip-b", now)
	if tb.Size() != 2 {
		t.Fatalf("size = %d", tb.Size())
	}
	tb.Cleanup(now.Add(time.Hour), 30*time.Minute)
	if tb.Size() != 0 {
		t.Fatalf("post-cleanup size = %d, want 0", tb.Size())
	}
}

func TestBurstClamp(t *testing.T) {
	tb := New(100, 3) // many tokens/sec but burst=3
	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = tb.Allow("a", now)
	}
	// Big idle window — refill would be huge but capped at burst.
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if !tb.Allow("a", now) {
			t.Fatalf("burst-3 should pass first 3 after long idle, failed at %d", i)
		}
	}
	if tb.Allow("a", now) {
		t.Fatalf("4th request should be rejected (burst clamp working)")
	}
}
