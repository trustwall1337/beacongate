package runtime

import (
	"bytes"
	"testing"
	"time"
)

// TestDeliverDataInOrder confirms the in-order fast path: chunks
// arriving with the expected seq go straight to the inbox.
func TestDeliverDataInOrder(t *testing.T) {
	s := &ClientSession{
		inbox:  make(chan []byte, 8),
		closed: make(chan struct{}),
	}
	p := &Pump{
		sessions: map[string]*ClientSession{"sid": s},
	}

	for i := uint64(0); i < 4; i++ {
		seq := i
		p.deliverData("sid", &seq, []byte{byte('a' + i)}, false)
	}

	want := []byte("abcd")
	got := drainInbox(t, s, len(want))
	if !bytes.Equal(got, want) {
		t.Fatalf("inbox: got %q want %q", got, want)
	}
}

// TestDeliverDataReorders is the core correctness test for the
// concurrent-POST design. Chunks arrive out of order on the wire
// (seq=2 before seq=0), but the session inbox sees them in the
// original send-seq order.
func TestDeliverDataReorders(t *testing.T) {
	s := &ClientSession{
		inbox:  make(chan []byte, 8),
		closed: make(chan struct{}),
	}
	p := &Pump{
		sessions: map[string]*ClientSession{"sid": s},
	}

	// Wire-arrival order: 2, 0, 1, 4, 3 (jumbled). Expected inbox
	// order after reordering: 0, 1, 2, 3, 4.
	wireOrder := []uint64{2, 0, 1, 4, 3}
	for _, seq := range wireOrder {
		s := seq
		p.deliverData("sid", &s, []byte{byte('a' + seq)}, false)
	}

	want := []byte("abcde")
	got := drainInbox(t, s, len(want))
	if !bytes.Equal(got, want) {
		t.Fatalf("inbox: got %q want %q (reorder failed)", got, want)
	}
}

// TestDeliverDataDropsDuplicate confirms re-delivery of an already-
// consumed seq is silently dropped (idempotent retry replay protection).
func TestDeliverDataDropsDuplicate(t *testing.T) {
	s := &ClientSession{
		inbox:  make(chan []byte, 8),
		closed: make(chan struct{}),
	}
	p := &Pump{
		sessions: map[string]*ClientSession{"sid": s},
	}
	seq0 := uint64(0)
	p.deliverData("sid", &seq0, []byte("x"), false)
	// Deliver seq=0 again (e.g. server retried with cached response).
	p.deliverData("sid", &seq0, []byte("x"), false)

	got := drainInbox(t, s, 1)
	if !bytes.Equal(got, []byte("x")) {
		t.Fatalf("inbox: got %q want \"x\"", got)
	}
	// Inbox should be empty — duplicate was dropped, not re-delivered.
	select {
	case extra := <-s.inbox:
		t.Fatalf("duplicate was delivered: %q", extra)
	case <-time.After(50 * time.Millisecond):
		// good
	}
}

// TestDeliverDataNoSeqLegacyPath confirms legacy server responses
// (no seq field populated) bypass the reorder buffer and deliver
// directly. Important so deployments running an older server don't
// break under a newer client.
func TestDeliverDataNoSeqLegacyPath(t *testing.T) {
	s := &ClientSession{
		inbox:  make(chan []byte, 8),
		closed: make(chan struct{}),
	}
	p := &Pump{
		sessions: map[string]*ClientSession{"sid": s},
	}
	p.deliverData("sid", nil, []byte("hello"), false)
	got := drainInbox(t, s, 5)
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("legacy path: got %q want \"hello\"", got)
	}
}

func drainInbox(t *testing.T, s *ClientSession, n int) []byte {
	t.Helper()
	out := make([]byte, 0, n)
	for len(out) < n {
		select {
		case chunk := <-s.inbox:
			out = append(out, chunk...)
		case <-time.After(time.Second):
			t.Fatalf("timed out reading inbox: got %d/%d bytes", len(out), n)
		}
	}
	return out
}
