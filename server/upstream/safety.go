package upstream

import (
	"errors"
	"fmt"
	"net"
)

// ErrUnsafeTarget is returned when the requested target resolves to an
// address that the safety policy refuses to dial. It is the single
// SSRF-prevention chokepoint — every dial in the server runtime goes
// through this gate.
var ErrUnsafeTarget = errors.New("upstream: target address is unsafe")

// SafetyConfig controls the SSRF guard. The defaults block private IP
// ranges, loopback, link-local, multicast, and well-known cloud metadata
// addresses. Operators on locked-down networks where these are legitimate
// targets can opt in with AllowPrivate.
type SafetyConfig struct {
	// AllowPrivate disables blocking of RFC1918, loopback, link-local, and
	// other "internal" address classes. Default false.
	AllowPrivate bool
	// AllowMetadata disables blocking of cloud metadata IPs (169.254.169.254,
	// fd00:ec2::254). Default false.
	AllowMetadata bool
	// ExtraBlocked is an additional list of CIDRs to block.
	ExtraBlocked []string
}

// IsUnsafe returns nil if it is safe to dial ip under cfg, or an error
// explaining why not. Unsafe addresses must never be dialed.
func IsUnsafe(ip net.IP, cfg SafetyConfig) error {
	if ip == nil {
		return fmt.Errorf("%w: nil ip", ErrUnsafeTarget)
	}

	if !cfg.AllowMetadata {
		if ip.Equal(net.IPv4(169, 254, 169, 254)) {
			return fmt.Errorf("%w: cloud metadata address", ErrUnsafeTarget)
		}
	}
	if !cfg.AllowPrivate {
		if ip.IsLoopback() {
			return fmt.Errorf("%w: loopback", ErrUnsafeTarget)
		}
		if ip.IsPrivate() {
			return fmt.Errorf("%w: private network", ErrUnsafeTarget)
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("%w: link-local", ErrUnsafeTarget)
		}
		if ip.IsMulticast() {
			return fmt.Errorf("%w: multicast", ErrUnsafeTarget)
		}
		if ip.IsUnspecified() {
			return fmt.Errorf("%w: unspecified address", ErrUnsafeTarget)
		}
		if ip.IsInterfaceLocalMulticast() {
			return fmt.Errorf("%w: interface-local multicast", ErrUnsafeTarget)
		}
	}
	for _, cidr := range cfg.ExtraBlocked {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if ipnet.Contains(ip) {
			return fmt.Errorf("%w: matches blocked range %s", ErrUnsafeTarget, cidr)
		}
	}
	return nil
}
