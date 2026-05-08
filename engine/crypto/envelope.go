// Package crypto wraps a BeaconGate plaintext batch with an authenticated
// encryption envelope. The transport layer carries only the opaque encrypted
// bytes produced here.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const (
	KeySize   = 32
	NonceSize = 12
)

var (
	ErrInvalidKey         = errors.New("crypto: invalid key length")
	ErrCiphertextTooSmall = errors.New("crypto: ciphertext too small")
	ErrAuthenticationFail = errors.New("crypto: authentication failed")
)

type Sealer struct {
	aead cipher.AEAD
	rng  io.Reader
}

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: want %d bytes, got %d", ErrInvalidKey, KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if aead.NonceSize() != NonceSize {
		return nil, fmt.Errorf("crypto: unexpected nonce size %d", aead.NonceSize())
	}
	return &Sealer{aead: aead, rng: rand.Reader}, nil
}

// Seal authenticates and encrypts plaintext into a self-contained envelope.
// Wire layout: nonce || ciphertext-with-tag.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(s.rng, nonce); err != nil {
		return nil, err
	}
	ct := s.aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open verifies and decrypts the envelope produced by Seal. A wrong key,
// modified ciphertext, or short payload all return an authentication error.
func (s *Sealer) Open(envelope []byte) ([]byte, error) {
	if len(envelope) < NonceSize+s.aead.Overhead() {
		return nil, fmt.Errorf("%w: %d bytes", ErrCiphertextTooSmall, len(envelope))
	}
	nonce := envelope[:NonceSize]
	ct := envelope[NonceSize:]
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthenticationFail, err)
	}
	return pt, nil
}

// GenerateKey returns a freshly random 32-byte key suitable for NewSealer.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	return key, nil
}
