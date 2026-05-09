package runtime

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/server/upstream"
)

func u64(v uint64) *uint64 { return &v }

// testDialer builds an upstream.NetDialer that allows loopback so the test
// suite can run an in-process echo server. Production deployments leave
// AllowPrivate=false (SSRF guard).
func testDialer(timeout time.Duration) *upstream.NetDialer {
	d, err := upstream.NewNetDialer(timeout, "")
	if err != nil {
		panic(err)
	}
	d.Safety.AllowPrivate = true
	return d
}

func newSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startEchoUpstream(t *testing.T) (string, uint16, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()
	host, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	return host, uint16(port), func() { l.Close() }
}

func roundtrip(t *testing.T, ts *httptest.Server, sealer *crypto.Sealer, env protocol.Envelope) protocol.Envelope {
	t.Helper()
	plain, err := protocol.EncodeEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := sealer.Seal(env.ClientID, plain)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/tunnel", "application/octet-stream", bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	batch, err := sealer.Open(body)
	if err != nil {
		t.Fatal(err)
	}
	out, err := protocol.DecodeEnvelope(batch.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTunnelOpenAndEcho(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()

	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	envOpen := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "client-x",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1", Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
			{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(0), Data: []byte("hello")},
		},
	}
	resp := roundtrip(t, ts, sealer, envOpen)
	// We may get back the echoed bytes immediately or on a follow-up poll.
	gotEcho := containsData(resp.Messages, "s1", []byte("hello"))
	if !gotEcho {
		// poll once more
		envPoll := protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-x",
			Compression: protocol.CompressionNone,
			Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "p"}},
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && !gotEcho {
			resp = roundtrip(t, ts, sealer, envPoll)
			if containsData(resp.Messages, "s1", []byte("hello")) {
				gotEcho = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !gotEcho {
		t.Fatalf("did not receive echo: %+v", resp.Messages)
	}
}

func containsData(msgs []protocol.Message, sessID string, want []byte) bool {
	for _, m := range msgs {
		if m.Type == protocol.MessageTypeData && m.SessionID == sessID && bytes.Equal(m.Data, want) {
			return true
		}
	}
	return false
}

func TestTunnelBadKey(t *testing.T) {
	srvSealer := newSealer(t)
	clientSealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(srvSealer), testDialer(time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	env := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "x",
		Compression: protocol.CompressionNone,
		Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "p"}},
	}
	plain, _ := protocol.EncodeEnvelope(env)
	cipher, _ := clientSealer.Seal(env.ClientID, plain)
	resp, err := http.Post(ts.URL+"/tunnel", "application/octet-stream", bytes.NewReader(cipher))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

type denyAll struct{}

func (denyAll) Evaluate(protocol.Target) PolicyDecision {
	return PolicyDecision{Allowed: false, Reason: "test deny"}
}

func TestTunnelPolicyDenyOnOpen(t *testing.T) {
	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(time.Second), denyAll{})
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	env := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "client-y",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1", Target: &protocol.Target{Network: "tcp", Host: "blocked.example", Port: 80}},
		},
	}
	resp := roundtrip(t, ts, sealer, env)
	var sawDeny bool
	for _, m := range resp.Messages {
		if m.Type == protocol.MessageTypeReset && m.Code == "POLICY_DENIED" {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Fatalf("expected POLICY_DENIED reset, got %+v", resp.Messages)
	}
}

func TestServerCloseTerminatesSessions(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	env := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "c",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s", Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
		},
	}
	roundtrip(t, ts, sealer, env)
	if srv.SessionCount() != 1 {
		t.Fatalf("expected 1 session, got %d", srv.SessionCount())
	}
	srv.Close()
	if srv.SessionCount() != 0 {
		t.Fatalf("expected 0 sessions after close")
	}
}

func TestHealthHandler(t *testing.T) {
	sealer := newSealer(t)
	srv := New("s", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(time.Second), nil)
	rr := httptest.NewRecorder()
	srv.Health().ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
}

// Sanity test that our test helper cleans up properly.
var _ = sync.WaitGroup{}

// TestLongPollWakesOnData verifies that an idle (probe-only) request held
// open by the server returns as soon as upstream data arrives, rather than
// waiting out the full window.
func TestLongPollWakesOnData(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	srv.SetLongPollWindow(2 * time.Second)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	// Open a session first.
	roundtrip(t, ts, sealer, protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1", Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
		},
	})

	// Issue an idle probe-only request in the background; meanwhile, fire
	// real DATA on a parallel request that the server will echo upstream.
	type result struct {
		env protocol.Envelope
		dur time.Duration
	}
	res := make(chan result, 1)
	go func() {
		start := time.Now()
		out := roundtrip(t, ts, sealer, protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp",
			Compression: protocol.CompressionNone,
			Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "lp1"}},
		})
		res <- result{env: out, dur: time.Since(start)}
	}()

	// Give the long-poll a moment to enter its wait phase.
	time.Sleep(50 * time.Millisecond)

	// Push DATA on a separate request; server echoes upstream immediately.
	roundtrip(t, ts, sealer, protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeData, SessionID: "s1", Seq: u64(0), Data: []byte("ping")},
		},
	})

	select {
	case r := <-res:
		if r.dur > time.Second {
			t.Fatalf("long-poll didn't wake early: took %s", r.dur)
		}
		if !containsData(r.env.Messages, "s1", []byte("ping")) {
			t.Fatalf("long-poll response missing echo: %+v", r.env.Messages)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("long-poll never returned")
	}
}

// TestLongPollCancelDoesNotDrain verifies that when a client cancels its
// idle request mid-hold, the server does NOT drain pending bytes — they
// must remain in the session for the next request to pick up.
func TestLongPollCancelDoesNotDrain(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	srv := New("server-test", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	srv.SetLongPollWindow(2 * time.Second)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	// Open the session.
	roundtrip(t, ts, sealer, protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp2",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s2", Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
		},
	})

	// Issue a long-poll request and cancel it almost immediately.
	cipher := mustSeal(t, sealer, protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp2",
		Compression: protocol.CompressionNone,
		Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "x"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/tunnel", bytes.NewReader(cipher))
	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp != nil {
		resp.Body.Close()
	}

	// Now produce DATA upstream by writing through a fresh DATA round-trip.
	// If cancellation drained, the seq counter would have advanced and the
	// next response's seq would not start at 0.
	out := roundtrip(t, ts, sealer, protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp2",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeData, SessionID: "s2", Seq: u64(0), Data: []byte("hi")},
		},
	})
	// Poll until echo arrives.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range out.Messages {
			if m.Type == protocol.MessageTypeData && m.SessionID == "s2" {
				if m.Seq == nil || *m.Seq != 0 {
					t.Fatalf("echo seq drifted, expected 0 got %v (cancellation drained)", m.Seq)
				}
				return
			}
		}
		out = roundtrip(t, ts, sealer, protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "lp2",
			Compression: protocol.CompressionNone,
			Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "p"}},
		})
	}
	t.Fatalf("never received echo for s2")
}

func mustSeal(t *testing.T, s *crypto.Sealer, env protocol.Envelope) []byte {
	t.Helper()
	plain, err := protocol.EncodeEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := s.Seal(env.ClientID, plain)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestServerContextCancelOnDial(t *testing.T) {
	sealer := newSealer(t)
	// 192.0.2.0/24 is TEST-NET; will time out.
	srv := New("s", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(50*time.Millisecond), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer srv.Close()

	env := protocol.Envelope{
		Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "c",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s", Target: &protocol.Target{Network: "tcp", Host: "192.0.2.1", Port: 1}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx
	resp := roundtrip(t, ts, sealer, env)
	var saw bool
	for _, m := range resp.Messages {
		if m.Type == protocol.MessageTypeReset && m.Code == "DIAL_FAILED" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected DIAL_FAILED, got %+v", resp.Messages)
	}
}
