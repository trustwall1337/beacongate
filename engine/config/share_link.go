package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// LinkScheme is the URL scheme BeaconGate uses for share-links.
//
// Format: bg://config?d=<base64url(json)>&v=1
//
//	d = base64-URL-no-padding encoding of the client_config JSON
//	v = link format version; v1 is the only supported version today
//
// The link contains the entire client config including the AES key.
// Treat it like a password and distribute over end-to-end-encrypted
// channels only.
const LinkScheme = "bg"

// linkVersion is the only supported share-link format version.
const linkVersion = "1"

// EncodeLink serializes a ClientConfig into a `bg://...` share-link.
// The link is a single string the operator can paste into a chat,
// email, or QR code; the recipient imports it with
// `beacongate-client -import "<link>"`.
//
// Returns an error only if the config can't be JSON-marshaled, which
// shouldn't happen for a valid in-memory ClientConfig.
func EncodeLink(cfg *ClientConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("share_link: cfg is nil")
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("share_link: marshal: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	q := url.Values{}
	q.Set("v", linkVersion)
	q.Set("d", encoded)
	u := url.URL{
		Scheme:   LinkScheme,
		Host:     "config",
		RawQuery: q.Encode(),
	}
	return u.String(), nil
}

// DecodeLink parses a `bg://...` share-link back into a ClientConfig
// and validates the result via the standard ClientConfig.Validate.
// A link that decodes to an invalid config is rejected.
func DecodeLink(link string) (*ClientConfig, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, fmt.Errorf("share_link: empty link")
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("share_link: parse URL: %w", err)
	}
	if u.Scheme != LinkScheme {
		return nil, fmt.Errorf("share_link: wrong scheme %q (want %q)", u.Scheme, LinkScheme)
	}
	if u.Host != "config" {
		return nil, fmt.Errorf("share_link: wrong host %q (want \"config\")", u.Host)
	}
	q := u.Query()
	if v := q.Get("v"); v != "" && v != linkVersion {
		return nil, fmt.Errorf("share_link: unsupported version %q (want %q); upgrade beacongate-client", v, linkVersion)
	}
	encoded := q.Get("d")
	if encoded == "" {
		return nil, fmt.Errorf("share_link: missing data parameter (?d=)")
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("share_link: base64 decode: %w", err)
	}
	var cfg ClientConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("share_link: unmarshal config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("share_link: imported config is invalid: %w", err)
	}
	return &cfg, nil
}

// LinkSafeSummary returns a one-line human-readable description of a
// share-link's contents *without* exposing the AES key. Useful for the
// "you're about to import this — confirm?" prompt.
func LinkSafeSummary(cfg *ClientConfig) string {
	if cfg == nil {
		return "(empty config)"
	}
	server := cfg.Server.URL
	if server == "" {
		// appsscript mode — show the deployment count instead.
		keys, _ := ParseScriptKeys(cfg.Transport.Options["script_keys"])
		server = fmt.Sprintf("%d Apps Script deployment(s)", len(keys))
	}
	return fmt.Sprintf("client_id=%q listen=%q transport=%q server=%s",
		cfg.ClientID, cfg.ListenAddr, cfg.Transport.Type, server)
}
