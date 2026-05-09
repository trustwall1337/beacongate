package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
)

// writeMultiTenantServer writes a fresh multi-tenant server config
// to disk with a populated client_template block. Returns the
// path. Used as the starting state for add-client tests.
func writeMultiTenantServer(t *testing.T, dir string, existingClients []config.ClientCredential) string {
	t.Helper()
	template := map[string]any{
		"listen_addr": "127.0.0.1:1080",
		"server":      map[string]any{"url": "https://example.com/tunnel"},
		"transport":   map[string]any{"type": "https"},
	}
	tmplBytes, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	srv := &config.ServerConfig{
		ServerID:       "server-test",
		ListenAddr:     ":8080",
		Clients:        existingClients,
		ClientTemplate: json.RawMessage(tmplBytes),
		Policy:         config.ServerPolicyConfig{BaselineEnabled: true},
		Admin:          config.ServerAdminConfig{Enabled: false},
	}
	body, err := json.MarshalIndent(srv, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAddClientHappyPath exercises the full flow: empty allowlist
// + valid template → run add-client → server config now has the
// new entry, friend file decodes as a valid ClientConfig with
// matching client_id and key.
func TestAddClientHappyPath(t *testing.T) {
	dir := t.TempDir()
	serverPath := writeMultiTenantServer(t, dir, nil)
	output := filepath.Join(dir, "mahdi.bg")

	if _, err := runAddClient(serverPath, "mahdi", output); err != nil {
		t.Fatalf("runAddClient: %v", err)
	}

	srv, err := config.LoadServer(serverPath)
	if err != nil {
		t.Fatalf("reload server: %v", err)
	}
	if len(srv.Clients) != 1 || srv.Clients[0].ClientID != "mahdi" {
		t.Fatalf("server config not updated as expected: %+v", srv.Clients)
	}

	friend, err := config.LoadClient(output)
	if err != nil {
		t.Fatalf("load friend file: %v", err)
	}
	if friend.ClientID != "mahdi" {
		t.Fatalf("friend client_id mismatch: got %q", friend.ClientID)
	}
	if friend.Server.Key != srv.Clients[0].Key {
		t.Fatalf("friend key does not match server allowlist entry")
	}
	// Sanity: the key must be a valid 32-byte AEAD key so the friend
	// can actually use it. crypto.NewSealer enforces this.
	keyBytes, err := config.DecodeKey(friend.Server.Key)
	if err != nil {
		t.Fatalf("friend key fails decode: %v", err)
	}
	if _, err := crypto.NewSealer(keyBytes); err != nil {
		t.Fatalf("friend key fails NewSealer: %v", err)
	}
}

// TestAddClientAppendsToExistingAllowlist verifies the second-run
// behaviour: existing entries are preserved and the new entry is
// appended. This is the common case once you have several friends.
func TestAddClientAppendsToExistingAllowlist(t *testing.T) {
	dir := t.TempDir()
	existingKey, _ := crypto.GenerateKey()
	serverPath := writeMultiTenantServer(t, dir, []config.ClientCredential{
		{ClientID: "sara", Key: config.EncodeKey(existingKey)},
	})
	output := filepath.Join(dir, "mahdi.bg")

	if _, err := runAddClient(serverPath, "mahdi", output); err != nil {
		t.Fatalf("runAddClient: %v", err)
	}

	srv, err := config.LoadServer(serverPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(srv.Clients))
	}
	if srv.Clients[0].ClientID != "sara" || srv.Clients[1].ClientID != "mahdi" {
		t.Fatalf("client order/contents wrong: %+v", srv.Clients)
	}
	// Sara's key MUST be unchanged — we only append, never rewrite.
	if srv.Clients[0].Key != config.EncodeKey(existingKey) {
		t.Fatalf("existing client's key was modified")
	}
}

func TestAddClientRejectsDuplicateName(t *testing.T) {
	dir := t.TempDir()
	existingKey, _ := crypto.GenerateKey()
	serverPath := writeMultiTenantServer(t, dir, []config.ClientCredential{
		{ClientID: "mahdi", Key: config.EncodeKey(existingKey)},
	})
	output := filepath.Join(dir, "mahdi.bg")
	_, err := runAddClient(serverPath, "mahdi", output)
	if err == nil || !strings.Contains(err.Error(), "already in the allowlist") {
		t.Fatalf("expected already-in-allowlist error, got %v", err)
	}
}

func TestAddClientRejectsLegacySingleKeyMode(t *testing.T) {
	dir := t.TempDir()
	key, _ := crypto.GenerateKey()
	srv := map[string]any{
		"server_id":   "server-legacy",
		"listen_addr": ":8080",
		"key":         config.EncodeKey(key),
		"policy":      map[string]any{"baseline_enabled": false},
		"admin":       map[string]any{"enabled": false},
	}
	body, _ := json.MarshalIndent(srv, "", "  ")
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runAddClient(path, "mahdi", filepath.Join(dir, "mahdi.bg"))
	if err == nil || !strings.Contains(err.Error(), "legacy single-key mode") {
		t.Fatalf("expected legacy-mode error, got %v", err)
	}
}

func TestAddClientRejectsMissingTemplate(t *testing.T) {
	dir := t.TempDir()
	srv := map[string]any{
		"server_id":   "server-no-template",
		"listen_addr": ":8080",
		"clients":     []map[string]any{},
		"policy":      map[string]any{"baseline_enabled": false},
		"admin":       map[string]any{"enabled": false},
	}
	// No client_template field.
	body, _ := json.MarshalIndent(srv, "", "  ")
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runAddClient(path, "mahdi", filepath.Join(dir, "mahdi.bg"))
	// We can't even pass server_config validation (no key, no clients
	// non-empty), so the error here is the auth-mode error, not the
	// template error. That's OK — the test below covers the
	// template-missing case once we have a valid auth mode.
	if err == nil {
		t.Fatalf("expected error on incomplete server config")
	}
}

// TestAddClientRejectsMissingTemplateWithExistingClients pins the
// case where the server is in valid multi-tenant mode (has at least
// one client already) but the operator forgot to add a
// client_template block. add-client must refuse with a clear hint.
func TestAddClientRejectsMissingTemplateWithExistingClients(t *testing.T) {
	dir := t.TempDir()
	existingKey, _ := crypto.GenerateKey()
	srv := map[string]any{
		"server_id":   "server-no-template",
		"listen_addr": ":8080",
		"clients": []map[string]any{
			{"client_id": "sara", "key": config.EncodeKey(existingKey)},
		},
		"policy": map[string]any{"baseline_enabled": false},
		"admin":  map[string]any{"enabled": false},
	}
	body, _ := json.MarshalIndent(srv, "", "  ")
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runAddClient(path, "mahdi", filepath.Join(dir, "mahdi.bg"))
	if err == nil || !strings.Contains(err.Error(), "client_template") {
		t.Fatalf("expected client_template-missing error, got %v", err)
	}
}

func TestAddClientRejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	serverPath := writeMultiTenantServer(t, dir, nil)
	_, err := runAddClient(serverPath, "", filepath.Join(dir, "x.bg"))
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("expected --name required error, got %v", err)
	}
}

func TestAddClientDefaultOutputName(t *testing.T) {
	dir := t.TempDir()
	serverPath := writeMultiTenantServer(t, dir, nil)
	// Run from inside dir so the default `<name>.bg` lands there.
	prev, _ := os.Getwd()
	defer func() { _ = os.Chdir(prev) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	out, err := runAddClient(serverPath, "mahdi", "")
	if err != nil {
		t.Fatalf("runAddClient: %v", err)
	}
	if out != "mahdi.bg" {
		t.Fatalf("default output: got %q want %q", out, "mahdi.bg")
	}
	if _, err := os.Stat(filepath.Join(dir, "mahdi.bg")); err != nil {
		t.Fatalf("default output file missing: %v", err)
	}
}

// TestAddClientAtomicWriteSurvivesPriorTempfile verifies that a
// stale .tmp file from an earlier crashed run does not block
// subsequent successful runs (rename should overwrite the temp on
// commit, and our writer creates a fresh temp each invocation).
func TestAddClientAtomicWriteSurvivesPriorTempfile(t *testing.T) {
	dir := t.TempDir()
	serverPath := writeMultiTenantServer(t, dir, nil)
	stale := serverPath + ".tmp"
	if err := os.WriteFile(stale, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "mahdi.bg")
	if _, err := runAddClient(serverPath, "mahdi", output); err != nil {
		t.Fatalf("runAddClient should overwrite stale temp: %v", err)
	}
	// Stale temp should be gone (rename consumed it on the commit).
	if _, err := os.Stat(stale); err == nil {
		t.Fatalf("expected stale .tmp to be consumed by atomic rename")
	}
}
