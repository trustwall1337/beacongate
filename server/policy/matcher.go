package policy

import (
	"net"
	"strings"
)

// Matches reports whether the rule applies to (host, port). The host can be
// a hostname or an IP literal; IP-typed rules ignore non-IP hosts and vice
// versa.
func (r *Rule) Matches(host string, port uint16) bool {
	if !r.Enabled {
		return false
	}
	if r.Port != 0 && r.Port != port {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	switch r.Match {
	case MatchExactHost:
		return strings.EqualFold(host, r.Pattern)
	case MatchWildcardHost:
		suffix := strings.ToLower(r.Pattern[1:]) // ".example.com"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	case MatchExactIP:
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		return ip.Equal(net.ParseIP(r.Pattern))
	case MatchCIDR:
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		_, ipnet, err := net.ParseCIDR(r.Pattern)
		if err != nil {
			return false
		}
		return ipnet.Contains(ip)
	}
	return false
}
