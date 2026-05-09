package runtime

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
)

// Multi-tenant integration tests for the per-friend allowlist
// (engine/crypto.SealerRegistry + tunnel_handler.go peek+lookup).
// These tests pin the security contract of the per-friend revocation
// model: each friend's master key authenticates ONLY their own
// client_id, and an unknown client_id is rejected before any AEAD
// work.
//
// Properties enforced:
//
//  1. Two friends with independent master keys can both connect
//     concurrently — they are isolated from each other.
//  2. A friend's wire packet sent to the server with another
//     friend's client_id stamped on it is rejected (the AEAD-bound
//     cleartext id forces a key-mismatch on Open).
//  3. A client_id that's not in the allowlist is rejected with the
//     same 401 status as an AEAD failure (no fingerprintable signal
//     distinguishes "wrong key" from "not in allowlist").
//  4. Legacy single-key mode (ServerConfig.Key set, Clients empty)
//     is unchanged — already covered by every existing test in
//     this package, but we add an explicit pin here so the property
//     is documented as a contract.

// newMultiTenantServer builds a server with two registered friends
// (mahdiSealer + saraSealer), returning their per-friend Sealers so
// tests can encrypt + decrypt as each friend.
func newMultiTenantServer(t *testing.T) (ts *httptest.Server, mahdi, sara *crypto.Sealer, cleanup func()) {
	t.Helper()
	mahdiKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	saraKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := crypto.NewMultiKeyRegistry([]crypto.MultiKeyEntry{
		{ClientID: "mahdi", MasterKey: mahdiKey},
		{ClientID: "sara", MasterKey: saraKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	mahdi, err = crypto.NewSealer(mahdiKey)
	if err != nil {
		t.Fatal(err)
	}
	sara, err = crypto.NewSealer(saraKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := New("server-multi", registry, testDialer(2*time.Second), nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts = httptest.NewServer(mux)
	cleanup = func() {
		ts.Close()
		_ = srv.Close()
	}
	return ts, mahdi, sara, cleanup
}

// TestMultiTenantTwoFriendsCanConnectIndependently verifies the
// happy path: two friends with their own master keys both
// successfully open sessions. The server routes their packets to
// the right Sealer via PeekClientID + Lookup, and each friend's
// Open succeeds because the wire packet was sealed under their own
// master key.
func TestMultiTenantTwoFriendsCanConnectIndependently(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	ts, mahdiSealer, saraSealer, cleanup := newMultiTenantServer(t)
	defer cleanup()

	openMsg := func(clientID, sessID string) protocol.Envelope {
		return protocol.Envelope{
			Version:     protocol.Version{Major: 1, Minor: 1},
			ClientID:    clientID,
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeOpen, SessionID: sessID, Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
			},
		}
	}

	out := roundtrip(t, ts, mahdiSealer, openMsg("mahdi", "mahdi-s1"))
	for _, m := range out.Messages {
		if m.Type == protocol.MessageTypeReset {
			t.Fatalf("mahdi's open was rejected: %+v", m)
		}
	}

	out = roundtrip(t, ts, saraSealer, openMsg("sara", "sara-s1"))
	for _, m := range out.Messages {
		if m.Type == protocol.MessageTypeReset {
			t.Fatalf("sara's open was rejected: %+v", m)
		}
	}
}

// TestMultiTenantRejectsCrossFriendKeyImpersonation verifies the
// per-friend isolation guarantee. Mahdi seals a request under his
// master key but stamps the cleartext id "sara" on the wire — the
// peek routes the packet to Sara's Sealer, but Sara's key cannot
// open Mahdi's AEAD. The handler returns 401.
//
// This is the test that proves a leaked friend's key cannot be
// repurposed to impersonate another friend.
func TestMultiTenantRejectsCrossFriendKeyImpersonation(t *testing.T) {
	ts, mahdiSealer, _, cleanup := newMultiTenantServer(t)
	defer cleanup()

	env := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "sara", // claiming Sara's id
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeProbe, ProbeID: "x"},
		},
	}
	plain, err := protocol.EncodeEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	// Seal under Mahdi's key but stamp "sara" on the wire.
	wire, err := mahdiSealer.Seal("sara", plain)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/tunnel", "application/octet-stream", bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-friend impersonation must return 401, got %d: %s", resp.StatusCode, body)
	}
}

// TestMultiTenantRejectsUnknownClient verifies that a client_id not
// in the allowlist is rejected with 401 — and crucially, with the
// SAME status code as an AEAD failure. A network observer probing
// the tunnel endpoint cannot distinguish "is this client_id
// registered?" from "did this packet authenticate?". This denies
// the attacker an enumeration oracle.
func TestMultiTenantRejectsUnknownClient(t *testing.T) {
	ts, _, _, cleanup := newMultiTenantServer(t)
	defer cleanup()

	// An attacker's own valid Sealer with a fresh master key but
	// using a client_id that's not in the allowlist.
	rogueKey, _ := crypto.GenerateKey()
	rogueSealer, _ := crypto.NewSealer(rogueKey)
	env := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    "ghost",
		Compression: protocol.CompressionNone,
		Messages: []protocol.Message{
			{Type: protocol.MessageTypeProbe, ProbeID: "x"},
		},
	}
	plain, _ := protocol.EncodeEnvelope(env)
	wire, _ := rogueSealer.Seal("ghost", plain)
	resp, err := http.Post(ts.URL+"/tunnel", "application/octet-stream", bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unknown client_id must return 401 (same as AEAD fail), got %d: %s", resp.StatusCode, body)
	}
}

// TestMultiTenantStatusCodeUniformity pins the security property
// that ALL failure modes return the same 401 status. This is what
// makes the auth gate non-fingerprintable. We exercise three
// failure modes back-to-back and verify the status code is
// identical for each:
//
//  1. Bad wire version (PeekClientID fails)
//  2. Unknown client_id (Lookup fails)
//  3. Cross-friend impersonation (Open fails)
//
// If any of these returned a different code (400, 403, etc.) a
// scanner could distinguish them.
func TestMultiTenantStatusCodeUniformity(t *testing.T) {
	ts, mahdiSealer, _, cleanup := newMultiTenantServer(t)
	defer cleanup()

	tcs := []struct {
		name string
		body []byte
	}{
		{
			name: "bad wire version",
			body: []byte{0xEE, 0x00, 0x05, 'a', 'l', 'p', 'h', 'a',
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 12 nonce
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 16 tag
			},
		},
		{
			name: "unknown client",
			body: func() []byte {
				rogueKey, _ := crypto.GenerateKey()
				rogue, _ := crypto.NewSealer(rogueKey)
				w, _ := rogue.Seal("ghost", []byte("plain"))
				return w
			}(),
		},
		{
			name: "cross-friend impersonation",
			body: func() []byte {
				w, _ := mahdiSealer.Seal("sara", []byte("plain"))
				return w
			}(),
		},
	}
	for _, tc := range tcs {
		resp, err := http.Post(ts.URL+"/tunnel", "application/octet-stream", bytes.NewReader(tc.body))
		if err != nil {
			t.Fatalf("%s: post: %v", tc.name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: must return 401 (uniformity), got %d", tc.name, resp.StatusCode)
		}
	}
}

// TestSingleTenantLegacyModeUnchanged pins the back-compat
// guarantee: a server built with the legacy single-key registry
// behaves exactly as before — any client_id authenticates, and the
// inbound + outbound AEAD round-trips work normally. This is the
// configuration every desktop / dev / pre-Phase-1 deployment uses,
// so a regression here would silently break those.
func TestSingleTenantLegacyModeUnchanged(t *testing.T) {
	host, port, stop := startEchoUpstream(t)
	defer stop()
	sealer := newSealer(t)
	srv := New("server-legacy", crypto.SingleKeyRegistryFromSealer(sealer), testDialer(2*time.Second), nil)
	defer srv.Close()
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Two distinct client_ids — both should succeed in single-key
	// mode because the registry returns the shared Sealer for any
	// id.
	for _, id := range []string{"client-a", "client-b"} {
		out := roundtrip(t, ts, sealer, protocol.Envelope{
			Version:     protocol.Version{Major: 1, Minor: 1},
			ClientID:    id,
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeOpen, SessionID: id + "-s1", Target: &protocol.Target{Network: "tcp", Host: host, Port: port}},
			},
		})
		for _, m := range out.Messages {
			if m.Type == protocol.MessageTypeReset {
				t.Fatalf("legacy %s rejected: %+v", id, m)
			}
		}
	}
}
