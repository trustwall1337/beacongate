package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport/transporttest"
)

// TestReconnectBackoffBeforeThreshold confirms we use the fast 200ms
// retry while consecutive_failures < reconnectingAfterFails.
func TestReconnectBackoffBeforeThreshold(t *testing.T) {
	p := &Pump{}
	for i := 1; i < reconnectingAfterFails; i++ {
		p.consecutiveFails = i
		got := p.reconnectBackoff()
		if got != 200*time.Millisecond {
			t.Errorf("fails=%d: backoff=%v, want 200ms (still in fast-retry zone)", i, got)
		}
	}
}

// TestReconnectBackoffExponential confirms the schedule once we're
// past the threshold: 5→3s, 6→6s, 7→12s, 8→24s, 9+→30s cap.
func TestReconnectBackoffExponential(t *testing.T) {
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{5, 3 * time.Second},
		{6, 6 * time.Second},
		{7, 12 * time.Second},
		{8, 24 * time.Second},
		{9, 30 * time.Second}, // capped
		{15, 30 * time.Second},
	}
	for _, tc := range cases {
		p := &Pump{}
		p.consecutiveFails = tc.fails
		got := p.reconnectBackoff()
		if got != tc.want {
			t.Errorf("fails=%d: backoff=%v, want %v", tc.fails, got, tc.want)
		}
	}
}

// TestPumpLongPollDeadlineNotCountedAsFailure: when the long-poll's
// own idleHold+5s deadline expires (no upstream data arrived), tick()
// MUST return nil so loop() calls recordSuccess instead of recordErr.
//
// Regression guard for the bug discovered in production where every
// idle long-poll counted as a failure: 3 idle ticks tripped state to
// "degraded", 5 tripped "reconnecting" exponential backoff, and the
// data path stalled even though the transport was healthy.
//
// Symptom in the field was: client status reported state=degraded with
// last_error="...context deadline exceeded" while server logs showed
// session.open / upstream_eof landing cleanly — server-side fine,
// client-side state machine over-reactive to natural long-poll timeouts.
func TestPumpLongPollDeadlineNotCountedAsFailure(t *testing.T) {
	// Transport handler returns context.DeadlineExceeded immediately —
	// functionally identical to the real production scenario (the
	// pump's long-poll context fires its idleHold+5s deadline) but
	// without waiting 5s per tick. errors.Is(err, context.DeadlineExceeded)
	// holds either way; that's the assertion the pump's discriminator
	// makes at sessions.go:439.
	ft := &transporttest.Fake{
		Handler: func(_ context.Context, _ []byte) ([]byte, error) {
			return nil, context.DeadlineExceeded
		},
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.ClientConfig{
		ClientID:   "client-deadline-test",
		ListenAddr: "127.0.0.1:0",
		Server:     config.ClientServerConfig{URL: "http://example", Key: config.EncodeKey(key)},
		Transport:  config.ClientTransportConfig{Type: "fake"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	rt, err := New(cfg, ft)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	p := NewPump(rt)
	// Tight idleHold so the long-poll deadline fires fast in tests.
	// Real production value is much larger.
	p.SetIdleHold(20 * time.Millisecond)
	// Inject a placeholder session so tick() takes the longPoll path
	// (instead of parking on the empty-sessions branch).
	p.mu.Lock()
	p.sessions["test-session"] = &ClientSession{}
	p.mu.Unlock()

	// Drive enough ticks to exceed BOTH the degraded threshold (3) and
	// the reconnecting threshold (5). All should be silent no-ops.
	for i := 0; i < reconnectingAfterFails+2; i++ {
		didWork, err := p.tick()
		if err != nil {
			t.Fatalf("tick #%d: returned err=%v; long-poll deadline should be a non-error completion", i+1, err)
		}
		// A long-poll completing on its own deadline counts as didWork
		// (success branch), not a failure.
		if !didWork {
			t.Fatalf("tick #%d: didWork=false unexpectedly (idle slot exhausted? sessions empty?)", i+1)
		}
	}

	if got := p.consecutiveFails; got != 0 {
		t.Errorf("consecutiveFails=%d after %d long-poll deadlines; want 0", got, reconnectingAfterFails+2)
	}
	if got := rt.State(); got == StateDegraded || got == StateError {
		t.Errorf("state=%v after long-poll deadlines; want healthy state (Connected/Starting), not degraded/error", got)
	}
}

// TestReconnectStateTransitionsThroughRecordErr drives recordErr/
// recordSuccess directly and asserts the runtime state-flag changes
// line up with the failure-count thresholds.
func TestReconnectStateTransitionsThroughRecordErr(t *testing.T) {
	noopHandler := func(env protocol.Envelope) protocol.Envelope { return env }
	rt := makeRuntime(t, noopHandler)
	defer rt.Close()
	p := &Pump{rt: rt}

	// 1 failure: still Starting (no transition yet).
	p.recordErr(errors.New("transient"))
	if got := rt.State(); got != StateStarting {
		t.Errorf("after 1 failure: state=%v, want StateStarting", got)
	}

	// 3 failures total → degraded.
	p.recordErr(errors.New("transient"))
	p.recordErr(errors.New("transient"))
	if got := rt.State(); got != StateDegraded {
		t.Errorf("after 3 failures: state=%v, want StateDegraded", got)
	}

	// 5 failures total → error / reconnecting.
	p.recordErr(errors.New("transient"))
	p.recordErr(errors.New("transient"))
	if got := rt.State(); got != StateError {
		t.Errorf("after 5 failures: state=%v, want StateError (reconnecting)", got)
	}
	if !p.reconnecting {
		t.Errorf("after 5 failures: reconnecting flag should be true")
	}

	// recordSuccess returns to Connected and clears the reconnecting flag.
	p.recordSuccess()
	if got := rt.State(); got != StateConnected {
		t.Errorf("after recordSuccess: state=%v, want StateConnected", got)
	}
	if p.reconnecting {
		t.Errorf("after recordSuccess: reconnecting flag should be cleared")
	}
}
