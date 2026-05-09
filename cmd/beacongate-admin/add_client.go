package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
)

// addClient generates a new per-friend (client_id, master_key) pair,
// appends it to the server config's `clients` allowlist, and emits a
// ready-to-import .bg file containing the matching client_config
// JSON for the friend.
//
// Usage:
//
//	beacongate-admin add-client \
//	  --server-config /etc/beacongate/server_config.json \
//	  --name          mahdi \
//	  --output        ./mahdi.bg
//
// Per-friend file shape comes from the server config's
// `client_template` block (see ServerConfig.ClientTemplate). That
// block carries the transport-side fields (Apps Script keys, SNI
// rotation, listen_addr) shared across all friends. This command
// stamps the freshly-generated `client_id` and `server.key` into
// the template, validates the result as a full ClientConfig, and
// writes it to --output. Keeping the template in the server config
// means there's exactly one file to maintain per deployment.
//
// The server config is mutated atomically (write to .tmp,
// rename) so a crash mid-edit cannot corrupt the live config. The
// caller must restart the server (or send SIGHUP if a future
// version supports it) for the new client to take effect; this
// tool prints the restart hint at the end.
//
// Refuses to operate when:
//   - the server config is still in legacy single-key mode
//     (cfg.Key non-empty). Operator must remove `key` first;
//     mixing the two would silently change auth semantics.
//   - the server config has no `client_template` block. The
//     operator must add one before running this tool — the error
//     message points at the exact field to populate.
//   - --name already exists in cfg.Clients (would shadow the
//     existing entry in undefined ways).
//   - --name is empty or longer than crypto.MaxClientIDLen.
//   - the rendered friend config fails ClientConfig.Validate
//     (template was malformed in some way the JSON load didn't
//     catch).
func addClient() {
	fs := flag.NewFlagSet("add-client", flag.ExitOnError)
	serverPath := fs.String("server-config", "", "path to server_config.json (will be atomically rewritten)")
	name := fs.String("name", "", "new client_id (stable identifier for this friend)")
	output := fs.String("output", "", "where to write the friend's .bg file (default: <name>.bg in cwd)")
	_ = fs.Parse(os.Args[2:])

	out, err := runAddClient(*serverPath, *name, *output)
	if err != nil {
		die("add-client: %v", err)
	}

	abs, _ := filepath.Abs(out)
	fmt.Fprintf(os.Stderr, "==> Added client %q to %s\n", *name, *serverPath)
	fmt.Fprintf(os.Stderr, "==> Wrote friend config: %s\n", abs)
	fmt.Fprintln(os.Stderr, "==> WARNING: the friend file contains the AES key. Treat it like a password.")
	fmt.Fprintln(os.Stderr, "==> NEXT: restart the server for the new client to take effect:")
	fmt.Fprintln(os.Stderr, "        systemctl restart beacongate-server.service")
	fmt.Fprintln(os.Stderr, "==> Then deliver the friend file to the user via Google Drive.")
}

// runAddClient is the testable core of the add-client subcommand.
// Returns the (possibly defaulted) output path on success, or an
// error describing why the operation was refused. All side effects
// (server-config rewrite, friend-file write) are atomic.
//
// Errors are returned, not Fatal'd, so tests can drive the
// function directly without subprocess machinery.
func runAddClient(serverPath, name, output string) (string, error) {
	if serverPath == "" {
		return "", fmt.Errorf("--server-config is required")
	}
	if name == "" {
		return "", fmt.Errorf("--name is required")
	}
	if len(name) > crypto.MaxClientIDLen {
		return "", fmt.Errorf("--name too long (%d > %d)", len(name), crypto.MaxClientIDLen)
	}
	if output == "" {
		output = name + ".bg"
	}

	srv, err := config.LoadServer(serverPath)
	if err != nil {
		return "", fmt.Errorf("load server config: %w", err)
	}
	if strings.TrimSpace(srv.Key) != "" {
		return "", fmt.Errorf("server config is in legacy single-key mode (key field set); "+
			"remove the `key` field from %s before adding clients", serverPath)
	}
	if len(srv.ClientTemplate) == 0 {
		return "", fmt.Errorf("server config has no `client_template` block; "+
			"add one to %s with the transport block shared across all friends", serverPath)
	}
	for _, c := range srv.Clients {
		if c.ClientID == name {
			return "", fmt.Errorf("client_id %q is already in the allowlist", name)
		}
	}

	var tmpl config.ClientConfig
	if err := json.Unmarshal(srv.ClientTemplate, &tmpl); err != nil {
		return "", fmt.Errorf("parse client_template: %w", err)
	}

	keyBytes, err := crypto.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	encodedKey := config.EncodeKey(keyBytes)
	tmpl.ClientID = name
	tmpl.Server.Key = encodedKey
	if err := tmpl.Validate(); err != nil {
		return "", fmt.Errorf("rendered friend config failed validation: %w", err)
	}

	srv.Clients = append(srv.Clients, config.ClientCredential{
		ClientID: name,
		Key:      encodedKey,
	})
	if err := srv.Validate(); err != nil {
		return "", fmt.Errorf("updated server config failed validation: %w", err)
	}

	if err := atomicWriteJSON(serverPath, srv, 0o600); err != nil {
		return "", fmt.Errorf("write server config: %w", err)
	}
	if err := atomicWriteJSON(output, tmpl, 0o600); err != nil {
		// Server config has already been updated. Caller-visible
		// recovery hint goes to the wrapper that called us.
		return output, fmt.Errorf("server config updated but friend file write failed; "+
			"re-run with the same --name to retry, or remove %q from %s manually: %w",
			name, serverPath, err)
	}
	return output, nil
}

// atomicWriteJSON marshals data as indented JSON and writes it to
// path via tempfile-and-rename so a crash mid-write cannot corrupt
// the existing file. Returns the original error if either the
// temp-write or the rename fails; on rename failure the temp file
// is removed.
func atomicWriteJSON(path string, data any, mode os.FileMode) error {
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	body = append(body, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir parent: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
