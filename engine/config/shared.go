// Package config defines the on-disk configuration model for the BeaconGate
// client and server. Configurations are JSON for portability and so they can
// be diff-reviewed during operator changes.
package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/trustwall1337/beacongate/engine/crypto"
)

var (
	ErrInvalidConfig = errors.New("config: invalid")
	ErrInvalidKey    = errors.New("config: invalid key")
)

// DecodeKey parses a base64-encoded 32-byte AEAD key.
func DecodeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: key is empty", ErrInvalidKey)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	if len(raw) != crypto.KeySize {
		return nil, fmt.Errorf("%w: want %d bytes, got %d", ErrInvalidKey, crypto.KeySize, len(raw))
	}
	return raw, nil
}

// EncodeKey is the inverse of DecodeKey.
func EncodeKey(key []byte) string { return base64.StdEncoding.EncodeToString(key) }

func loadJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}
