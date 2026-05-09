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

// stripLinkDefaults removes JSON fields equal to their compile-time
// defaults so the encoded share-link is shorter (visibly less dense
// QR). Defaults that downstream code applies anyway — the appsscript
// transport's GoogleIP default at construction time, or
// fillLinkDefaults() at decode time for fields Validate requires —
// reconstruct the elided values without ambiguity.
//
// What's stripped (when matching the listed default):
//
//	listen_addr            "127.0.0.1:1080"
//	server.url             ""           (always empty in appsscript mode)
//	transport.options.google_host
//	                       "216.239.38.120:443"
//	script_keys[].account  ""           (purely cosmetic stats label)
//	socks                  {}           (empty object)
//
// What's NOT stripped:
//
//	transport.options.sni  — the appsscript transport's default is
//	                         ["www.google.com"] (single host) but
//	                         operators routinely configure 3-host
//	                         rotations; eliding would silently change
//	                         the disguise behaviour.
func stripLinkDefaults(m map[string]any) {
	// listen_addr default
	if m["listen_addr"] == "127.0.0.1:1080" {
		delete(m, "listen_addr")
	}
	// server.url: always empty in appsscript mode; default-zero
	// otherwise. Either way redundant in the link.
	if server, ok := m["server"].(map[string]any); ok {
		if server["url"] == "" {
			delete(server, "url")
		}
	}
	// transport.options stripping
	if t, ok := m["transport"].(map[string]any); ok {
		if opts, ok := t["options"].(map[string]any); ok {
			if opts["google_host"] == "216.239.38.120:443" {
				delete(opts, "google_host")
			}
			if keys, ok := opts["script_keys"].([]any); ok {
				for _, k := range keys {
					if km, ok := k.(map[string]any); ok {
						if km["account"] == "" {
							delete(km, "account")
						}
					}
				}
			}
			if len(opts) == 0 {
				delete(t, "options")
			}
		}
	}
	// Empty socks block adds ~10 chars for nothing
	if s, ok := m["socks"].(map[string]any); ok {
		if len(s) == 0 {
			delete(m, "socks")
		}
	}
}

// fillLinkDefaults restores defaults the encoder elided. Only fields
// whose absence would fail ClientConfig.Validate need to be filled
// here; transport-construction defaults (google_host, etc.) are
// applied later by the transport package and don't need the loader's
// help.
func fillLinkDefaults(cfg *ClientConfig) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:1080"
	}
}

// EncodeLink serializes a ClientConfig into a `bg://...` share-link.
// The link is a single string the operator can paste into a chat,
// email, or QR code; the recipient imports it with
// `beacongate-client -import "<link>"`.
//
// Default-valued fields are stripped from the encoded JSON to keep
// the link short — see stripLinkDefaults for the list. The decoder
// transparently restores them via fillLinkDefaults.
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
	// Walk the marshaled JSON as a generic map to strip default-valued
	// fields, then re-marshal the trimmed map. Cheaper than maintaining
	// a parallel "compact" struct definition; the encoder is not on a
	// hot path so the round-trip-through-map is fine.
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("share_link: pre-strip unmarshal: %w", err)
	}
	stripLinkDefaults(m)
	body, err = json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("share_link: post-strip marshal: %w", err)
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
	// Restore defaults the encoder elided. Backwards-compatible with
	// older "full" links (where listen_addr is already populated): the
	// fill is a no-op when the field is non-empty.
	fillLinkDefaults(&cfg)
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
