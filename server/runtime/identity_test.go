package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

// Plan B7 tests the residual identity guarantees of v1.1's
// per-client key derivation: even though distinct client_ids are
// cryptographically isolated, the cleartext client_id is still
// self-asserted, so two peers can claim the same id. The contract
// the server promises in that case must be tested.

// TestSameClientIDSharesSessionCap pins the documented behavior:
// per-client session caps are enforced against the *aggregate*
// of all peers claiming that id. This is by design, not a bug —
// docs/architecture.md and SECURITY.md call it out.
func TestSameClientIDSharesSessionCap(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	srv := New("server-test", sealer, testDialer(2*time.Second), nil)
	srv.SetMaxSessionsPerClient(2) // tight cap for the test
	defer srv.Close()
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	openMsg := func(sessID string) protocol.Envelope {
		return protocol.Envelope{
			Version:     protocol.Version{Major: 1, Minor: 1},
			ClientID:    "shared-id",
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeOpen, SessionID: sessID, Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
			},
		}
	}

	// First peer opens 2 sessions — both succeed (at the cap).
	for _, id := range []string{"s1", "s2"} {
		out := roundtrip(t, ts, sealer, openMsg(id))
		for _, m := range out.Messages {
			if m.Type == protocol.MessageTypeReset && m.Code == "POLICY_DENIED" {
				t.Fatalf("session %s rejected unexpectedly: %+v", id, m)
			}
		}
	}

	// Second peer (same client_id, distinct session_id) attempts a 3rd
	// open — should be POLICY_DENIED because the cap counts the
	// aggregate across both peers.
	out := roundtrip(t, ts, sealer, openMsg("s3"))
	var sawDeny bool
	for _, m := range out.Messages {
		if m.Type == protocol.MessageTypeReset && m.Code == "POLICY_DENIED" {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Fatalf("expected POLICY_DENIED for 3rd session under shared client_id, got %+v", out.Messages)
	}
}

// TestPolicyEvaluationIgnoresClientID verifies that policy decisions
// are made on the request target only, not on which client_id
// claimed the request. Two different client_ids hitting the same
// blocked target both get POLICY_DENIED; two different client_ids
// hitting an allowed target both succeed.
func TestPolicyEvaluationIgnoresClientID(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	// Default AllowAll → everything succeeds regardless of client_id.
	srv := New("server-test", sealer, testDialer(2*time.Second), nil)
	defer srv.Close()
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	openOK := func(clientID, sessID string) protocol.Envelope {
		return protocol.Envelope{
			Version:     protocol.Version{Major: 1, Minor: 1},
			ClientID:    clientID,
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeOpen, SessionID: sessID, Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
			},
		}
	}
	for _, id := range []string{"alpha", "beta", "gamma"} {
		out := roundtrip(t, ts, sealer, openOK(id, id+"-s-allow"))
		for _, m := range out.Messages {
			if m.Type == protocol.MessageTypeReset && m.Code == "POLICY_DENIED" {
				t.Fatalf("AllowAll yet client_id=%s denied: %+v", id, m)
			}
		}
	}

	// Now switch to deny-all and verify all three client_ids get
	// the same decision (= POLICY_DENIED) — proving identity does
	// not enter policy. Use distinct session_ids so the prior
	// AllowAll-phase sessions don't confuse the test (SESSION_EXISTS
	// would otherwise mask POLICY_DENIED on a re-open).
	srv.SetPolicy(denyAll{})
	for _, id := range []string{"alpha", "beta", "gamma"} {
		out := roundtrip(t, ts, sealer, openOK(id, id+"-s-deny"))
		var sawDeny bool
		for _, m := range out.Messages {
			if m.Type == protocol.MessageTypeReset && m.Code == "POLICY_DENIED" {
				sawDeny = true
			}
		}
		if !sawDeny {
			t.Fatalf("denyAll yet client_id=%s allowed: %+v", id, out.Messages)
		}
	}
}
