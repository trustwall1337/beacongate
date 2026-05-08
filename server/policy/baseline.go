package policy

import "time"

// Baseline returns the bundled default policy. The list focuses on
// abuse-prone categories the project commits to baseline-blocking; the data
// is kept here as plain values rather than mixed into the matcher.
//
// This list is intentionally conservative; operators are expected to extend
// it with environment-specific rules.
func Baseline() []Rule {
	now := time.Now().UTC()
	return []Rule{
		{
			ID:        "baseline.torrent.tracker.opentrackr",
			Name:      "Block opentrackr.org tracker",
			Category:  "torrent",
			Source:    "baseline",
			Enabled:   true,
			Action:    ActionBlock,
			Match:     MatchWildcardHost,
			Pattern:   "*.opentrackr.org",
			Reason:    "torrent tracker (baseline)",
			UpdatedAt: now,
		},
		{
			ID:        "baseline.torrent.tracker.openbittorrent",
			Name:      "Block openbittorrent.com tracker",
			Category:  "torrent",
			Source:    "baseline",
			Enabled:   true,
			Action:    ActionBlock,
			Match:     MatchWildcardHost,
			Pattern:   "*.openbittorrent.com",
			Reason:    "torrent tracker (baseline)",
			UpdatedAt: now,
		},
		{
			ID:        "baseline.torrent.tracker.thepiratebay",
			Name:      "Block thepiratebay variants",
			Category:  "torrent",
			Source:    "baseline",
			Enabled:   true,
			Action:    ActionBlock,
			Match:     MatchWildcardHost,
			Pattern:   "*.thepiratebay.org",
			Reason:    "torrent index (baseline)",
			UpdatedAt: now,
		},
		{
			ID:        "baseline.torrent.port.bittorrent",
			Name:      "Block bittorrent default port range",
			Category:  "torrent",
			Source:    "baseline",
			Enabled:   true,
			Action:    ActionBlock,
			Match:     MatchCIDR,
			Pattern:   "0.0.0.0/0",
			Port:      6881,
			Reason:    "common bittorrent port (baseline)",
			UpdatedAt: now,
		},
	}
}
