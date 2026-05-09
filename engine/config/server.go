package config

import (
	"fmt"
	"net/url"
	"strings"
)

type ServerConfig struct {
	ServerID   string             `json:"server_id"`
	ListenAddr string             `json:"listen_addr"`
	TunnelPath string             `json:"tunnel_path,omitempty"`
	Key        string             `json:"key"`
	Policy     ServerPolicyConfig `json:"policy"`
	Admin      ServerAdminConfig  `json:"admin"`
	HealthPath string             `json:"health_path,omitempty"`
	Safety     ServerSafetyConfig `json:"safety"`
	Limits     ServerLimitsConfig `json:"limits"`
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
	if _, err := DecodeKey(c.Key); err != nil {
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
