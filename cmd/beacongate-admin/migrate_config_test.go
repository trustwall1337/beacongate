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

func TestApplyMigrationsConvertsScriptKeysStringToArray(t *testing.T) {
	raw := map[string]any{
		"transport": map[string]any{
			"type":    "appsscript",
			"options": map[string]any{"script_keys": "ID1,ID2", "script_accounts": "alpha,beta"},
		},
	}
	changes := applyMigrations(raw)
	if len(changes) != 2 {
		t.Fatalf("want 2 changes (script_keys array conversion + script_accounts removal), got %d: %v", len(changes), changes)
	}
	opts := raw["transport"].(map[string]any)["options"].(map[string]any)
	got, ok := opts["script_keys"].([]any)
	if !ok {
		t.Fatalf("script_keys should be []any after migration, got %T", opts["script_keys"])
	}
	want := []any{
		map[string]any{"id": "ID1", "account": "alpha"},
		map[string]any{"id": "ID2", "account": "beta"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, present := opts["script_accounts"]; present {
		t.Errorf("script_accounts should have been removed (folded into script_keys)")
	}
}

func TestApplyMigrationsLeavesArrayShapeAlone(t *testing.T) {
	// If script_keys is already in the array-of-objects shape, the
	// migration leaves it alone (idempotent).
	raw := map[string]any{
		"transport": map[string]any{
			"type": "appsscript",
			"options": map[string]any{
				"script_keys": []any{
					map[string]any{"id": "ID1", "account": "alpha"},
				},
			},
		},
	}
	before := deepCopyMap(raw)
	if changes := applyMigrations(raw); len(changes) != 0 {
		t.Fatalf("array-shape config should not be migrated, got %d changes: %v", len(changes), changes)
	}
	if !reflect.DeepEqual(raw, before) {
		t.Fatalf("mutated already-migrated config: %+v vs %+v", raw, before)
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
