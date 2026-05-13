package runtime

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
)

// TestInboundDataOutOfOrderReassembled is the core correctness test for
// the server-side inbound reorder buffer. The client is allowed to run
// multiple outbound POST workers, so DATA frames for one session can
// land here out-of-order on the wire. The server must buffer and
// reassemble in seq order before writing to the upstream socket;
// otherwise the upstream sees garbled bytes.
//
// Wire-arrival order here is 0, 2, 1, 4, 3. The echo upstream must
// receive bytes in the canonical order "ABCDE".
func TestInboundDataOutOfOrderReassembled(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()

	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	// OPEN the session.
	envOpen := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "client-reorder",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1",
				Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
		},
	}
	roundtrip(t, ts, sealer, envOpen)

	// Send DATA frames out of order: 0, 2, 1, 4, 3. Each as its own
	// envelope (== separate POST), since the goal is to model out-of-
	// order POST arrival. Each DATA-carrying POST may have echoed bytes
	// folded into its response by the server's active-drain window;
	// accumulate them into got along the way.
	got := []byte{}
	accumulate := func(env protocol.Envelope) {
		for _, m := range env.Messages {
			if m.Type == protocol.MessageTypeReset && m.SessionID == "s1" {
				t.Fatalf("unexpected RESET code=%s reason=%s", m.Code, m.Reason)
			}
			if m.Type == protocol.MessageTypeData && m.SessionID == "s1" {
				got = append(got, m.Data...)
			}
		}
	}
	for _, x := range []struct {
		seq uint64
		b   []byte
	}{
		{0, []byte("A")}, {2, []byte("C")}, {1, []byte("B")},
		{4, []byte("E")}, {3, []byte("D")},
	} {
		env := protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-reorder",
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(x.seq), Data: x.b},
			},
		}
		accumulate(roundtrip(t, ts, sealer, env))
	}

	// Poll for any bytes still pending after the last send.
	want := []byte("ABCDE")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(got) < len(want) {
		envPoll := protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-reorder",
			Compression: protocol.CompressionNone,
			Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "p"}},
		}
		accumulate(roundtrip(t, ts, sealer, envPoll))
		if len(got) < len(want) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("upstream echo: got %q, want %q (reorder failed or upstream not drained)", got, want)
	}
}

// TestInboundDataDuplicateDropped confirms that a replayed DATA frame
// (same seq as one already consumed) is silently dropped rather than
// treated as out-of-order. This is the idempotent-retry path: if the
// transport redelivers a frame the server already accepted, the
// upstream must NOT receive it twice.
func TestInboundDataDuplicateDropped(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()

	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	// Accumulator that pulls echo bytes from every response (DATA-
	// carrying POSTs may fold echoes via active-drain) and fails on RESET.
	got := []byte{}
	accumulate := func(env protocol.Envelope) {
		for _, m := range env.Messages {
			if m.Type == protocol.MessageTypeReset && m.SessionID == "s1" {
				t.Fatalf("unexpected RESET code=%s reason=%s", m.Code, m.Reason)
			}
			if m.Type == protocol.MessageTypeData && m.SessionID == "s1" {
				got = append(got, m.Data...)
			}
		}
	}

	// OPEN + seq=0 in one batch.
	envOpen := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "client-dup",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1",
				Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
			{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(0), Data: []byte("X")},
		},
	}
	accumulate(roundtrip(t, ts, sealer, envOpen))

	// Replay seq=0 — must NOT cause a reset, must NOT be re-echoed.
	envReplay := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-dup",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(0), Data: []byte("X")},
		},
	}
	accumulate(roundtrip(t, ts, sealer, envReplay))

	// Now send seq=1 and confirm the upstream sees exactly "XY" — one
	// X (not duplicated despite replay) followed by Y.
	envNext := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-dup",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(1), Data: []byte("Y")},
		},
	}
	accumulate(roundtrip(t, ts, sealer, envNext))

	want := []byte("XY")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(got) < len(want) {
		envPoll := protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-dup",
			Compression: protocol.CompressionNone,
			Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "p"}},
		}
		accumulate(roundtrip(t, ts, sealer, envPoll))
		if len(got) < len(want) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo after duplicate-replay: got %q, want %q", got, want)
	}
}

// TestInboundDataReorderBufferOverflow confirms that an unfillable gap
// (missing seq + maxInboundReorderBuffer trailing frames) is rejected
// as BAD_SEQUENCE rather than buffered indefinitely. Otherwise a lost
// frame would leak memory until the session timed out via the reaper.
func TestInboundDataReorderBufferOverflow(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()

	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	// OPEN.
	envOpen := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "client-overflow",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1",
				Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
		},
	}
	roundtrip(t, ts, sealer, envOpen)

	// Send seqs 1..maxInboundReorderBuffer (skipping 0 to create a gap).
	// All should buffer without error.
	for i := uint64(1); i <= uint64(maxInboundReorderBuffer); i++ {
		env := protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-overflow",
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(i), Data: []byte{byte(i)}},
			},
		}
		resp := roundtrip(t, ts, sealer, env)
		for _, m := range resp.Messages {
			if m.Type == protocol.MessageTypeReset && m.SessionID == "s1" {
				t.Fatalf("seq=%d caused premature RESET (only %d entries buffered)", i, i)
			}
		}
	}

	// One more (maxInboundReorderBuffer+1) — must overflow and RESET
	// the session as BAD_SEQUENCE.
	overflowSeq := uint64(maxInboundReorderBuffer + 1)
	env := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-overflow",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(overflowSeq), Data: []byte{0xff}},
		},
	}
	resp := roundtrip(t, ts, sealer, env)
	gotReset := false
	for _, m := range resp.Messages {
		if m.Type == protocol.MessageTypeReset && m.SessionID == "s1" && m.Code == "BAD_SEQUENCE" {
			gotReset = true
			break
		}
	}
	if !gotReset {
		t.Fatalf("seq=%d should have overflowed reorder buffer + reset; got %+v", overflowSeq, resp.Messages)
	}
}
