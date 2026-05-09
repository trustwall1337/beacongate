package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/trustwall1337/beacongate/engine/crypto"
)

func mustValidConfig(t *testing.T) *ClientConfig {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return &ClientConfig{
		ClientID:   "test-client",
		ListenAddr: "127.0.0.1:1080",
		Server: ClientServerConfig{
			URL: "",
			Key: EncodeKey(key),
		},
		Transport: ClientTransportConfig{
			Type: "appsscript",
			Options: map[string]any{
				"script_keys": []any{
					map[string]any{"id": "DEPID_ALPHA", "account": "alpha"},
				},
			},
		},
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cfg := mustValidConfig(t)
	link, err := EncodeLink(cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(link, "bg://config?") {
		t.Errorf("link doesn't start with bg://config?: %s", link)
	}
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ClientID != cfg.ClientID {
		t.Errorf("ClientID round-trip: got %q, want %q", got.ClientID, cfg.ClientID)
	}
	if got.Server.Key != cfg.Server.Key {
		t.Errorf("Server.Key round-trip mismatch")
	}
}

func TestDecodeRejectsWrongScheme(t *testing.T) {
	_, err := DecodeLink("https://example.com/?d=abcd")
	if err == nil {
		t.Fatal("expected error for non-bg scheme")
	}
}

func TestDecodeRejectsMissingData(t *testing.T) {
	_, err := DecodeLink("bg://config")
	if err == nil {
		t.Fatal("expected error for missing ?d=")
	}
}

func TestDecodeRejectsCorruptBase64(t *testing.T) {
	_, err := DecodeLink("bg://config?v=1&d=not!valid!base64")
	if err == nil {
		t.Fatal("expected error for corrupt base64")
	}
}

func TestDecodeRejectsInvalidConfigInLink(t *testing.T) {
	// A link whose embedded JSON is structurally valid but fails
	// Validate (e.g. empty client_id).
	bad := `{"client_id":"","listen_addr":"127.0.0.1:1080","server":{"key":"` + EncodeKey(make([]byte, 32)) + `"},"transport":{"type":"https","options":{}}}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(bad))
	link := "bg://config?v=1&d=" + encoded
	_, err := DecodeLink(link)
	if err == nil {
		t.Fatal("expected error for invalid embedded config")
	}
	if !strings.Contains(err.Error(), "client_id") && !strings.Contains(err.Error(), "Validate") && !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention validation failure: %v", err)
	}
}

func TestDecodeRejectsUnknownVersion(t *testing.T) {
	cfg := mustValidConfig(t)
	link, _ := EncodeLink(cfg)
	// Replace v=1 with v=99
	tampered := strings.Replace(link, "v=1", "v=99", 1)
	_, err := DecodeLink(tampered)
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestLinkSafeSummaryHidesKey(t *testing.T) {
	cfg := mustValidConfig(t)
	summary := LinkSafeSummary(cfg)
	if strings.Contains(summary, cfg.Server.Key) {
		t.Errorf("summary leaks AES key: %s", summary)
	}
	if !strings.Contains(summary, cfg.ClientID) {
		t.Errorf("summary should include client_id: %s", summary)
	}
}
