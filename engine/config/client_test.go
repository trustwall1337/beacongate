package config

import (
	"bytes"
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
  "transport": { "type": "https" }
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
	body := []byte(`{"client_id":"c","listen_addr":"127.0.0.1:1080","server":{"url":"x","key":"abc"},"transport":{"type":"https"}}`)
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

// TestExampleConfigsLoad verifies the two repo-root example files
// round-trip through LoadClient. Catches drift between the JSON
// schema, example files, and Validate rules — historically the most
// common breakage point during transport-level changes.
func TestExampleConfigsLoad(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		path string
	}{
		{"https example", "../../client_config.example.json"},
		{"appsscript example", "../../client_config.appsscript.example.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read %s: %v", tc.path, err)
			}
			// Replace placeholder key with a real one so Validate's
			// key-decode passes; we're only testing schema/Validate
			// round-trip, not key correctness.
			key, err := crypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.ReplaceAll(raw, []byte("REPLACE_ME_WITH_BASE64_32_BYTE_KEY=="), []byte(EncodeKey(key)))
			tmp := writeFile(t, body)
			if _, err := LoadClient(tmp); err != nil {
				t.Fatalf("%s: LoadClient failed: %v", tc.path, err)
			}
		})
	}
}

// TestValidateAppsScriptRequiresEmptyServerURL pins the A8 plan rule:
// in appsscript mode, server.url MUST be empty/omitted. Both "set
// server.url" and "missing script_keys" must fail validation.
func TestValidateAppsScriptRequiresEmptyServerURL(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	enc := EncodeKey(key)

	// Reject: appsscript + non-empty server.url
	c := &ClientConfig{
		ClientID:   "c",
		ListenAddr: "127.0.0.1:1080",
		Server:     ClientServerConfig{URL: "https://relay.example.com/tunnel", Key: enc},
		Transport:  ClientTransportConfig{Type: "appsscript", Options: map[string]any{"script_keys": "ID"}},
	}
	if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for appsscript+server.url, got %v", err)
	}

	// Reject: appsscript + missing script_keys
	c = &ClientConfig{
		ClientID:   "c",
		ListenAddr: "127.0.0.1:1080",
		Server:     ClientServerConfig{Key: enc},
		Transport:  ClientTransportConfig{Type: "appsscript"},
	}
	if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for appsscript without script_keys, got %v", err)
	}

	// Accept: appsscript + empty server.url + script_keys present
	c = &ClientConfig{
		ClientID:   "c",
		ListenAddr: "127.0.0.1:1080",
		Server:     ClientServerConfig{Key: enc},
		Transport:  ClientTransportConfig{Type: "appsscript", Options: map[string]any{"script_keys": "ID1,ID2"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error for valid appsscript config: %v", err)
	}

	// Accept: https + non-empty server.url
	c = &ClientConfig{
		ClientID:   "c",
		ListenAddr: "127.0.0.1:1080",
		Server:     ClientServerConfig{URL: "https://relay.example.com/tunnel", Key: enc},
		Transport:  ClientTransportConfig{Type: "https"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error for valid https config: %v", err)
	}

	// Reject: https + missing server.url
	c = &ClientConfig{
		ClientID:   "c",
		ListenAddr: "127.0.0.1:1080",
		Server:     ClientServerConfig{Key: enc},
		Transport:  ClientTransportConfig{Type: "https"},
	}
	if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for https without server.url, got %v", err)
	}
}
