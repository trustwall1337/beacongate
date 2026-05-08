// Package policy implements server-side outbound policy: a rules engine the
// tunnel handler consults before dialing. Rules are structured data, not
// code, so they can be edited by operators and persisted in a store.
package policy

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Action is what the policy decides for a matched target.
type Action string

const (
	ActionBlock   Action = "block"
	ActionAllow   Action = "allow"
	ActionLogOnly Action = "log-only"
)

// MatchKind selects how a rule matches a target.
type MatchKind string

const (
	MatchExactHost    MatchKind = "exact-host"
	MatchWildcardHost MatchKind = "wildcard-host"
	MatchExactIP      MatchKind = "exact-ip"
	MatchCIDR         MatchKind = "cidr"
)

// Rule is a single declarative entry. Rules are evaluated in priority order
// by the Engine.
type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Category  string    `json:"category,omitempty"`
	Source    string    `json:"source,omitempty"`
	Enabled   bool      `json:"enabled"`
	Action    Action    `json:"action"`
	Match     MatchKind `json:"match"`
	Pattern   string    `json:"pattern"`
	Port      uint16    `json:"port,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

var (
	ErrInvalidRule = errors.New("policy: invalid rule")
)

// Validate checks the rule fields for shape correctness without consulting a
// matcher. The caller should call this on any rule that arrives from a user.
func (r *Rule) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("%w: id required", ErrInvalidRule)
	}
	switch r.Action {
	case ActionBlock, ActionAllow, ActionLogOnly:
	default:
		return fmt.Errorf("%w: unknown action %q", ErrInvalidRule, r.Action)
	}
	if r.Pattern == "" {
		return fmt.Errorf("%w: pattern required", ErrInvalidRule)
	}
	switch r.Match {
	case MatchExactHost:
		// Must be a valid hostname (no spaces, no slashes).
		if strings.ContainsAny(r.Pattern, " /") {
			return fmt.Errorf("%w: invalid exact-host pattern", ErrInvalidRule)
		}
	case MatchWildcardHost:
		if !strings.HasPrefix(r.Pattern, "*.") || len(r.Pattern) <= 2 {
			return fmt.Errorf("%w: wildcard-host pattern must look like *.example.com", ErrInvalidRule)
		}
	case MatchExactIP:
		if net.ParseIP(r.Pattern) == nil {
			return fmt.Errorf("%w: invalid IP %q", ErrInvalidRule, r.Pattern)
		}
	case MatchCIDR:
		if _, _, err := net.ParseCIDR(r.Pattern); err != nil {
			return fmt.Errorf("%w: invalid CIDR %q: %v", ErrInvalidRule, r.Pattern, err)
		}
	default:
		return fmt.Errorf("%w: unknown match kind %q", ErrInvalidRule, r.Match)
	}
	return nil
}
