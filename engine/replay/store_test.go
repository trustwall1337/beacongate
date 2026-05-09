package replay

import (
	"bytes"
	"testing"
	"time"
)

func mkID(b byte) [16]byte {
	var id [16]byte
	for i := range id {
		id[i] = b
	}
	return id
}

func TestAcceptThenDuplicateProcessedWithinResponseTTL(t *testing.T) {
	s := New(Defaults())
	id := mkID(0x01)
	now := time.Now()

	// First arrival: Accept.
	d, body := s.Check("client-a", id, now, now)
	if d != Accept {
		t.Fatalf("first Check: got %s want ACCEPT", d)
	}
	if body != nil {
		t.Fatalf("first Check returned body %q, expected nil", body)
	}

	// Server records the response after processing.
	resp := []byte("processed-response-bytes")
	s.RecordResponse("client-a", id, resp, now)

	// Retry within the response-cache window: DuplicateProcessed,
	// returns the cached bytes.
	d2, body2 := s.Check("client-a", id, now, now.Add(30*time.Second))
	if d2 != DuplicateProcessed {
		t.Fatalf("retry: got %s want DUPLICATE_PROCESSED", d2)
	}
	if !bytes.Equal(body2, resp) {
		t.Fatalf("retry: body mismatch")
	}
}

func TestRetryAfterResponseTTLReturnsReplayed(t *testing.T) {
	s := New(Defaults())
	id := mkID(0x02)
	now := time.Now()

	d, _ := s.Check("client-a", id, now, now)
	if d != Accept {
		t.Fatalf("first Check: %s", d)
	}
	s.RecordResponse("client-a", id, []byte("resp"), now)

	// 2 minutes later: response cache expired but dedup ring still
	// remembers the replay-id → REPLAYED.
	d2, body := s.Check("client-a", id, now, now.Add(2*time.Minute))
	if d2 != Replayed {
		t.Fatalf("post-TTL retry: got %s want REPLAYED", d2)
	}
	if body != nil {
		t.Fatalf("REPLAYED should not return cached body, got %q", body)
	}
}

func TestStaleTimestampRejectedRegardlessOfReplayCache(t *testing.T) {
	s := New(Defaults())
	now := time.Now()
	// Timestamp 1 hour in the past.
	d, _ := s.Check("client-a", mkID(0x03), now.Add(-time.Hour), now)
	if d != StaleTimestamp {
		t.Fatalf("got %s want STALE_TIMESTAMP", d)
	}
	// Timestamp 1 hour in the future.
	d, _ = s.Check("client-a", mkID(0x04), now.Add(time.Hour), now)
	if d != StaleTimestamp {
		t.Fatalf("future ts got %s want STALE_TIMESTAMP", d)
	}
}

func TestCrossClientReplayIDsAreIndependent(t *testing.T) {
	s := New(Defaults())
	id := mkID(0x05)
	now := time.Now()

	d, _ := s.Check("client-alice", id, now, now)
	if d != Accept {
		t.Fatalf("alice first: %s", d)
	}
	// Bob using the same replay-id should also be accepted (different
	// client_id namespace).
	d, _ = s.Check("client-bob", id, now, now)
	if d != Accept {
		t.Fatalf("bob with same id should be ACCEPT (different namespace), got %s", d)
	}
}

func TestRatePressureRejectsBeforeOldEntryEvicted(t *testing.T) {
	cfg := Defaults()
	cfg.DedupCapPerClient = 4
	cfg.DedupTTL = time.Hour
	s := New(cfg)
	now := time.Now()

	// Fill the ring.
	for i := 0; i < 4; i++ {
		var id [16]byte
		id[0] = byte(i + 1)
		if d, _ := s.Check("client", id, now, now); d != Accept {
			t.Fatalf("fill %d: %s", i, d)
		}
	}
	// One more, still within TTL → RATE_PRESSURE (no eviction).
	d, _ := s.Check("client", mkID(0xFF), now, now.Add(time.Second))
	if d != RatePressure {
		t.Fatalf("expected RATE_PRESSURE on full ring within TTL, got %s", d)
	}
}

func TestRingEvictsAfterTTL(t *testing.T) {
	cfg := Defaults()
	cfg.DedupCapPerClient = 2
	cfg.DedupTTL = 100 * time.Millisecond
	cfg.ResponseTTL = 50 * time.Millisecond
	s := New(cfg)
	t0 := time.Now()

	d, _ := s.Check("c", mkID(1), t0, t0)
	if d != Accept {
		t.Fatalf("a: %s", d)
	}
	d, _ = s.Check("c", mkID(2), t0, t0)
	if d != Accept {
		t.Fatalf("b: %s", d)
	}
	// Both ring entries TTL'd.
	t1 := t0.Add(200 * time.Millisecond)
	d, _ = s.Check("c", mkID(3), t1, t1)
	if d != Accept {
		t.Fatalf("post-TTL eviction failed: %s", d)
	}
}

func TestResponseCacheByteBudgetEvictsLRU(t *testing.T) {
	cfg := Defaults()
	cfg.ResponseBudgetBytesPerClient = 1024 // tiny budget
	s := New(cfg)
	now := time.Now()
	big := make([]byte, 700)

	for i := 0; i < 3; i++ {
		s.Check("c", mkID(byte(i+1)), now, now)
		s.RecordResponse("c", mkID(byte(i+1)), big, now)
	}

	// First entry should have been evicted from the response cache
	// (3 * 700 > 1024); a Check with id #1 should fall through to
	// ring lookup → REPLAYED (the dedup entry survives even after
	// response-cache byte-budget eviction).
	d, body := s.Check("c", mkID(1), now, now.Add(10*time.Second))
	if d != Replayed {
		t.Fatalf("expected REPLAYED after byte-budget eviction, got %s body=%q", d, body)
	}
	// Most recent entry still in response cache → DuplicateProcessed.
	d, body = s.Check("c", mkID(3), now, now.Add(10*time.Second))
	if d != DuplicateProcessed {
		t.Fatalf("expected DUPLICATE_PROCESSED for fresh entry, got %s", d)
	}
	if len(body) != 700 {
		t.Fatalf("response body length mismatch: %d", len(body))
	}
}

func TestForgetDropsClientState(t *testing.T) {
	s := New(Defaults())
	now := time.Now()
	id := mkID(7)
	s.Check("c", id, now, now)
	s.RecordResponse("c", id, []byte("x"), now)
	s.Forget("c")
	// Same replay-id arriving again should be Accept (client state
	// fully dropped).
	d, _ := s.Check("c", id, now, now.Add(time.Second))
	if d != Accept {
		t.Fatalf("post-Forget got %s, expected ACCEPT", d)
	}
}

func TestCheckIsConcurrentSafe(t *testing.T) {
	s := New(Defaults())
	now := time.Now()
	const workers = 8
	const each = 50
	done := make(chan struct{}, workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			for i := 0; i < each; i++ {
				var id [16]byte
				id[0] = byte(w)
				id[1] = byte(i)
				s.Check("c", id, now, now)
				s.RecordResponse("c", id, []byte("x"), now)
			}
			done <- struct{}{}
		}(w)
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	// Just confirm no panic; race detector catches concurrent map
	// races when this is run with -race.
}

func TestDecisionString(t *testing.T) {
	cases := []struct {
		d    Decision
		want string
	}{
		{Accept, "ACCEPT"},
		{DuplicateProcessed, "DUPLICATE_PROCESSED"},
		{Replayed, "REPLAYED"},
		{StaleTimestamp, "STALE_TIMESTAMP"},
		{RatePressure, "RATE_PRESSURE"},
		{Decision(99), "UNKNOWN"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Fatalf("Decision(%d).String() = %q want %q", c.d, got, c.want)
		}
	}
}
