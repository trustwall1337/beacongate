package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func newTestSealer(t *testing.T) (*Sealer, []byte) {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return s, key
}

func TestSealOpenRoundTrip(t *testing.T) {
	s, _ := newTestSealer(t)
	plaintext := []byte("beacongate batch payload")
	ct, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := s.Open(ct)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch: %q != %q", got, plaintext)
	}
}

func TestSealProducesUniqueOutputs(t *testing.T) {
	s, _ := newTestSealer(t)
	a, err := s.Seal([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Seal([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("expected unique ciphertexts due to random nonce")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	s1, _ := newTestSealer(t)
	s2, _ := newTestSealer(t)
	ct, err := s1.Seal([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Open(ct); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("expected auth fail, got %v", err)
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	s, _ := newTestSealer(t)
	ct, err := s.Seal([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	ct[len(ct)-1] ^= 0x01
	if _, err := s.Open(ct); !errors.Is(err, ErrAuthenticationFail) {
		t.Fatalf("expected auth fail on tamper, got %v", err)
	}
}

func TestOpenRejectsShort(t *testing.T) {
	s, _ := newTestSealer(t)
	if _, err := s.Open([]byte("short")); !errors.Is(err, ErrCiphertextTooSmall) {
		t.Fatalf("expected ciphertext-too-small, got %v", err)
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
