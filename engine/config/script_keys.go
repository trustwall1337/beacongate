package config

import (
	"fmt"
	"strings"
)

// ScriptKey is one Apps Script deployment entry: a deployment ID plus
// an optional human-readable account label used for stats grouping.
type ScriptKey struct {
	ID      string
	Account string
}

// ParseScriptKeys converts the raw `transport.options.script_keys` value
// (read from JSON as `any`) into a slice of ScriptKey.
//
// Accepted input shapes (operators may use any; ParseScriptKeys
// normalizes them to the same internal form):
//
//  1. string, comma-separated:           "ID1,ID2,ID3"
//  2. []any of strings:                  ["ID1", "ID2"]
//  3. []any of objects:                  [{"id":"ID1","account":"alpha"}, {"id":"ID2","account":"beta"}]
//  4. mixed object/string entries:       [{"id":"ID1","account":"alpha"}, "ID2"]
//
// Bare-string entries always produce ScriptKey{ID: <s>, Account: ""}.
//
// Returns an error if the input shape is unsupported, an entry is the
// wrong type, or any entry has an empty ID. Returns an empty slice
// (no error) if the input is nil, missing, an empty string, or an
// empty list — callers decide whether emptiness is allowed.
func ParseScriptKeys(raw any) ([]ScriptKey, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		return parseScriptKeysFromString(v), nil
	case []any:
		return parseScriptKeysFromArray(v)
	case []string:
		// Accept []string in case the value was constructed in Go
		// rather than decoded from JSON. JSON decode always produces
		// []any, but a programmatic config could supply []string.
		out := make([]ScriptKey, 0, len(v))
		for i, s := range v {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, ScriptKey{ID: s})
			_ = i
		}
		return out, nil
	default:
		return nil, fmt.Errorf("script_keys: unsupported type %T (want string or array)", raw)
	}
}

func parseScriptKeysFromString(s string) []ScriptKey {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]ScriptKey, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, ScriptKey{ID: p})
	}
	return out
}

func parseScriptKeysFromArray(arr []any) ([]ScriptKey, error) {
	out := make([]ScriptKey, 0, len(arr))
	for i, entry := range arr {
		switch e := entry.(type) {
		case string:
			s := strings.TrimSpace(e)
			if s == "" {
				continue
			}
			out = append(out, ScriptKey{ID: s})
		case map[string]any:
			id, _ := e["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("script_keys[%d]: object entry has empty or missing \"id\" field", i)
			}
			account, _ := e["account"].(string)
			account = strings.TrimSpace(account)
			out = append(out, ScriptKey{ID: id, Account: account})
		default:
			return nil, fmt.Errorf("script_keys[%d]: unsupported entry type %T (want string or object)", i, entry)
		}
	}
	return out, nil
}

// IDs returns just the deployment IDs from a slice of ScriptKey.
// Useful for the appsscript transport which still uses a parallel
// []string internally for ScriptKeys.
func ScriptKeyIDs(keys []ScriptKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.ID
	}
	return out
}

// Accounts returns the parallel slice of account labels (empty string
// where no label was set).
func ScriptKeyAccounts(keys []ScriptKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.Account
	}
	return out
}
