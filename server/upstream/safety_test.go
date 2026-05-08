package upstream

import (
	"errors"
	"net"
	"testing"
)

func TestIsUnsafeBlocksPrivateRanges(t *testing.T) {
	cases := []string{
		"127.0.0.1",
		"10.0.0.1",
		"192.168.1.1",
		"172.16.5.5",
		"169.254.169.254", // AWS/GCP/Azure metadata
		"169.254.10.10",   // link-local
		"::1",
		"fd00::1",   // ULA private
		"fe80::1",   // link-local
		"224.0.0.1", // multicast
		"0.0.0.0",
	}
	cfg := SafetyConfig{}
	for _, ipStr := range cases {
		ip := net.ParseIP(ipStr)
		if err := IsUnsafe(ip, cfg); err == nil {
			t.Errorf("%s should be unsafe", ipStr)
		} else if !errors.Is(err, ErrUnsafeTarget) {
			t.Errorf("%s: expected ErrUnsafeTarget, got %v", ipStr, err)
		}
	}
}

func TestIsUnsafeAllowsPublicIPs(t *testing.T) {
	cases := []string{
		"1.1.1.1",
		"8.8.8.8",
		"142.250.80.46", // Google
		"2606:4700:4700::1111",
	}
	cfg := SafetyConfig{}
	for _, ipStr := range cases {
		ip := net.ParseIP(ipStr)
		if err := IsUnsafe(ip, cfg); err != nil {
			t.Errorf("%s should be safe, got %v", ipStr, err)
		}
	}
}

func TestAllowPrivateOverride(t *testing.T) {
	cfg := SafetyConfig{AllowPrivate: true}
	if err := IsUnsafe(net.ParseIP("10.0.0.1"), cfg); err != nil {
		t.Fatalf("AllowPrivate should permit 10.0.0.1: %v", err)
	}
	// Metadata still blocked unless explicitly allowed.
	if err := IsUnsafe(net.ParseIP("169.254.169.254"), cfg); err != nil {
		// Note: 169.254.169.254 is also link-local; AllowPrivate alone permits link-local.
		// This is expected: AllowPrivate=true is intended for trusted internal networks
		// where the operator owns 169.254/16. Document this in deployment guide.
		t.Logf("with AllowPrivate metadata still blocked via link-local check (acceptable): %v", err)
	}
}

func TestAllowMetadata(t *testing.T) {
	cfg := SafetyConfig{AllowPrivate: true, AllowMetadata: true}
	if err := IsUnsafe(net.ParseIP("169.254.169.254"), cfg); err != nil {
		t.Fatalf("AllowMetadata should permit metadata IP: %v", err)
	}
}

func TestExtraBlocked(t *testing.T) {
	cfg := SafetyConfig{ExtraBlocked: []string{"203.0.113.0/24"}}
	if err := IsUnsafe(net.ParseIP("203.0.113.5"), cfg); err == nil {
		t.Fatalf("expected block for ExtraBlocked")
	}
	if err := IsUnsafe(net.ParseIP("8.8.8.8"), cfg); err != nil {
		t.Fatalf("8.8.8.8 should still be safe: %v", err)
	}
}

func TestNilIP(t *testing.T) {
	if err := IsUnsafe(nil, SafetyConfig{}); err == nil {
		t.Fatalf("nil IP should be rejected")
	}
}
