package config

import "fmt"

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
