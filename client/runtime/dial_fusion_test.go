package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport/transporttest"
)

// recordingHandler captures every Roundtrip call so tests can assert
// what messages landed in which POST. Decodes the encrypted batch on
// the way through and stores the decoded message types per call. To
// avoid PROBE spam under the in-process fake transport, it sleeps for
// probeDelay before responding to PROBE-only batches — modelling the
// real-world Apps Script per-call latency that paces idle long-polls
// in production.
type recordingHandler struct {
	mu         sync.Mutex
	calls      [][]protocol.MessageType
	hits       atomic.Int64
	probeDelay time.Duration

	sealer *crypto.Sealer
	reply  func(env protocol.Envelope) protocol.Envelope
}

func (r *recordingHandler) handle(_ context.Context, ct []byte) ([]byte, error) {
	batch, err := r.sealer.Open(ct)
	if err != nil {
		return nil, err
	}
	env, err := protocol.DecodeEnvelope(batch.Plaintext)
	if err != nil {
		return nil, err
	}
	types := make([]protocol.MessageType, len(env.Messages))
	probeOnly := true
	for i, m := range env.Messages {
		types[i] = m.Type
		if m.Type != protocol.MessageTypeProbe {
			probeOnly = false
		}
	}
	if probeOnly && r.probeDelay > 0 {
		time.Sleep(r.probeDelay)
	}
	r.mu.Lock()
	r.calls = append(r.calls, types)
	r.mu.Unlock()
	r.hits.Add(1)

	out := r.reply(env)
	plain, err := protocol.EncodeEnvelope(out)
	if err != nil {
		return nil, err
	}
	return r.sealer.Seal(out.ClientID, plain)
}

// outboundCalls returns only the recorded POSTs that carry session
// traffic (OPEN/DATA/CLOSE/RESET). PROBE-only POSTs are idle long-
// polls; counting them would conflate the idle worker's keepalive
// rate with the outbound worker's traffic.
func (r *recordingHandler) outboundCalls() [][]protocol.MessageType {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]protocol.MessageType
	for _, types := range r.calls {
		probeOnly := true
		for _, t := range types {
			if t != protocol.MessageTypeProbe {
				probeOnly = false
				break
			}
		}
		if !probeOnly {
			cp := make([]protocol.MessageType, len(types))
			copy(cp, types)
			out = append(out, cp)
		}
	}
	return out
}

func newDialFusionRuntime(t *testing.T, reply func(env protocol.Envelope) protocol.Envelope) (*Runtime, *recordingHandler) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingHandler{sealer: sealer, reply: reply, probeDelay: 50 * time.Millisecond}
	ft := &transporttest.Fake{Handler: rec.handle}
	cfg := &config.ClientConfig{
		ClientID:   "client-fusion",
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
	return rt, rec
}

// noopServerReply mirrors a minimal server: every Probe gets an ack,
// every other message produces an empty (probe noop) response so the
// envelope is non-empty and Decode succeeds.
func noopServerReply(env protocol.Envelope) protocol.Envelope {
	out := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "server",
		Compression: protocol.CompressionNone,
	}
	for _, m := range env.Messages {
		if m.Type == protocol.MessageTypeProbe {
			out.Messages = append(out.Messages, protocol.Message{
				Type:    protocol.MessageTypeProbe,
				ProbeID: m.ProbeID,
				Status:  "ok",
			})
		}
	}
	if len(out.Messages) == 0 {
		out.Messages = append(out.Messages, protocol.Message{
			Type: protocol.MessageTypeProbe, ProbeID: "noop", Status: "ok",
		})
	}
	return out
}

// TestDialDefersOutboundUntilFirstWrite is the core proof for the
// OPEN+first-Write fusion. After Dial, the outbound worker should
// see no session traffic until the user's first Write — at which
// point OPEN and the first DATA chunk must land in the SAME POST.
func TestDialDefersOutboundUntilFirstWrite(t *testing.T) {
	rt, rec := newDialFusionRuntime(t, noopServerReply)
	defer rt.Close()

	p := NewPump(rt)
	p.Start()
	defer p.Close()

	sess, err := p.Dial(protocol.Target{Network: "tcp", Host: "x", Port: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Give the pump a moment. With the fix, no outbound POST should
	// fire: OPEN is held on the session, the safety timer is 1 s away,
	// and no enqueue has happened yet.
	time.Sleep(100 * time.Millisecond)
	if got := rec.outboundCalls(); len(got) != 0 {
		t.Fatalf("outbound POST fired before first Write: %v", got)
	}

	if _, err := sess.Write([]byte("ClientHello")); err != nil {
		t.Fatal(err)
	}

	// Wait for the coalesce + roundtrip to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.outboundCalls()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := rec.outboundCalls()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 outbound POST after Write, got %d: %v", len(got), got)
	}
	types := got[0]
	if len(types) != 2 ||
		types[0] != protocol.MessageTypeOpen ||
		types[1] != protocol.MessageTypeData {
		t.Fatalf("first outbound POST should fuse [OPEN, DATA], got %v", types)
	}
}

// TestDialSafetyTimerFiresWhenNoWrite confirms the degenerate case:
// if the user opens a session and never writes, the 1-second safety
// timer eventually flushes OPEN alone so the server can dial the
// upstream.
func TestDialSafetyTimerFiresWhenNoWrite(t *testing.T) {
	rt, rec := newDialFusionRuntime(t, noopServerReply)
	defer rt.Close()

	p := NewPump(rt)
	p.Start()
	defer p.Close()

	if _, err := p.Dial(protocol.Target{Network: "tcp", Host: "x", Port: 1}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(rec.outboundCalls()) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := rec.outboundCalls()
	if len(got) < 1 {
		t.Fatalf("safety timer never fired after 1.5 s")
	}
	// First outbound POST should be OPEN-only (no Write happened).
	if len(got[0]) != 1 || got[0][0] != protocol.MessageTypeOpen {
		t.Fatalf("safety-timer POST should be [OPEN], got %v", got[0])
	}
}

// TestDialCloseWithoutWriteFusesOpenAndClose confirms the SOCKS-
// Connect-then-immediate-Close pattern (e.g. user hits Ctrl-C before
// any TLS data) batches OPEN and CLOSE into one POST. Important so
// the server dials and immediately tears down in a single Apps
// Script round-trip instead of two.
func TestDialCloseWithoutWriteFusesOpenAndClose(t *testing.T) {
	rt, rec := newDialFusionRuntime(t, noopServerReply)
	defer rt.Close()

	p := NewPump(rt)
	p.Start()
	defer p.Close()

	sess, err := p.Dial(protocol.Target{Network: "tcp", Host: "x", Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(rec.outboundCalls()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := rec.outboundCalls()
	if len(got) != 1 {
		t.Fatalf("expected 1 outbound POST after Dial+Close, got %d: %v", len(got), got)
	}
	types := got[0]
	if len(types) != 2 ||
		types[0] != protocol.MessageTypeOpen ||
		types[1] != protocol.MessageTypeClose {
		t.Fatalf("first outbound POST should fuse [OPEN, CLOSE], got %v", types)
	}
}
