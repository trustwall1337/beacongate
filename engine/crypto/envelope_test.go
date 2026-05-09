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
