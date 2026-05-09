// Package crypto wraps a BeaconGate plaintext batch in the v1.1 wire
// envelope: a cleartext header carrying the wire-version byte and the
// client_id, followed by an AEAD ciphertext sealed under a per-client
// key derived via HKDF-SHA256 from the operator's master key.
//
// Wire layout (v1.1):
//
//	[ 1 byte ]   wire version, currently 0x01
//	[ 2 BE  ]   client_id length N (max 1024)
//	[ N     ]   client_id (UTF-8, no NUL)
//	[ 12    ]   AEAD nonce (random per Seal call)
//	[ rest  ]   AEAD ciphertext + 16-byte tag, where:
//	             AAD       = wire_version || client_id_len_be || client_id
//	             plaintext = [8 BE timestamp_ms] || [16 replay_id] || JSON envelope
//
// Properties this layout buys (per plan B1):
//
//   - **Per-client AEAD keys** (HKDF salt "beacongate v1.1 per-client",
//     info = client_id). Compromise of one client's derived key does
//     NOT expose other clients' traffic.
//   - **AAD-bound client_id**: a captured wire packet with its
//     cleartext client_id swapped fails AEAD authentication.
//   - **Replay protection** via the inner 8-byte timestamp + 16-byte
//     replay-id. The server-side replay store (engine/crypto/replay.go)
//     consumes these.
//
// Hard-cut from v1.0: there is no v1.0 fallback. Any wire packet that
// does not start with WireVersionV11 (0x01) is rejected.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// MasterKeySize is the length of the operator's master key
	// (base64-encoded in client/server config). Per-client keys
	// derived from it are also 32 bytes (SHA-256 output truncated to
	// the AEAD key size).
	MasterKeySize = 32

	// AEADNonceSize is the 12-byte AES-GCM nonce.
	AEADNonceSize = 12

	// WireVersionV11 is the byte value the v1.1 wire format puts at
	// offset 0. Future wire-format bumps allocate a new byte value
	// here; the application-protocol version (1.x in the JSON
	// envelope) advances independently. See engine/protocol/version.go.
	WireVersionV11 byte = 0x01

	// ClientIDLenSize is the byte width of the cleartext client_id
	// length field (uint16 BE).
	ClientIDLenSize = 2

	// MaxClientIDLen caps the cleartext client_id length so a
	// malicious peer cannot force the receiver to allocate large
	// buffers before the AEAD check rejects the packet. 1 KiB is
	// generous; real client_ids are short stable strings.
	MaxClientIDLen = 1024

	// TimestampSize is the byte width of the inner timestamp
	// (uint64 BE, milliseconds since Unix epoch).
	TimestampSize = 8

	// ReplayIDSize is the byte width of the inner replay-id
	// (16 random bytes per Seal call). Used by the server's replay
	// dedup cache (replay.go).
	ReplayIDSize = 16

	// hkdfSalt and hkdfInfoPrefix are constants in the per-client
	// key derivation. Salt is fixed across clients; info varies per
	// client_id so two clients with the same master key get
	// cryptographically independent AEAD keys. Bumping these strings
	// is a wire change.
	hkdfSalt       = "beacongate v1.1 per-client"
	hkdfInfoPrefix = ""
)

var (
	// ErrInvalidKey indicates the master key is the wrong size or
	// the AEAD construction failed.
	ErrInvalidKey = errors.New("crypto: invalid master key")
	// ErrInvalidWire indicates the wire bytes don't parse as a
	// v1.1 envelope (truncated, wrong version byte, oversized
	// client_id, etc.).
	ErrInvalidWire = errors.New("crypto: invalid wire format")
	// ErrAuthenticationFail indicates the AEAD tag did not verify.
	// The most common cause is a wrong key; second most common is
	// a captured packet replayed against a different client_id (the
	// AAD binding catches this).
	ErrAuthenticationFail = errors.New("crypto: authentication failed")
)

// SealedBatch is the result of opening a v1.1 wire packet. The
// caller (the server tunnel handler) feeds Timestamp and ReplayID
// into the replay store before processing the JSON envelope.
type SealedBatch struct {
	ClientID  string
	Plaintext []byte
	Timestamp time.Time
	ReplayID  [ReplayIDSize]byte
}

// Sealer holds the master key and a key-derivation cache.
//
// The cache is intentionally small and bounded so a flood of
// distinct client_ids cannot fill memory. Cache misses re-derive at
// the cost of one HKDF-SHA256 call (microseconds); cache hits skip
// HKDF entirely.
type Sealer struct {
	masterKey []byte // copy of input, owned by Sealer
	rng       io.Reader

	keyCache *perClientKeyCache
}

// NewSealer returns a v1.1 Sealer initialized with masterKey.
// masterKey MUST be exactly MasterKeySize bytes; shorter or longer
// is a configuration error.
func NewSealer(masterKey []byte) (*Sealer, error) {
	if len(masterKey) != MasterKeySize {
		return nil, fmt.Errorf("%w: want %d bytes, got %d", ErrInvalidKey, MasterKeySize, len(masterKey))
	}
	out := make([]byte, len(masterKey))
	copy(out, masterKey)
	return &Sealer{
		masterKey: out,
		rng:       rand.Reader,
		keyCache:  newPerClientKeyCache(64),
	}, nil
}

// derivePerClientAEAD returns the AEAD constructed with the
// HKDF-derived per-client key. Cached in the Sealer so repeated
// Seal/Open calls for the same client_id don't re-run HKDF.
func (s *Sealer) derivePerClientAEAD(clientID string) (cipher.AEAD, error) {
	if cached := s.keyCache.get(clientID); cached != nil {
		return cached, nil
	}
	derived, err := hkdf.Key(sha256.New, s.masterKey, []byte(hkdfSalt), hkdfInfoPrefix+clientID, MasterKeySize)
	if err != nil {
		return nil, fmt.Errorf("%w: hkdf: %v", ErrInvalidKey, err)
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, fmt.Errorf("%w: aes: %v", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm: %v", ErrInvalidKey, err)
	}
	if aead.NonceSize() != AEADNonceSize {
		return nil, fmt.Errorf("%w: unexpected nonce size %d", ErrInvalidKey, aead.NonceSize())
	}
	s.keyCache.put(clientID, aead)
	return aead, nil
}

// Seal wraps plaintext in a v1.1 wire envelope authenticated under
// clientID's per-client key. Returns the full wire bytes ready to
// hand to a transport.
//
// Each call mints a fresh AEAD nonce (12 random bytes) and a fresh
// replay-id (16 random bytes). The current wall-clock timestamp is
// stamped into the inner header so the server can reject stale
// packets outside the ±5min window.
func (s *Sealer) Seal(clientID string, plaintext []byte) ([]byte, error) {
	if clientID == "" {
		return nil, fmt.Errorf("%w: client_id required", ErrInvalidWire)
	}
	if len(clientID) > MaxClientIDLen {
		return nil, fmt.Errorf("%w: client_id too long (%d > %d)", ErrInvalidWire, len(clientID), MaxClientIDLen)
	}
	aead, err := s.derivePerClientAEAD(clientID)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, AEADNonceSize)
	if _, err := io.ReadFull(s.rng, nonce); err != nil {
		return nil, fmt.Errorf("%w: nonce read: %v", ErrInvalidWire, err)
	}

	var replayID [ReplayIDSize]byte
	if _, err := io.ReadFull(s.rng, replayID[:]); err != nil {
		return nil, fmt.Errorf("%w: replay-id read: %v", ErrInvalidWire, err)
	}

	// Inner plaintext layout: [8 BE timestamp_ms][16 replay_id][envelope].
	innerLen := TimestampSize + ReplayIDSize + len(plaintext)
	inner := make([]byte, 0, innerLen)
	tsBuf := make([]byte, TimestampSize)
	binary.BigEndian.PutUint64(tsBuf, uint64(time.Now().UnixMilli()))
	inner = append(inner, tsBuf...)
	inner = append(inner, replayID[:]...)
	inner = append(inner, plaintext...)

	aad := buildAAD(WireVersionV11, clientID)
	ct := aead.Seal(nil, nonce, inner, aad)

	// Outer wire layout: [1 ver][2 BE id_len][N id][12 nonce][ct+tag].
	headerLen := 1 + ClientIDLenSize + len(clientID) + AEADNonceSize
	out := make([]byte, 0, headerLen+len(ct))
	out = append(out, WireVersionV11)
	idLen := make([]byte, ClientIDLenSize)
	binary.BigEndian.PutUint16(idLen, uint16(len(clientID)))
	out = append(out, idLen...)
	out = append(out, clientID...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open inverts Seal. It parses the cleartext header to recover
// the client_id (so the server can derive the per-client key
// before AEAD-opening), then verifies and decrypts the inner
// payload. Both the wire-version byte and the client_id are
// covered by the AAD, so a captured packet posted with a
// modified client_id fails authentication.
func (s *Sealer) Open(wire []byte) (*SealedBatch, error) {
	if len(wire) < 1+ClientIDLenSize {
		return nil, fmt.Errorf("%w: short header (%d bytes)", ErrInvalidWire, len(wire))
	}
	if wire[0] != WireVersionV11 {
		return nil, fmt.Errorf("%w: unsupported wire version 0x%02x", ErrInvalidWire, wire[0])
	}
	idLen := int(binary.BigEndian.Uint16(wire[1 : 1+ClientIDLenSize]))
	if idLen == 0 {
		return nil, fmt.Errorf("%w: client_id empty", ErrInvalidWire)
	}
	if idLen > MaxClientIDLen {
		return nil, fmt.Errorf("%w: client_id too long (%d > %d)", ErrInvalidWire, idLen, MaxClientIDLen)
	}
	headerEnd := 1 + ClientIDLenSize + idLen
	if len(wire) < headerEnd+AEADNonceSize {
		return nil, fmt.Errorf("%w: short header for client_id len %d", ErrInvalidWire, idLen)
	}
	clientID := string(wire[1+ClientIDLenSize : headerEnd])
	nonce := wire[headerEnd : headerEnd+AEADNonceSize]
	ct := wire[headerEnd+AEADNonceSize:]

	aead, err := s.derivePerClientAEAD(clientID)
	if err != nil {
		return nil, err
	}
	if len(ct) < aead.Overhead() {
		return nil, fmt.Errorf("%w: ciphertext too short", ErrInvalidWire)
	}

	aad := buildAAD(WireVersionV11, clientID)
	inner, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		// Generic message to the caller — never leak whether the
		// failure was wrong key, modified client_id, or modified
		// ciphertext, because a network attacker can use that
		// distinction to fingerprint the deployment.
		return nil, fmt.Errorf("%w: %v", ErrAuthenticationFail, err)
	}
	if len(inner) < TimestampSize+ReplayIDSize {
		return nil, fmt.Errorf("%w: inner payload too short", ErrInvalidWire)
	}

	tsMs := binary.BigEndian.Uint64(inner[:TimestampSize])
	var replayID [ReplayIDSize]byte
	copy(replayID[:], inner[TimestampSize:TimestampSize+ReplayIDSize])
	plaintext := inner[TimestampSize+ReplayIDSize:]

	return &SealedBatch{
		ClientID:  clientID,
		Plaintext: plaintext,
		Timestamp: time.UnixMilli(int64(tsMs)),
		ReplayID:  replayID,
	}, nil
}

// buildAAD constructs the AAD that binds wire version + client_id
// to the AEAD. The exact byte layout MUST match between Seal and
// Open or authentication breaks; the assert here documents the
// contract.
func buildAAD(wireVersion byte, clientID string) []byte {
	aad := make([]byte, 0, 1+ClientIDLenSize+len(clientID))
	aad = append(aad, wireVersion)
	idLen := make([]byte, ClientIDLenSize)
	binary.BigEndian.PutUint16(idLen, uint16(len(clientID)))
	aad = append(aad, idLen...)
	aad = append(aad, clientID...)
	return aad
}

// GenerateKey returns a freshly random 32-byte master key suitable
// for NewSealer.
func GenerateKey() ([]byte, error) {
	key := make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// --- back-compat shims for existing call sites that don't yet pass client_id ---

// KeySize is retained as the public name for back-compat; it equals
// MasterKeySize.
const KeySize = MasterKeySize

// NonceSize is retained for callers that referenced the old constant
// (used by tests for v1.0 byte counting). New code should not use
// it; the wire layout owns its sizing.
const NonceSize = AEADNonceSize

// ErrCiphertextTooSmall is retained for callers that errors.Is-checked
// the v1.0 sentinel. New code should errors.Is against ErrInvalidWire.
var ErrCiphertextTooSmall = ErrInvalidWire
