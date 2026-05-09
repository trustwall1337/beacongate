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

// TestEncodeLinkStripsDefaults verifies that a ClientConfig containing
// only-default values for elidable fields produces a noticeably
// shorter link than the same config marshaled directly. This is the
// QR-density win — the encoded link must drop server.url, the
// 216.239.38.120:443 google_host, the empty account label, the
// 127.0.0.1:1080 listen_addr, and the empty socks block.
func TestEncodeLinkStripsDefaults(t *testing.T) {
	cfg := mustValidConfig(t)
	cfg.Transport.Options["google_host"] = "216.239.38.120:443" // matches default
	link, err := EncodeLink(cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Decode the base64 payload back to JSON to inspect what was emitted.
	const prefix = "bg://config?d="
	if !strings.Contains(link, prefix) {
		t.Fatalf("link missing %q: %s", prefix, link)
	}
	idx := strings.Index(link, "d=")
	if idx < 0 {
		t.Fatalf("link missing d= param: %s", link)
	}
	dParam := link[idx+2:]
	if amp := strings.Index(dParam, "&"); amp >= 0 {
		dParam = dParam[:amp]
	}
	body, err := base64.RawURLEncoding.DecodeString(dParam)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	js := string(body)
	for _, banned := range []string{
		`"google_host":"216.239.38.120:443"`,
		`"url":""`,
		`"account":""`,
		`"listen_addr":"127.0.0.1:1080"`,
		`"socks":{}`,
	} {
		if strings.Contains(js, banned) {
			t.Errorf("encoded JSON should NOT contain default-valued %q; got %s", banned, js)
		}
	}
	// The required fields must still be present.
	for _, required := range []string{`"client_id"`, `"key"`, `"transport"`, `"type"`} {
		if !strings.Contains(js, required) {
			t.Errorf("encoded JSON missing required field %q: %s", required, js)
		}
	}
}

// TestDecodeLinkFillsDefaultsForStrippedFields confirms a link
// produced by the new (stripping) encoder decodes back into a
// validate-able ClientConfig: listen_addr gets restored, the
// transport's own defaults handle google_host downstream.
func TestDecodeLinkFillsDefaultsForStrippedFields(t *testing.T) {
	cfg := mustValidConfig(t)
	link, err := EncodeLink(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ListenAddr != "127.0.0.1:1080" {
		t.Errorf("listen_addr default not restored: got %q", got.ListenAddr)
	}
}

// TestDecodeLinkBackwardsCompatibleWithFullLink asserts a
// hand-constructed "full" (pre-stripping) link — what an older client
// would have produced — still decodes cleanly with the new decoder.
// fillLinkDefaults must be a no-op when the field is already populated.
func TestDecodeLinkBackwardsCompatibleWithFullLink(t *testing.T) {
	full := `{"client_id":"old-style","listen_addr":"127.0.0.1:1080","server":{"url":"","key":"` + EncodeKey(make([]byte, 32)) + `"},"transport":{"type":"appsscript","options":{"google_host":"216.239.38.120:443","script_keys":[{"id":"DEPID","account":""}],"sni":"www.google.com"}},"socks":{}}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(full))
	link := "bg://config?v=1&d=" + encoded
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatalf("decode old-style link: %v", err)
	}
	if got.ListenAddr != "127.0.0.1:1080" {
		t.Errorf("ListenAddr round-trip: got %q", got.ListenAddr)
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
