package main

import (
	"reflect"
	"testing"
)

func TestApplyMigrationsRenamesGoogleToHTTPS(t *testing.T) {
	raw := map[string]any{
		"client_id":   "c",
		"listen_addr": "127.0.0.1:1080",
		"server":      map[string]any{"url": "https://relay.example.com/tunnel", "key": "K"},
		"transport":   map[string]any{"type": "google", "options": map[string]any{"fronting_host": ""}},
	}
	changes := applyMigrations(raw)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	tr := raw["transport"].(map[string]any)
	if tr["type"] != "https" {
		t.Fatalf("transport.type = %q, want %q", tr["type"], "https")
	}
}

func TestApplyMigrationsIsIdempotent(t *testing.T) {
	raw := map[string]any{
		"client_id":   "c",
		"listen_addr": "127.0.0.1:1080",
		"server":      map[string]any{"url": "https://relay.example.com/tunnel", "key": "K"},
		"transport":   map[string]any{"type": "https", "options": map[string]any{}},
	}
	before := deepCopyMap(raw)
	changes := applyMigrations(raw)
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes on already-v1.1 config, got %d: %v", len(changes), changes)
	}
	if !reflect.DeepEqual(raw, before) {
		t.Fatalf("idempotency violation: raw mutated despite no changes\nbefore: %+v\nafter:  %+v", before, raw)
	}
}

func TestApplyMigrationsAcceptsCaseAndWhitespace(t *testing.T) {
	for _, t1 := range []string{"google", "GOOGLE", "Google", "  google  "} {
		raw := map[string]any{
			"transport": map[string]any{"type": t1},
		}
		changes := applyMigrations(raw)
		if len(changes) != 1 {
			t.Fatalf("input %q: expected 1 change, got %d", t1, len(changes))
		}
		if raw["transport"].(map[string]any)["type"] != "https" {
			t.Fatalf("input %q: rename failed: %+v", t1, raw)
		}
	}
}

func TestApplyMigrationsLeavesAppsScriptAlone(t *testing.T) {
	raw := map[string]any{
		"transport": map[string]any{
			"type":    "appsscript",
			"options": map[string]any{"script_keys": "ID1"},
		},
	}
	before := deepCopyMap(raw)
	if changes := applyMigrations(raw); len(changes) != 0 {
		t.Fatalf("appsscript should not be migrated, got %d changes: %v", len(changes), changes)
	}
	if !reflect.DeepEqual(raw, before) {
		t.Fatalf("mutated appsscript config: %+v vs %+v", raw, before)
	}
}

func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if vm, ok := v.(map[string]any); ok {
			out[k] = deepCopyMap(vm)
		} else {
			out[k] = v
		}
	}
	return out
}
