package runtime

import (
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

// TestDialDoesNotFlushOpen confirms the bedrock fix: Dial queues OPEN
// in the outbox but does NOT signal the outbound worker. If this
// regresses, the freshly-dialed upstream socket sits silent during the
// next carrier round-trip and aggressive edges (YouTube's TCP frontend)
// reap the idle connection at ~15 s.
func TestDialDoesNotFlushOpen(t *testing.T) {
	rt := makeRuntime(t, func(protocol.Envelope) protocol.Envelope { return protocol.Envelope{} })
	defer rt.Close()
	p := NewPump(rt)

	sess, err := p.Dial(protocol.Target{Network: "tcp", Host: "example.com", Port: 443})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// OPEN must be in the outbox.
	p.mu.Lock()
	box := append([]protocol.Message(nil), p.outbox...)
	p.mu.Unlock()
	if len(box) != 1 || box[0].Type != protocol.MessageTypeOpen {
		t.Fatalf("outbox after Dial: want one OPEN, got %+v", box)
	}

	// flush channel must be empty — Dial does not wake the outbound worker.
	select {
	case <-p.flush:
		t.Fatalf("flush signalled by Dial; OPEN/DATA fusion broken (OPEN would ship alone)")
	default:
	}
}

// TestWriteFusesOpenAndData confirms the fusion fast path: the first
// Write clears openPending, stops the safety timer, and enqueues DATA
// behind the still-queued OPEN. After Write, the outbox holds both
// frames so the next drain ships them in one POST.
func TestWriteFusesOpenAndData(t *testing.T) {
	rt := makeRuntime(t, func(protocol.Envelope) protocol.Envelope { return protocol.Envelope{} })
	defer rt.Close()
	p := NewPump(rt)

	sess, err := p.Dial(protocol.Target{Network: "tcp", Host: "example.com", Port: 443})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	if _, err := sess.Write([]byte("ClientHello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	p.mu.Lock()
	box := append([]protocol.Message(nil), p.outbox...)
	p.mu.Unlock()
	if len(box) != 2 {
		t.Fatalf("outbox after Write: want OPEN+DATA, got %d frames %+v", len(box), box)
	}
	if box[0].Type != protocol.MessageTypeOpen {
		t.Fatalf("outbox[0]: want OPEN, got %v", box[0].Type)
	}
	if box[1].Type != protocol.MessageTypeData {
		t.Fatalf("outbox[1]: want DATA, got %v", box[1].Type)
	}
	if sess.openPending {
		t.Fatalf("openPending still true after first Write")
	}
}

// TestFallbackShipsOpenAlone confirms the safety-timer path: when a
// SOCKS client Dials but never Writes (server-talks-first protocols),
// the timer fires within openFuseFallback and signals the flush so the
// server can dial the upstream and start draining its greeting.
func TestFallbackShipsOpenAlone(t *testing.T) {
	rt := makeRuntime(t, func(protocol.Envelope) protocol.Envelope { return protocol.Envelope{} })
	defer rt.Close()
	p := NewPump(rt)

	sess, err := p.Dial(protocol.Target{Network: "tcp", Host: "smtp.example.com", Port: 587})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// Wait long enough for the safety timer to fire (openFuseFallback + slack).
	select {
	case <-p.flush:
		// good — fallback shipped OPEN alone.
	case <-time.After(openFuseFallback + 100*time.Millisecond):
		t.Fatalf("safety timer never signalled flush within %v", openFuseFallback+100*time.Millisecond)
	}
	sess.mu.Lock()
	pending := sess.openPending
	sess.mu.Unlock()
	if pending {
		t.Fatalf("openPending still true after fallback fired")
	}
}

// TestCloseWithoutWriteShipsOpenAndClose covers Dial-then-Close: the
// CLOSE-enqueue must also flush so the server tears down the half-open
// session promptly instead of waiting on the safety timer.
func TestCloseWithoutWriteShipsOpenAndClose(t *testing.T) {
	rt := makeRuntime(t, func(protocol.Envelope) protocol.Envelope { return protocol.Envelope{} })
	defer rt.Close()
	p := NewPump(rt)

	sess, err := p.Dial(protocol.Target{Network: "tcp", Host: "example.com", Port: 443})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	p.mu.Lock()
	box := append([]protocol.Message(nil), p.outbox...)
	p.mu.Unlock()
	if len(box) != 2 {
		t.Fatalf("outbox after Close: want OPEN+CLOSE, got %d frames %+v", len(box), box)
	}
	if box[0].Type != protocol.MessageTypeOpen || box[1].Type != protocol.MessageTypeClose {
		t.Fatalf("outbox order: want [OPEN, CLOSE], got [%v, %v]", box[0].Type, box[1].Type)
	}
}
