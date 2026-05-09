// it is the routing layer that maps an inbound wire packet's
// cleartext client_id to the per-client Sealer that holds the
// matching master key. Renaming would touch every server-side
// consumer, including the bootstrap.
//
//nolint:revive // var-naming: package shadows stdlib "crypto" by design;
package crypto

import "fmt"

// SealerRegistry routes cleartext-extracted client_ids (see
// PeekClientID) to the Sealer that owns the matching master key.
//
// Two modes:
//
//   - **Single-tenant** (legacy): one master key shared by every
//     client. Built with NewSingleKeyRegistry. Lookup returns the
//     same Sealer for any client_id. Backwards-compatible with
//     deployments where ServerConfig.Key is set and
//     ServerConfig.Clients is empty.
//
//   - **Multi-tenant** (per-friend allowlist): each registered
//     client_id owns a distinct master key. Built with
//     NewMultiKeyRegistry from a list of (client_id, master_key)
//     pairs. Lookup returns the per-friend Sealer, or nil if the
//     client_id is not in the allowlist.
//
// In multi-tenant mode the nil return on unknown client_id is the
// rejection signal: the tunnel handler maps it to a 401 response
// before any AEAD work runs. This makes per-friend revocation O(1)
// — drop the entry, reload, done.
//
// Concurrency: the registry is read-only after construction. All
// methods are safe for concurrent use without locking. (The inner
// Sealer's per-client AEAD cache has its own lock.)
type SealerRegistry struct {
	// single is non-nil iff the registry was built via
	// NewSingleKeyRegistry. When set, Lookup returns it for any
	// client_id.
	single *Sealer
	// byClient is non-empty iff the registry was built via
	// NewMultiKeyRegistry. Lookup returns byClient[clientID] or nil
	// if absent.
	byClient map[string]*Sealer
}

// NewSingleKeyRegistry wraps a single Sealer so every client_id
// resolves to it. Used by the legacy single-tenant bootstrap path
// (ServerConfig.Key set, ServerConfig.Clients empty) and by tests
// that don't exercise the per-friend allowlist.
//
// Returns an error only if masterKey is the wrong size (mirrors
// NewSealer's validation).
func NewSingleKeyRegistry(masterKey []byte) (*SealerRegistry, error) {
	s, err := NewSealer(masterKey)
	if err != nil {
		return nil, err
	}
	return &SealerRegistry{single: s}, nil
}

// SingleKeyRegistryFromSealer is the test-friendly alternative to
// NewSingleKeyRegistry: it wraps an already-constructed Sealer so
// tests can share one Sealer for both the client side of a
// round-trip and the server-side registry without re-exposing the
// raw master key.
//
// Production code should prefer NewSingleKeyRegistry — it owns the
// Sealer construction and validates the key.
func SingleKeyRegistryFromSealer(s *Sealer) *SealerRegistry {
	return &SealerRegistry{single: s}
}

// MultiKeyEntry is one element of the per-client allowlist passed
// to NewMultiKeyRegistry. The engine/config.ClientCredential type
// is the on-disk JSON form; bootstrapping code adapts each
// ClientCredential into a MultiKeyEntry by base64-decoding the key.
//
// engine/crypto cannot depend on engine/config (it would create an
// import cycle), so this struct lives here as the package-internal
// shape multi-tenant callers construct.
type MultiKeyEntry struct {
	// ClientID is the cleartext identifier the friend's client
	// stamps into the v1.1 wire envelope header. Must be non-empty
	// and unique within the entries slice.
	ClientID string
	// MasterKey is the friend's per-friend master key. MUST be
	// MasterKeySize bytes; NewMultiKeyRegistry returns an error
	// otherwise. The same per-client HKDF derivation Sealer
	// applies, so both inbound (client → server) and outbound
	// (server → friend) AEAD keys are derived from this key.
	MasterKey []byte
}

// NewMultiKeyRegistry builds a multi-tenant registry from a list of
// (client_id, master_key) entries. Each entry gets its own Sealer
// — i.e. a unique HKDF root — so a leaked or seized friend's key
// does NOT compromise any other friend's traffic.
//
// Returns an error if:
//   - entries is empty (use NewSingleKeyRegistry for single-tenant),
//   - any client_id is empty or duplicated within entries,
//   - any master key is the wrong size.
//
// Note: duplicate-key detection (same MasterKey under two
// distinct client_ids) is performed by engine/config when loading
// the server config, not here, because the only way to detect it
// without exposing key bytes is at the JSON-parse layer where
// equality of base64 strings is meaningful.
func NewMultiKeyRegistry(entries []MultiKeyEntry) (*SealerRegistry, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("crypto: NewMultiKeyRegistry: at least one entry required")
	}
	byClient := make(map[string]*Sealer, len(entries))
	for i, e := range entries {
		if e.ClientID == "" {
			return nil, fmt.Errorf("crypto: NewMultiKeyRegistry: entries[%d]: client_id required", i)
		}
		if _, dup := byClient[e.ClientID]; dup {
			return nil, fmt.Errorf("crypto: NewMultiKeyRegistry: entries[%d]: duplicate client_id %q", i, e.ClientID)
		}
		s, err := NewSealer(e.MasterKey)
		if err != nil {
			return nil, fmt.Errorf("crypto: NewMultiKeyRegistry: entries[%d] (%s): %w", i, e.ClientID, err)
		}
		byClient[e.ClientID] = s
	}
	return &SealerRegistry{byClient: byClient}, nil
}

// Lookup returns the Sealer that owns the master key for clientID.
//
//   - Single-tenant: returns the shared Sealer for any clientID.
//     The caller still must call Sealer.Open(wire) to authenticate;
//     a wrong client_id with a valid master key only matters if the
//     attacker controlled both the cleartext id and the AEAD seal,
//     which they don't (master key is the secret).
//
//   - Multi-tenant: returns the per-friend Sealer or nil if
//     clientID is not in the allowlist. nil is the rejection
//     signal — callers map it to 401.
//
// Lookup is allocation-free and runs in O(1) (single-tenant) or
// O(1)-amortized (multi-tenant map lookup). The fast path before
// the AEAD gate is intentionally minimal so the auth check does
// not move the latency p50.
func (r *SealerRegistry) Lookup(clientID string) *Sealer {
	if r.single != nil {
		return r.single
	}
	return r.byClient[clientID]
}

// IsMultiTenant reports whether the registry was built in
// multi-tenant mode. Useful for the bootstrap path's startup log
// line and for tests that exercise mode-specific behavior.
func (r *SealerRegistry) IsMultiTenant() bool {
	return r.byClient != nil
}

// Size returns the number of registered client_ids. In single-
// tenant mode this is always 1 (the shared Sealer); in
// multi-tenant mode it is the allowlist length. Used by startup
// logging only.
func (r *SealerRegistry) Size() int {
	if r.single != nil {
		return 1
	}
	return len(r.byClient)
}
