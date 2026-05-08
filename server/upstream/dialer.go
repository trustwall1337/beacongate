// Package upstream encapsulates how the BeaconGate server opens outbound
// TCP connections on behalf of an authenticated client session. The
// abstraction exists so policy enforcement and DNS caching live in one
// place, separate from the tunnel handler.
package upstream

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"
)

type Dialer interface {
	Dial(ctx context.Context, host string, port uint16) (net.Conn, error)
}

type NetDialer struct {
	Resolver      *DNSCache
	DialTimeout   time.Duration
	DefaultDialer net.Dialer
	Safety        SafetyConfig
}

func NewNetDialer(timeout time.Duration) *NetDialer {
	return &NetDialer{
		DialTimeout:   timeout,
		DefaultDialer: net.Dialer{Timeout: timeout},
		Resolver:      NewDNSCache(60 * time.Second),
	}
}

// Dial resolves host, validates the resulting IP against the safety policy
// (SSRF guard), and only then opens the TCP connection. Hostnames that
// resolve to multiple IPs are rejected if any candidate is unsafe — we
// never silently fall back to a "different" IP.
func (d *NetDialer) Dial(ctx context.Context, host string, port uint16) (net.Conn, error) {
	ip, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := IsUnsafe(ip, d.Safety); err != nil {
		return nil, err
	}
	dialer := d.DefaultDialer
	if d.DialTimeout > 0 && dialer.Timeout == 0 {
		dialer.Timeout = d.DialTimeout
	}
	hostport := net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	return dialer.DialContext(ctx, "tcp", hostport)
}

// resolve consults the DNS cache or, on miss, performs a single resolver
// lookup. It returns the canonical IP that will be dialed; the safety
// gate runs against this exact value so cache poisoning still has to win
// the safety check.
func (d *NetDialer) resolve(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	if d.Resolver != nil {
		if cached, ok := d.Resolver.Lookup(host); ok {
			if ip := net.ParseIP(cached); ip != nil {
				return ip, nil
			}
		}
	}
	r := net.Resolver{}
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errResolveEmpty
	}
	// Validate every returned address; a hostname pointing at private IPs
	// (DNS rebinding) must not be allowed simply because we picked addrs[0].
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if err := IsUnsafe(ip, d.Safety); err != nil {
			return nil, err
		}
	}
	first := net.ParseIP(addrs[0])
	if first == nil {
		return nil, errResolveEmpty
	}
	if d.Resolver != nil {
		d.Resolver.Set(host, first.String())
	}
	return first, nil
}

var errResolveEmpty = errors.New("upstream: resolver returned no addresses")
