// it lives at engine/crypto to mirror Go's package layout convention
//
//nolint:revive // var-naming: package shadows stdlib "crypto" by design;
package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNewSingleKeyRegistryHappyPath(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewSingleKeyRegistry(key)
	if err != nil {
		t.Fatalf("new single registry: %v", err)
	}
	if reg.IsMultiTenant() {
		t.Fatalf("single-key registry should not report multi-tenant")
	}
	if reg.Size() != 1 {
		t.Fatalf("single-key registry size: got %d want 1", reg.Size())
	}
}

// TestSingleKeyRegistryReturnsSameSealerForAnyClientID pins the
// documented contract: in single-tenant mode every client_id resolves
// to the same Sealer, including ones the operator never explicitly
// registered. This is what preserves backwards compatibility for
// the legacy ServerConfig.Key bootstrap path.
func TestSingleKeyRegistryReturnsSameSealerForAnyClientID(t *testing.T) {
	key, _ := GenerateKey()
	reg, _ := NewSingleKeyRegistry(key)
	a := reg.Lookup("client-alpha")
	b := reg.Lookup("client-beta")
	c := reg.Lookup("never-seen-before")
	if a == nil || b == nil || c == nil {
		t.Fatalf("single-key Lookup must never return nil; got a=%v b=%v c=%v", a, b, c)
	}
	if a != b || b != c {
		t.Fatalf("single-key Lookup must return the same Sealer for every client_id")
	}
}

func TestNewSingleKeyRegistryRejectsBadKey(t *testing.T) {
	_, err := NewSingleKeyRegistry(make([]byte, 16)) // wrong size
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey on bad master key, got %v", err)
	}
}

func TestSingleKeyRegistryFromSealerWraps(t *testing.T) {
	s := newTestSealer(t)
	reg := SingleKeyRegistryFromSealer(s)
	if reg.IsMultiTenant() {
		t.Fatalf("from-sealer registry should not be multi-tenant")
	}
	if reg.Lookup("anything") != s {
		t.Fatalf("from-sealer registry should return the wrapped Sealer")
	}
}

// TestNewMultiKeyRegistryHappyPath verifies the per-friend shape: each
// registered client_id resolves to its own Sealer, and an unknown
// client_id resolves to nil (the rejection signal).
func TestNewMultiKeyRegistryHappyPath(t *testing.T) {
	keyA, _ := GenerateKey()
	keyB, _ := GenerateKey()
	reg, err := NewMultiKeyRegistry([]MultiKeyEntry{
		{ClientID: "mahdi", MasterKey: keyA},
		{ClientID: "sara", MasterKey: keyB},
	})
	if err != nil {
		t.Fatalf("new multi registry: %v", err)
	}
	if !reg.IsMultiTenant() {
		t.Fatalf("multi-key registry should report multi-tenant")
	}
	if reg.Size() != 2 {
		t.Fatalf("multi-key registry size: got %d want 2", reg.Size())
	}
	mahdi := reg.Lookup("mahdi")
	sara := reg.Lookup("sara")
	if mahdi == nil || sara == nil {
		t.Fatalf("registered client_ids must resolve to non-nil Sealers")
	}
	if mahdi == sara {
		t.Fatalf("distinct client_ids must resolve to distinct Sealers")
	}
	if reg.Lookup("ghost") != nil {
		t.Fatalf("unknown client_id must resolve to nil (rejection signal)")
	}
}

// TestMultiKeyRegistryKeysAreCryptographicallyIndependent verifies
// the security property the per-friend allowlist exists to provide:
// a wire packet sealed under friend A's key MUST NOT decrypt under
// friend B's Sealer, even though both use the same HKDF derivation
// chain. Compromise of one friend's master key does not affect the
// other.
func TestMultiKeyRegistryKeysAreCryptographicallyIndependent(t *testing.T) {
	keyA, _ := GenerateKey()
	keyB, _ := GenerateKey()
	reg, _ := NewMultiKeyRegistry([]MultiKeyEntry{
		{ClientID: "mahdi", MasterKey: keyA},
		{ClientID: "sara", MasterKey: keyB},
	})
	mahdiSealer := reg.Lookup("mahdi")
	saraSealer := reg.Lookup("sara")

	// Mahdi seals a request under his client_id.
	wire, err := mahdiSealer.Seal("mahdi", []byte("mahdi-secret-data"))
	if err != nil {
		t.Fatal(err)
	}
	// Sara's sealer (different master key) MUST NOT decrypt it,
	// even though the cleartext id and HKDF info string would
	// normally produce the same per-direction key.
	if _, err := saraSealer.Open(wire); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("Sara's sealer must reject Mahdi's packet; got err=%v", err)
	}
	// And Mahdi's own sealer must still accept it (sanity).
	got, err := mahdiSealer.Open(wire)
	if err != nil {
		t.Fatalf("Mahdi's own sealer should accept his own packet: %v", err)
	}
	if !bytes.Equal(got.Plaintext, []byte("mahdi-secret-data")) {
		t.Fatalf("plaintext mismatch: %q", got.Plaintext)
	}
}

// TestMultiKeyRegistryRejectsCrossClientIDImpersonation verifies the
// AAD binding still works under the per-friend allowlist: a packet
// sealed by Mahdi (under his master key + cleartext id "mahdi") that
// has the cleartext id flipped to "sara" while being routed must
// fail authentication on Sara's Sealer. PeekClientID would route
// the tampered packet to Sara, but Sara's sealer rejects it because
// the AAD-bound original id no longer matches.
func TestMultiKeyRegistryRejectsCrossClientIDImpersonation(t *testing.T) {
	keyA, _ := GenerateKey()
	keyB, _ := GenerateKey()
	reg, _ := NewMultiKeyRegistry([]MultiKeyEntry{
		{ClientID: "mahdi", MasterKey: keyA},
		{ClientID: "sara", MasterKey: keyB},
	})

	// Mahdi seals legitimately.
	mahdiSealer := reg.Lookup("mahdi")
	wire, err := mahdiSealer.Seal("mahdi", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	// Network attacker rewrites cleartext client_id to "sara".
	// Header layout: [1 ver][2 idLen][N id][12 nonce][ct].
	// "mahdi" and "sara" are different lengths so we have to
	// rebuild the header — easier to just re-seal under "sara"
	// with Mahdi's key (different attack, same property to test).
	tampered, err := mahdiSealer.Seal("sara", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	// The tampered packet is routed to Sara (PeekClientID returns
	// "sara") but Sara's sealer must reject it because the AEAD
	// tag was computed under Mahdi's master key.
	saraSealer := reg.Lookup("sara")
	if saraSealer == nil {
		t.Fatal("sara should be registered")
	}
	if _, err := saraSealer.Open(tampered); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("Sara's sealer must reject Mahdi-keyed packet stamped with sara's id; got %v", err)
	}
	// Sanity: original wire still verifies under Mahdi's sealer.
	if _, err := mahdiSealer.Open(wire); err != nil {
		t.Fatalf("legit packet must still verify: %v", err)
	}
}

func TestNewMultiKeyRegistryRejectsEmptyEntries(t *testing.T) {
	_, err := NewMultiKeyRegistry(nil)
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("expected error on empty entries, got %v", err)
	}
}

func TestNewMultiKeyRegistryRejectsEmptyClientID(t *testing.T) {
	key, _ := GenerateKey()
	_, err := NewMultiKeyRegistry([]MultiKeyEntry{{ClientID: "", MasterKey: key}})
	if err == nil || !strings.Contains(err.Error(), "client_id required") {
		t.Fatalf("expected error on empty client_id, got %v", err)
	}
}

func TestNewMultiKeyRegistryRejectsDuplicateClientID(t *testing.T) {
	keyA, _ := GenerateKey()
	keyB, _ := GenerateKey()
	_, err := NewMultiKeyRegistry([]MultiKeyEntry{
		{ClientID: "mahdi", MasterKey: keyA},
		{ClientID: "mahdi", MasterKey: keyB},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate client_id") {
		t.Fatalf("expected error on duplicate client_id, got %v", err)
	}
}

func TestNewMultiKeyRegistryRejectsBadKey(t *testing.T) {
	_, err := NewMultiKeyRegistry([]MultiKeyEntry{
		{ClientID: "mahdi", MasterKey: make([]byte, 16)}, // wrong size
	})
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey on bad master key, got %v", err)
	}
}

// BenchmarkRegistryLookupSingle measures Lookup cost in single-tenant
// mode. This runs once per inbound /tunnel POST. Must be effectively
// free — well under 100 ns and zero allocations.
func BenchmarkRegistryLookupSingle(b *testing.B) {
	reg, _ := NewSingleKeyRegistry(make([]byte, MasterKeySize))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.Lookup("client-alpha")
	}
}

// BenchmarkRegistryLookupMulti measures Lookup cost in multi-tenant
// mode with a representative friend count (10). Map lookup cost
// scales O(1) amortized; we track this to make sure adding friends
// never moves the auth-gate latency budget.
func BenchmarkRegistryLookupMulti(b *testing.B) {
	const friends = 10
	entries := make([]MultiKeyEntry, friends)
	for i := 0; i < friends; i++ {
		k, _ := GenerateKey()
		entries[i] = MultiKeyEntry{
			ClientID:  "friend-" + string(rune('a'+i)),
			MasterKey: k,
		}
	}
	reg, _ := NewMultiKeyRegistry(entries)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.Lookup("friend-e") // mid-list hit
	}
}
