package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/trustwall1337/beacongate/engine/crypto"
)

func writeServer(t *testing.T, body []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "server.json")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadServerHappyPath(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
  "server_id": "server-west",
  "listen_addr": ":8080",
  "key": "` + EncodeKey(key) + `",
  "policy": { "baseline_enabled": true },
  "admin": { "enabled": false }
}`)
	cfg, err := LoadServer(writeServer(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ServerID != "server-west" {
		t.Fatalf("server_id mismatch")
	}
	if !cfg.Policy.BaselineEnabled {
		t.Fatalf("baseline should be enabled")
	}
}

func TestLoadServerRejectsBadKey(t *testing.T) {
	body := []byte(`{"server_id":"s","listen_addr":":1","key":"???","policy":{"baseline_enabled":false},"admin":{"enabled":false}}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestLoadServerRequiresAdminListenWhenEnabled(t *testing.T) {
	key, _ := crypto.GenerateKey()
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "key": "` + EncodeKey(key) + `",
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": true }
}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}
