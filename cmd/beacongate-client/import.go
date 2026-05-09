package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trustwall1337/beacongate/engine/config"
)

// runImportLink decodes a `bg://...` share-link and writes the config
// to dst. If dst exists and force is false, the user is asked to
// confirm the overwrite on stdin.
//
// Returns the exit code (0 on success, 1 on any error).
func runImportLink(link, dst string, force bool) int {
	cfg, err := config.DecodeLink(link)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "==> Imported config: %s\n", config.LinkSafeSummary(cfg))

	if _, err := os.Stat(dst); err == nil && !force {
		fmt.Fprintf(os.Stderr, "==> %s already exists. Overwrite? [y/N] ", dst)
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			fmt.Fprintln(os.Stderr, "import cancelled.")
			return 1
		}
	}

	// Marshal with indent so the resulting file is human-readable; the
	// user may want to edit it later.
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "import failed: marshal: %v\n", err)
		return 1
	}
	body = append(body, '\n')

	// Make sure the parent dir exists so the user can use this with
	// the multi-profile setup that writes to ~/.config/beacongate/profiles/.
	if dir := filepath.Dir(dst); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "import failed: mkdir %s: %v\n", dir, err)
			return 1
		}
	}

	// Mode 0600 — the file contains the AES key.
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "import failed: write %s: %v\n", dst, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "==> Wrote %s (mode 0600)\n", dst)
	fmt.Fprintln(os.Stderr, "==> WARNING: this file contains the AES key. Treat it like a password.")
	fmt.Fprintln(os.Stderr, "==> Run beacongate-client -config", dst, "to start the tunnel.")
	return 0
}
