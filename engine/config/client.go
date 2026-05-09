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

// ClientTransportConfig holds transport-mode and a free-form options
// map.
//
// **As of v1.1.0 the Options value type is `any`, not `string`.** This
// is a breaking source-level API change for anyone vendoring this
// package; end-user JSON configs are backward compatible (a JSON
// string-valued option still parses correctly). The change is needed
// to support `script_keys` in the Goose-natural array-of-objects
// shape: `[{"id": "...", "account": "..."}]`. See
// engine/config/script_keys.go for the parser.
//
// Use the OptionString helper for string-valued options to get the
// stdlib-equivalent ergonomics of the old type.
type ClientTransportConfig struct {
	Type    string         `json:"type"`
	Options map[string]any `json:"options,omitempty"`
}

// OptionString returns the string value for the given key, or "" if
// the key is missing or holds a non-string value.
//
// Use this for transport options that are always string-valued (most
// of them). For `script_keys`, which can be a string OR an array of
// strings/objects, call ParseScriptKeys instead.
func (c *ClientTransportConfig) OptionString(key string) string {
	v, ok := c.Options[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
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
		// script_keys can be either a comma-separated string (legacy)
		// or an array of strings/objects (Goose-natural). ParseScriptKeys
		// normalizes both. Empty (nil, "", or []) is rejected here because
		// appsscript mode requires at least one deployment.
		keys, err := ParseScriptKeys(c.Transport.Options["script_keys"])
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		if len(keys) == 0 {
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
