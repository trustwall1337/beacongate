package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
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
