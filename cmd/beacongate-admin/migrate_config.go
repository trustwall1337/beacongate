package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// migrateConfig rewrites a pre-v1.1 client config to the v1.1 shape:
//   - `transport.type = "google"` is renamed to `"https"`.
//   - Other v1.0 → v1.1 mechanical changes can be added here as the
//     wire bump lands.
//
// The rewrite is idempotent: running it on an already-v1.1 config is
// a no-op (with a friendly message). The file is overwritten in place
// only when there are actual changes; otherwise it stays untouched
// (preserves mtime, helps audit trails).
//
// Usage:
//
//	beacongate-admin migrate-config --file client_config.json
//	beacongate-admin migrate-config --file client_config.json --dry-run
//
// Exit codes:
//
//	0 — success (file rewritten OR already in v1.1 shape)
//	1 — file not found, parse error, or migration produced an
//	    invalid config
func migrateConfig() {
	fs := flag.NewFlagSet("migrate-config", flag.ExitOnError)
	file := fs.String("file", "", "path to a client config JSON to migrate in place")
	dryRun := fs.Bool("dry-run", false, "print the migrated JSON to stdout without writing")
	_ = fs.Parse(os.Args[2:])
	if *file == "" {
		die("migrate-config: --file is required")
	}
	original, err := os.ReadFile(*file)
	if err != nil {
		die("migrate-config: read %s: %v", *file, err)
	}

	// Parse permissively: we want to preserve every field the user has
	// (including ones unknown to the v1.1 struct) so a no-op migration
	// doesn't strip anything. So we work on a generic map.
	var raw map[string]any
	if err := json.Unmarshal(original, &raw); err != nil {
		die("migrate-config: parse %s: %v", *file, err)
	}

	changes := applyMigrations(raw)
	if len(changes) == 0 {
		fmt.Fprintf(os.Stderr, "migrate-config: %s is already in v1.1 shape; no changes needed\n", *file)
		return
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		die("migrate-config: re-encode: %v", err)
	}
	out = append(out, '\n')

	if *dryRun {
		fmt.Println(string(out))
		fmt.Fprintf(os.Stderr, "\nmigrate-config dry-run: %d change(s):\n", len(changes))
		for _, c := range changes {
			fmt.Fprintf(os.Stderr, "  - %s\n", c)
		}
		fmt.Fprintf(os.Stderr, "Run without --dry-run to write %s in place.\n", *file)
		return
	}

	if err := os.WriteFile(*file, out, 0o600); err != nil {
		die("migrate-config: write %s: %v", *file, err)
	}
	fmt.Fprintf(os.Stderr, "migrate-config: rewrote %s with %d change(s):\n", *file, len(changes))
	for _, c := range changes {
		fmt.Fprintf(os.Stderr, "  - %s\n", c)
	}
}

// applyMigrations mutates raw in place to the v1.1 shape and returns
// human-readable descriptions of every change made. An empty return
// slice means the input is already v1.1.
func applyMigrations(raw map[string]any) []string {
	var changes []string

	// Migration 1: transport.type "google" → "https".
	transport, ok := raw["transport"].(map[string]any)
	if !ok {
		return changes
	}
	t, _ := transport["type"].(string)
	if strings.EqualFold(strings.TrimSpace(t), "google") {
		transport["type"] = "https"
		changes = append(changes, `transport.type: "google" → "https" (package was renamed in v1.1)`)
	}

	// Future migrations layer on here. Each should:
	//   1. Detect the old shape.
	//   2. Mutate raw to the new shape.
	//   3. Append a one-line description to `changes`.
	// Order matters when migrations build on each other; document
	// dependencies inline.

	return changes
}
