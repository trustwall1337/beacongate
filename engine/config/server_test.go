package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadServerHappyPathClients(t *testing.T) {
	keyA, _ := crypto.GenerateKey()
	keyB, _ := crypto.GenerateKey()
	body := []byte(`{
  "server_id": "server-west",
  "listen_addr": ":8080",
  "clients": [
    {"client_id": "mahdi", "key": "` + EncodeKey(keyA) + `"},
    {"client_id": "sara",  "key": "` + EncodeKey(keyB) + `"}
  ],
  "policy": { "baseline_enabled": true },
  "admin": { "enabled": false }
}`)
	cfg, err := LoadServer(writeServer(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Clients) != 2 {
		t.Fatalf("want 2 clients, got %d", len(cfg.Clients))
	}
	if cfg.Clients[0].ClientID != "mahdi" || cfg.Clients[1].ClientID != "sara" {
		t.Fatalf("client_id mismatch: %+v", cfg.Clients)
	}
	if cfg.Key != "" {
		t.Fatalf("legacy key should be empty in multi-tenant mode")
	}
}

func TestLoadServerRejectsBothKeyAndClients(t *testing.T) {
	key, _ := crypto.GenerateKey()
	clientKey, _ := crypto.GenerateKey()
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "key": "` + EncodeKey(key) + `",
  "clients": [{"client_id": "mahdi", "key": "` + EncodeKey(clientKey) + `"}],
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": false }
}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected error message about mutual exclusion, got %v", err)
	}
}

// TestLoadServerAcceptsBootstrapState pins the operator-tooling
// contract: a server config with neither Key nor Clients populated
// is a valid *bootstrap* state at the loader level. The server
// runtime refuses to start in that state, but the admin CLI
// (`beacongate-admin add-client`) needs to be able to read and
// write the file BEFORE any clients exist — that's how the
// operator goes from "fresh deploy" to "first friend added"
// without a manual placeholder dance.
func TestLoadServerAcceptsBootstrapState(t *testing.T) {
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": false }
}`)
	cfg, err := LoadServer(writeServer(t, body))
	if err != nil {
		t.Fatalf("bootstrap state must load cleanly, got %v", err)
	}
	if cfg.Key != "" || len(cfg.Clients) != 0 {
		t.Fatalf("bootstrap state should have empty Key and Clients, got %+v", cfg)
	}
}

func TestLoadServerRejectsDuplicateClientID(t *testing.T) {
	keyA, _ := crypto.GenerateKey()
	keyB, _ := crypto.GenerateKey()
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "clients": [
    {"client_id": "mahdi", "key": "` + EncodeKey(keyA) + `"},
    {"client_id": "mahdi", "key": "` + EncodeKey(keyB) + `"}
  ],
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": false }
}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate client_id") {
		t.Fatalf("expected error message about duplicate client_id, got %v", err)
	}
}

func TestLoadServerRejectsDuplicateClientKey(t *testing.T) {
	key, _ := crypto.GenerateKey()
	encoded := EncodeKey(key)
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "clients": [
    {"client_id": "mahdi", "key": "` + encoded + `"},
    {"client_id": "sara",  "key": "` + encoded + `"}
  ],
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": false }
}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("expected error message about duplicate key, got %v", err)
	}
}

func TestLoadServerRejectsBadClientKey(t *testing.T) {
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "clients": [{"client_id": "mahdi", "key": "???"}],
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": false }
}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig (wrapping ErrInvalidKey), got %v", err)
	}
}

func TestLoadServerRejectsEmptyClientID(t *testing.T) {
	key, _ := crypto.GenerateKey()
	body := []byte(`{
  "server_id": "s", "listen_addr": ":1",
  "clients": [{"client_id": "", "key": "` + EncodeKey(key) + `"}],
  "policy": { "baseline_enabled": false },
  "admin": { "enabled": false }
}`)
	_, err := LoadServer(writeServer(t, body))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
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
