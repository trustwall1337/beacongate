package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/trustwall1337/beacongate/engine/crypto"
)

// ClientCredential is one entry in the server's per-client allowlist.
// Each friend gets their own (ClientID, Key) pair so that a leaked or
// seized config can be revoked by removing a single entry — without
// rotating every other friend's key. Compare this with the legacy
// single-key mode (ServerConfig.Key) where every client shares one
// master key and revocation forces a global re-key.
type ClientCredential struct {
	// ClientID is the cleartext identifier the friend's client sends
	// in the wire envelope header. Must be non-empty and unique
	// within the allowlist.
	ClientID string `json:"client_id"`
	// Key is the friend's per-friend master key, base64-encoded
	// (decodes to 32 bytes). It is NOT shared with any other friend
	// or with the legacy single-tenant Key. The Sealer uses this
	// master key to derive the per-direction AEAD keys for both
	// inbound (client → server) and outbound (server → friend) traffic
	// on this client_id's channel.
	Key string `json:"key"`
}

type ServerConfig struct {
	ServerID   string `json:"server_id"`
	ListenAddr string `json:"listen_addr"`
	TunnelPath string `json:"tunnel_path,omitempty"`
	// Key is the legacy single-tenant master key. Every connecting
	// client shares this key. Mutually exclusive with Clients: set
	// exactly one. Single-key mode remains supported for desktop /
	// dev / single-operator deployments where revocation isn't a
	// concern.
	Key string `json:"key,omitempty"`
	// Clients is the per-friend allowlist for multi-tenant deployments.
	// When non-empty, each connecting wire packet's cleartext
	// client_id is looked up here; unknown client_ids are rejected
	// before any crypto work, and the matching entry's Key is used
	// to derive AEAD keys for that channel. Removing an entry and
	// reloading the server is the revocation path.
	Clients []ClientCredential `json:"clients,omitempty"`
	// ClientTemplate is the per-deployment ClientConfig template the
	// admin CLI (`beacongate-admin add-client`) uses to render
	// per-friend `.bg` files. It carries the transport block (Apps
	// Script keys, SNI rotation, listen_addr) shared across all
	// friends. The server runtime IGNORES this field — it is
	// purely operator-tooling state stored alongside the allowlist
	// so a single file holds everything per-deployment.
	//
	// Stored as raw JSON so the embedded ClientConfig fields
	// (notably the empty placeholders for client_id and
	// server.key) don't have to satisfy ClientConfig.Validate at
	// server-config load time. The CLI parses + validates at
	// render time.
	ClientTemplate json.RawMessage    `json:"client_template,omitempty"`
	Policy         ServerPolicyConfig `json:"policy"`
	Admin          ServerAdminConfig  `json:"admin"`
	HealthPath     string             `json:"health_path,omitempty"`
	Safety         ServerSafetyConfig `json:"safety"`
	Limits         ServerLimitsConfig `json:"limits"`
	// UpstreamProxy optionally routes ALL outbound connections (the
	// dials the server makes to fulfil tunneled CONNECT requests)
	// through a SOCKS5 proxy. Useful when the VPS's datacenter IP is
	// blocked by Cloudflare bot scoring or similar — set this to a
	// local Cloudflare WARP socks5 endpoint
	// (e.g. "socks5://127.0.0.1:40000") so destinations see the
	// Cloudflare egress IP instead of the VPS IP. DNS is resolved at
	// the proxy (socks5h semantics). Empty = dial directly. Must be
	// "socks5://..." if set; other schemes are rejected.
	UpstreamProxy string `json:"upstream_proxy,omitempty"`
}

// ServerSafetyConfig controls the SSRF guard. The defaults block private,
// loopback, link-local, multicast, and cloud metadata IPs. Override only
// when you know the operational network does not include attacker-reachable
// internal services.
type ServerSafetyConfig struct {
	AllowPrivate  bool     `json:"allow_private,omitempty"`
	AllowMetadata bool     `json:"allow_metadata,omitempty"`
	ExtraBlocked  []string `json:"extra_blocked,omitempty"`
}

// ServerLimitsConfig caps abusive per-client behaviour. Zero values mean
// "use the safe default."
type ServerLimitsConfig struct {
	// MaxSessionsPerClient limits how many simultaneous tunnel sessions a
	// single client_id may hold. Zero -> 100 sessions.
	MaxSessionsPerClient int `json:"max_sessions_per_client,omitempty"`
	// IdleSessionTimeoutSeconds reaps sessions that have seen no upstream
	// or downstream traffic for that long. Zero -> 600s (10 min).
	IdleSessionTimeoutSeconds int `json:"idle_session_timeout_seconds,omitempty"`
}

type ServerPolicyConfig struct {
	BaselineEnabled bool   `json:"baseline_enabled"`
	StorePath       string `json:"store_path,omitempty"`
}

type ServerAdminConfig struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr,omitempty"`
	// Token is required when ListenAddr is non-loopback.
	Token string `json:"token,omitempty"`
}

func (c *ServerConfig) Validate() error {
	if c.ServerID == "" {
		return fmt.Errorf("%w: server_id required", ErrInvalidConfig)
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("%w: listen_addr required", ErrInvalidConfig)
	}
	if err := c.validateAuth(); err != nil {
		return err
	}
	if c.Admin.Enabled && c.Admin.ListenAddr == "" {
		return fmt.Errorf("%w: admin.listen_addr required when admin.enabled", ErrInvalidConfig)
	}
	if s := strings.TrimSpace(c.UpstreamProxy); s != "" {
		u, err := url.Parse(s)
		if err != nil {
			return fmt.Errorf("%w: upstream_proxy not a valid URL: %v", ErrInvalidConfig, err)
		}
		if u.Scheme != "socks5" {
			return fmt.Errorf("%w: upstream_proxy must use socks5:// scheme (got %q)", ErrInvalidConfig, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("%w: upstream_proxy missing host:port", ErrInvalidConfig)
		}
	}
	return nil
}

// validateAuth enforces that the configured auth mode is internally
// consistent — Key (single-tenant) and Clients (multi-tenant) are
// mutually exclusive — and that the populated entries are well-
// formed.
//
// **Bootstrap state allowed:** a server config with neither Key
// set nor Clients populated (typically `clients: []` plus a
// `client_template` block) is intentionally accepted at this layer
// so that `beacongate-admin add-client` can read and mutate the
// file before any friends exist. The server runtime
// (cmd/beacongate-server bootstrap) refuses to start in that state
// — it requires at least one configured client — so the bootstrap
// state is operator-tooling-visible only and never reaches a
// listening port.
//
// Setting both Key and Clients is rejected because the resulting
// auth surface is ambiguous: it would force a runtime decision
// about which one "wins" that an operator reading the file can't
// predict.
func (c *ServerConfig) validateAuth() error {
	hasKey := strings.TrimSpace(c.Key) != ""
	hasClients := len(c.Clients) > 0
	switch {
	case hasKey && hasClients:
		return fmt.Errorf("%w: set either key or clients, not both", ErrInvalidConfig)
	case hasKey:
		_, err := DecodeKey(c.Key)
		return err
	case hasClients:
		return validateClients(c.Clients)
	default:
		// Bootstrap state: no clients yet, no legacy key. Valid
		// for operator tooling (add-client). Server runtime will
		// refuse to start until at least one client is added.
		return nil
	}
}

// validateClients enforces uniqueness of client_id and key, plus
// well-formed key bytes. Duplicate keys across distinct client_ids
// are rejected because they almost always indicate a copy-paste
// mistake while editing the file by hand — and the resulting state
// (two friends able to impersonate each other) silently breaks the
// revocation guarantee the allowlist exists to provide.
func validateClients(clients []ClientCredential) error {
	seenIDs := make(map[string]struct{}, len(clients))
	seenKeys := make(map[string]string, len(clients))
	for i, cc := range clients {
		if cc.ClientID == "" {
			return fmt.Errorf("%w: clients[%d]: client_id required", ErrInvalidConfig, i)
		}
		if len(cc.ClientID) > crypto.MaxClientIDLen {
			return fmt.Errorf("%w: clients[%d] (%s): client_id too long (%d > %d)",
				ErrInvalidConfig, i, cc.ClientID, len(cc.ClientID), crypto.MaxClientIDLen)
		}
		if _, dup := seenIDs[cc.ClientID]; dup {
			return fmt.Errorf("%w: clients[%d]: duplicate client_id %q", ErrInvalidConfig, i, cc.ClientID)
		}
		seenIDs[cc.ClientID] = struct{}{}
		if _, err := DecodeKey(cc.Key); err != nil {
			return fmt.Errorf("%w: clients[%d] (%s): %v", ErrInvalidConfig, i, cc.ClientID, err)
		}
		if other, dup := seenKeys[cc.Key]; dup {
			return fmt.Errorf("%w: clients[%d] (%s): key duplicates client %q",
				ErrInvalidConfig, i, cc.ClientID, other)
		}
		seenKeys[cc.Key] = cc.ClientID
	}
	return nil
}

func LoadServer(path string) (*ServerConfig, error) {
	var c ServerConfig
	if err := loadJSON(path, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *ServerConfig) KeyBytes() ([]byte, error) { return DecodeKey(c.Key) }
