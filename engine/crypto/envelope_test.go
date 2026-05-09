// it lives at engine/crypto to mirror Go's package layout convention
// and is imported as the local AEAD wrapper, never alongside stdlib
// crypto. Renaming would touch every consumer.
//
//nolint:revive // var-naming: package shadows stdlib "crypto" by design;
package crypto

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

const testClientID = "client-alpha"

func newTestSealer(t *testing.T) *Sealer {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return s
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := newTestSealer(t)
	plaintext := []byte("beacongate batch payload")
	wire, err := s.Seal(testClientID, plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := s.Open(wire)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got.Plaintext, plaintext) {
		t.Fatalf("plaintext mismatch: %q != %q", got.Plaintext, plaintext)
	}
	if got.ClientID != testClientID {
		t.Fatalf("client_id mismatch: %q != %q", got.ClientID, testClientID)
	}
	if got.ReplayID == ([ReplayIDSize]byte{}) {
		t.Fatalf("replay-id should be random, got all-zero")
	}
	if time.Since(got.Timestamp) > time.Minute {
		t.Fatalf("timestamp too old: %s", got.Timestamp)
	}
}

func TestSealProducesUniqueWireBytes(t *testing.T) {
	s := newTestSealer(t)
	a, err := s.Seal(testClientID, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Seal(testClientID, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("expected unique wire bytes due to random nonce + replay-id")
	}
}

func TestSealProducesUniqueReplayIDs(t *testing.T) {
	s := newTestSealer(t)
	const N = 20
	seen := make(map[[ReplayIDSize]byte]bool, N)
	for i := 0; i < N; i++ {
		wire, err := s.Seal(testClientID, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		batch, err := s.Open(wire)
		if err != nil {
			t.Fatal(err)
		}
		if seen[batch.ReplayID] {
			t.Fatalf("duplicate replay-id at iteration %d", i)
		}
		seen[batch.ReplayID] = true
	}
}

func TestOpenRejectsWrongMasterKey(t *testing.T) {
	s1 := newTestSealer(t)
	s2 := newTestSealer(t)
	wire, err := s1.Seal(testClientID, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Open(wire); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("expected auth fail, got %v", err)
	}
}

// TestOpenRejectsAADTamperedClientID is the load-bearing test for the
// AAD binding (plan B1). A captured wire packet whose cleartext
// client_id is swapped for a different name MUST fail authentication.
func TestOpenRejectsAADTamperedClientID(t *testing.T) {
	s := newTestSealer(t)
	wire, err := s.Seal("client-alpha", []byte("secret-payload"))
	if err != nil {
		t.Fatal(err)
	}
	const same = "client-omega"
	if len(same) != len("client-alpha") {
		t.Fatal("test setup: replacement must be same length")
	}
	off := 1 + ClientIDLenSize
	for i := range same {
		wire[off+i] = same[i]
	}
	if _, err := s.Open(wire); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("expected auth fail on AAD-tampered client_id, got %v", err)
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	s := newTestSealer(t)
	wire, err := s.Seal(testClientID, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	wire[len(wire)-1] ^= 0x01
	if _, err := s.Open(wire); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("expected auth fail on tamper, got %v", err)
	}
}

func TestOpenRejectsShortHeader(t *testing.T) {
	s := newTestSealer(t)
	if _, err := s.Open(nil); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on nil, got %v", err)
	}
	if _, err := s.Open([]byte("short")); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on short, got %v", err)
	}
}

func TestOpenRejectsUnsupportedWireVersion(t *testing.T) {
	s := newTestSealer(t)
	wire, err := s.Seal(testClientID, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	wire[0] = 0xFF
	if _, err := s.Open(wire); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on unknown wire-version, got %v", err)
	}
}

func TestOpenRejectsOversizedClientIDLen(t *testing.T) {
	s := newTestSealer(t)
	wire := make([]byte, 4)
	wire[0] = WireVersionV11
	wire[1], wire[2] = 0xFF, 0xFF
	if _, err := s.Open(wire); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on oversized client_id len, got %v", err)
	}
}

func TestOpenRejectsZeroClientIDLen(t *testing.T) {
	s := newTestSealer(t)
	wire := make([]byte, 4)
	wire[0] = WireVersionV11
	wire[1], wire[2] = 0x00, 0x00
	if _, err := s.Open(wire); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on zero client_id len, got %v", err)
	}
}

func TestPerClientKeysAreIndependent(t *testing.T) {
	s := newTestSealer(t)
	wireA, err := s.Seal("client-alpha", []byte("alpha-secret"))
	if err != nil {
		t.Fatal(err)
	}
	wireB, err := s.Seal("client-beta", []byte("beta-secret"))
	if err != nil {
		t.Fatal(err)
	}
	openA, err := s.Open(wireA)
	if err != nil || openA.ClientID != "client-alpha" {
		t.Fatalf("alpha round-trip failed: err=%v id=%q", err, openA.ClientID)
	}
	openB, err := s.Open(wireB)
	if err != nil || openB.ClientID != "client-beta" {
		t.Fatalf("beta round-trip failed: err=%v id=%q", err, openB.ClientID)
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	if _, err := NewSealer(make([]byte, 16)); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected invalid key, got %v", err)
	}
	if _, err := NewSealer(nil); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected invalid key, got %v", err)
	}
}

func TestSealRejectsEmptyClientID(t *testing.T) {
	s := newTestSealer(t)
	if _, err := s.Seal("", []byte("x")); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on empty client_id, got %v", err)
	}
}

func TestSealRejectsOversizedClientID(t *testing.T) {
	s := newTestSealer(t)
	id := string(make([]byte, MaxClientIDLen+1))
	if _, err := s.Seal(id, []byte("x")); !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on oversized client_id, got %v", err)
	}
}

func TestKeyCacheReusesDerivation(t *testing.T) {
	s := newTestSealer(t)
	first, err := s.derivePerClientAEAD("client-x")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.derivePerClientAEAD("client-x")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("expected cached AEAD reuse for same client_id, got distinct pointers")
	}
	other, err := s.derivePerClientAEAD("client-y")
	if err != nil {
		t.Fatal(err)
	}
	if first == other {
		t.Fatalf("expected distinct AEADs for distinct client_ids")
	}
}

// --- PeekClientID tests ---

func TestPeekClientIDHappyPath(t *testing.T) {
	s := newTestSealer(t)
	wire, err := s.Seal("client-alpha", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := PeekClientID(wire)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if got != "client-alpha" {
		t.Fatalf("client_id mismatch: got %q want %q", got, "client-alpha")
	}
}

// TestPeekClientIDDoesNotValidateAEAD pins the documented contract:
// PeekClientID is a fast routing primitive and MUST NOT do AEAD work.
// We construct a wire packet whose ciphertext bytes have been mangled
// — Open() would reject this with ErrAuthenticationFail, but Peek
// must still return the cleartext client_id so the caller can route
// to the right Sealer (whose Open() then rejects authentically).
func TestPeekClientIDDoesNotValidateAEAD(t *testing.T) {
	s := newTestSealer(t)
	wire, err := s.Seal("client-beta", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	// Mangle the last ciphertext byte. Header + cleartext id are
	// untouched; AEAD verification will fail but Peek should not care.
	wire[len(wire)-1] ^= 0xFF
	got, err := PeekClientID(wire)
	if err != nil {
		t.Fatalf("peek should ignore ciphertext tampering, got err: %v", err)
	}
	if got != "client-beta" {
		t.Fatalf("client_id mismatch: got %q want %q", got, "client-beta")
	}
	// Sanity: Open MUST reject the same packet.
	if _, err := s.Open(wire); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("Open should reject tampered ct, got %v", err)
	}
}

func TestPeekClientIDRejectsShortHeader(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		_, err := PeekClientID(make([]byte, n))
		if !errors.Is(err, ErrInvalidWire) {
			t.Fatalf("len=%d: expected ErrInvalidWire, got %v", n, err)
		}
	}
}

func TestPeekClientIDRejectsUnsupportedVersion(t *testing.T) {
	wire := []byte{0xEE, 0x00, 0x05, 'a', 'l', 'p', 'h', 'a'}
	_, err := PeekClientID(wire)
	if !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on bad version, got %v", err)
	}
}

func TestPeekClientIDRejectsEmptyClientID(t *testing.T) {
	wire := make([]byte, 4)
	wire[0] = WireVersionV11
	// idLen 0x0000 → empty id
	_, err := PeekClientID(wire)
	if !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on empty client_id, got %v", err)
	}
}

func TestPeekClientIDRejectsOversizedClientIDLen(t *testing.T) {
	wire := make([]byte, 4)
	wire[0] = WireVersionV11
	wire[1], wire[2] = 0xFF, 0xFF
	_, err := PeekClientID(wire)
	if !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on oversized id len, got %v", err)
	}
}

// TestPeekClientIDRejectsTruncatedAfterID covers the case where the
// header claims an id length that overruns the available bytes
// before the nonce. A v1.1 packet must include 12 nonce bytes after
// the cleartext id; truncation there means the packet is malformed.
func TestPeekClientIDRejectsTruncatedAfterID(t *testing.T) {
	// 1 ver + 2 idLen + 5 id (no nonce, no ct) = 8 bytes, not enough
	wire := []byte{WireVersionV11, 0x00, 0x05, 'a', 'l', 'p', 'h', 'a'}
	_, err := PeekClientID(wire)
	if !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("expected ErrInvalidWire on truncated header, got %v", err)
	}
}

// BenchmarkPeekClientID measures the fast-path routing cost. In
// multi-tenant mode this runs once per inbound /tunnel POST before
// any AEAD work; the cost must be negligible vs the existing per-IP
// rate limit and replay store lookups (each ~hundreds of ns).
func BenchmarkPeekClientID(b *testing.B) {
	s, _ := NewSealer(make([]byte, MasterKeySize))
	wire, _ := s.Seal("client-alpha", []byte("payload bytes"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PeekClientID(wire); err != nil {
			b.Fatal(err)
		}
	}
}
