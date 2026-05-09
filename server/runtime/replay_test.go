package runtime

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

// TestTunnelIdempotentRetryWithinResponseWindow exercises plan B4's
// response-cache idempotency. Re-POSTing the exact same wire bytes
// within the 60s response-cache TTL must return the cached response
// without re-processing. This is what makes the appsscript transport's
// per-batch failover safe under retry.
func TestTunnelIdempotentRetryWithinResponseWindow(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	srv := New("server-test", sealer, testDialer(2*time.Second), nil)
	defer srv.Close()
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	env := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "idempotent-client",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeOpen, SessionID: "s1", Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
		},
	}
	plain, _ := protocol.EncodeEnvelope(env)
	wire, _ := sealer.Seal(env.ClientID, plain)

	// First POST → server processes, caches the response.
	status1, body1 := postWire(t, ts.URL+"/tunnel", wire)
	if status1 != http.StatusOK {
		t.Fatalf("first POST: status %d body=%s", status1, body1)
	}

	// Replay the EXACT same wire bytes (idempotent retry). Server
	// must return the cached bytes verbatim — no double-process,
	// no rejection.
	status2, body2 := postWire(t, ts.URL+"/tunnel", wire)
	if status2 != http.StatusOK {
		t.Fatalf("idempotent retry: status %d body=%s", status2, body2)
	}
	if !bytes.Equal(body1, body2) {
		t.Fatalf("idempotent retry: body differs from cached response (re-processed instead of cached)")
	}
}

// TestTunnelRejectsAADTamperedClientID is the server-level
// regression for plan B1's AAD binding: a captured wire packet with
// its cleartext client_id swapped at the same length must fail the
// AEAD check and return 401, not a more revealing status code.
func TestTunnelRejectsAADTamperedClientID(t *testing.T) {
	sealer := newSealer(t)
	srv := New("server-test", sealer, testDialer(time.Second), nil)
	defer srv.Close()
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	env := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "client-alpha",
		Compression: protocol.CompressionNone,
		Messages:    []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: "p"}},
	}
	plain, _ := protocol.EncodeEnvelope(env)
	wire, _ := sealer.Seal(env.ClientID, plain)

	// Swap the cleartext client_id in the wire header (same length so
	// the layout doesn't shift). Must be an exact-length swap.
	const swap = "client-omega"
	if len(swap) != len("client-alpha") {
		t.Fatal("test setup: swap target must be same length")
	}
	off := 1 + 2 // wire-version byte + 2-byte BE length prefix
	for i := range swap {
		wire[off+i] = swap[i]
	}

	status, _ := postWire(t, ts.URL+"/tunnel", wire)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 on AAD-tampered client_id, got %d", status)
	}
}

// TestTunnelRejectsUnknownWireVersion exercises the cleartext-header
// check before any AEAD work. A first byte other than 0x01 returns
// HTTP 401 with a generic body — never a hint of WHY (defense against
// fingerprinting).
func TestTunnelRejectsUnknownWireVersion(t *testing.T) {
	sealer := newSealer(t)
	srv := New("server-test", sealer, testDialer(time.Second), nil)
	defer srv.Close()
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wire := []byte{0xFF, 0x00, 0x05, 'a', 'b', 'c', 'd', 'e'}
	wire = append(wire, make([]byte, 28)...) // garbage, won't reach AEAD
	status, _ := postWire(t, ts.URL+"/tunnel", wire)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 on bad wire-version, got %d", status)
	}
}

// postWire fires one HTTP POST and returns (statusCode, body) so the
// caller never has to close the response body — postWire already did.
// Returning *http.Response would trip golangci-lint's bodyclose check
// because it can't see across function boundaries.
func postWire(t *testing.T, url string, wire []byte) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(wire)) //nolint:noctx // tests
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
