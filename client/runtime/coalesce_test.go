package runtime

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestCoalesceWindowZeroFiresImmediately confirms coalesce_step_ms=0
// preserves the prior behavior — every signalFlush goes through
// without delay.
func TestCoalesceWindowZeroFiresImmediately(t *testing.T) {
	p := &Pump{flush: make(chan struct{}, 1)}
	// Default coalesceWindow is zero.
	start := time.Now()
	p.scheduleFlush()
	// flush channel should be filled immediately.
	select {
	case <-p.flush:
		// good
	case <-time.After(50 * time.Millisecond):
		t.Fatal("scheduleFlush with window=0 didn't fire immediately")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("scheduleFlush with window=0 took %v; expected immediate", elapsed)
	}
}

// TestCoalesceWindowDelaysFlush confirms a non-zero window defers the
// flush by approximately that window.
func TestCoalesceWindowDelaysFlush(t *testing.T) {
	p := &Pump{flush: make(chan struct{}, 1)}
	p.SetCoalesceWindow(40 * time.Millisecond)

	start := time.Now()
	p.scheduleFlush()

	select {
	case <-p.flush:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduleFlush with window=40ms never fired")
	}
	elapsed := time.Since(start)
	if elapsed < 30*time.Millisecond {
		t.Errorf("flush fired too early (%v); expected ~40ms", elapsed)
	}
	// Allow scheduling-jitter slack on overloaded test runners.
	if elapsed > 150*time.Millisecond {
		t.Errorf("flush fired too late (%v); expected ~40ms", elapsed)
	}
}

// TestCoalesceMultipleEnqueuesProduceOneFlush is the core quota-economy
// proof: 5 enqueues within a 40ms window produce ONE flush, not 5.
// In the real system this means 1 HTTP POST instead of 5 — the entire
// reason for `coalesce_step_ms`.
func TestCoalesceMultipleEnqueuesProduceOneFlush(t *testing.T) {
	p := &Pump{flush: make(chan struct{}, 4)} // capacity 4 so we can detect spurious extra flushes
	p.SetCoalesceWindow(50 * time.Millisecond)

	// Fire 5 schedules, 5ms apart — each should reset the timer.
	for i := 0; i < 5; i++ {
		p.scheduleFlush()
		time.Sleep(5 * time.Millisecond)
	}

	// The flush should NOT have fired yet (timer keeps resetting).
	select {
	case <-p.flush:
		t.Fatal("flush fired during the coalesce burst — timer not resetting")
	case <-time.After(0):
		// good
	}

	// Now wait for the window to elapse without resetting.
	select {
	case <-p.flush:
		// good
	case <-time.After(150 * time.Millisecond):
		t.Fatal("flush never fired after the coalesce burst")
	}

	// Drain — there should be no second flush.
	select {
	case <-p.flush:
		t.Fatal("got a second flush — coalescing should produce exactly one")
	case <-time.After(50 * time.Millisecond):
		// good
	}
}

// TestCoalesceSafetyCap confirms that a steady stream of enqueues
// faster than the window can't defer the flush forever — the safety
// cap (5×window) forces a flush.
func TestCoalesceSafetyCap(t *testing.T) {
	p := &Pump{flush: make(chan struct{}, 1)}
	window := 30 * time.Millisecond
	p.SetCoalesceWindow(window)

	stop := make(chan struct{})
	defer close(stop)

	var resets atomic.Int64
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.scheduleFlush()
			resets.Add(1)
			// Reset every 10ms — well inside the 30ms window.
			time.Sleep(10 * time.Millisecond)
		}
	}()

	start := time.Now()
	select {
	case <-p.flush:
		// safety cap should fire within ~5×window = 150ms
		elapsed := time.Since(start)
		// Allow generous jitter for slow CI; the point is "doesn't run
		// forever," not exact timing.
		if elapsed > 500*time.Millisecond {
			t.Errorf("safety cap did not fire within 500ms (took %v)", elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("safety cap never fired despite continuous resets")
	}
}
