package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/trustwall1337/beacongate/engine/crypto"
)

func validClientJSON(t *testing.T) []byte {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{
  "client_id": "client-alpha",
  "listen_addr": "127.0.0.1:1080",
  "server": { "url": "https://example.com/", "key": "` + EncodeKey(key) + `" },
  "transport": { "type": "google" }
}`)
}

func writeFile(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadClientHappyPath(t *testing.T) {
	path := writeFile(t, validClientJSON(t))
	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ClientID != "client-alpha" {
		t.Fatalf("client_id mismatch")
	}
	key, err := cfg.ServerKeyBytes()
	if err != nil || len(key) != crypto.KeySize {
		t.Fatalf("key decode: %v len=%d", err, len(key))
	}
}

func TestLoadClientRejectsBadKey(t *testing.T) {
	body := []byte(`{"client_id":"c","listen_addr":"127.0.0.1:1080","server":{"url":"x","key":"abc"},"transport":{"type":"google"}}`)
	_, err := LoadClient(writeFile(t, body))
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestLoadClientRejectsMissingFields(t *testing.T) {
	body := []byte(`{}`)
	_, err := LoadClient(writeFile(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestLoadClientRejectsUnknownField(t *testing.T) {
	body := []byte(`{"client_id":"c","listen_addr":"x","server":{"url":"y","key":"z"},"transport":{"type":"t"},"wat":1}`)
	_, err := LoadClient(writeFile(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}
