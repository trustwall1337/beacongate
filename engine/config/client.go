package config

import (
	"errors"
	"fmt"
)

type ClientConfig struct {
	ClientID   string                `json:"client_id"`
	ListenAddr string                `json:"listen_addr"`
	Server     ClientServerConfig    `json:"server"`
	Transport  ClientTransportConfig `json:"transport"`
	Socks      ClientSocksConfig     `json:"socks,omitempty"`
}

// ClientSocksConfig optionally requires SOCKS5 username/password auth on
// the local listener. Empty username = no auth (default), suitable for
// single-user laptops where 127.0.0.1 is already a trust boundary.
type ClientSocksConfig struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type ClientServerConfig struct {
	URL string `json:"url"`
	// Key is the base64-encoded 32-byte AEAD key shared with the server.
	Key string `json:"key"`
}

type ClientTransportConfig struct {
	Type    string            `json:"type"`
	Options map[string]string `json:"options,omitempty"`
}

func (c *ClientConfig) Validate() error {
	if c.ClientID == "" {
		return fmt.Errorf("%w: client_id required", ErrInvalidConfig)
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("%w: listen_addr required", ErrInvalidConfig)
	}
	if _, err := DecodeKey(c.Server.Key); err != nil {
		return err
	}
	if c.Transport.Type == "" {
		return fmt.Errorf("%w: transport.type required", ErrInvalidConfig)
	}
	// Transport-aware server.url rules. Plan A8: in appsscript mode the
	// script URL is built from transport.options.script_keys, so
	// server.url MUST be empty/omitted; the loader fails closed if both
	// are set so a stale URL can't silently bypass the disguise.
	switch c.Transport.Type {
	case "https", "google":
		if c.Server.URL == "" {
			return fmt.Errorf("%w: server.url required for transport.type=%q", ErrInvalidConfig, c.Transport.Type)
		}
	case "appsscript":
		if c.Server.URL != "" {
			return fmt.Errorf("%w: server.url must be empty for transport.type=\"appsscript\" (the script URL is built from transport.options.script_keys; got %q)", ErrInvalidConfig, c.Server.URL)
		}
		if c.Transport.Options["script_keys"] == "" {
			return fmt.Errorf("%w: transport.options.script_keys required for transport.type=\"appsscript\"", ErrInvalidConfig)
		}
	}
	return nil
}

func LoadClient(path string) (*ClientConfig, error) {
	var c ClientConfig
	if err := loadJSON(path, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// ServerKeyBytes returns the decoded shared key for a validated config.
func (c *ClientConfig) ServerKeyBytes() ([]byte, error) {
	if c.Server.Key == "" {
		return nil, errors.New("config: server.key missing")
	}
	return DecodeKey(c.Server.Key)
}
