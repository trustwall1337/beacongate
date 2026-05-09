// Package profiles is the lightweight multi-profile layer for the
// BeaconGate client. A "profile" is a single client_config.json that
// lives under `${XDG_CONFIG_HOME:-~/.config}/beacongate/profiles/<name>.json`.
//
// The package is intentionally minimal: it computes the profiles
// directory, resolves a profile name to a path, and lists what's
// there. Add/remove/edit are file-system operations the user can do
// with `cp`/`rm` plus the `beacongate-client -import` flag, which
// targets a profile path when `-profile <name>` is set.
package profiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir returns the absolute path of the profiles directory:
//
//	${XDG_CONFIG_HOME}/beacongate/profiles
//
// Falls back to ${HOME}/.config/beacongate/profiles if
// XDG_CONFIG_HOME is unset (XDG Base Directory spec). Returns an
// error if neither environment variable can yield a usable path.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "beacongate", "profiles"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("profiles: cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "beacongate", "profiles"), nil
}

// Path returns the absolute path of a single named profile:
// <Dir>/<name>.json. The name is sanitized — only letters, digits,
// underscore, hyphen, and dot are allowed; anything else is an
// error. This keeps a malicious or careless `-profile ../etc/passwd`
// from doing path traversal.
func Path(name string) (string, error) {
	if name == "" {
		return "", errors.New("profiles: empty profile name")
	}
	for _, r := range name {
		if !isSafeNameRune(r) {
			return "", fmt.Errorf("profiles: profile name %q contains invalid character %q (allowed: letters, digits, _ - .)", name, r)
		}
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// EnsureDir creates the profiles directory if it doesn't exist.
// Mode 0700 — only the user can list profiles (each contains an AES
// key).
func EnsureDir() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

// List returns the names of all profiles currently stored in the
// profiles directory, sorted alphabetically. A missing directory is
// not an error — it just yields an empty list.
func List() ([]string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("profiles: read dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes a profile by name. Returns nil on missing-file
// (idempotent) and an error on any other I/O failure.
func Remove(name string) error {
	p, err := Path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("profiles: remove %s: %w", p, err)
	}
	return nil
}

func isSafeNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-' || r == '.':
		return true
	}
	return false
}
