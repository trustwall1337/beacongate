// Package upstream encapsulates how the BeaconGate server opens outbound
// TCP connections on behalf of an authenticated client session. The
// abstraction exists so policy enforcement and DNS caching live in one
// place, separate from the tunnel handler.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/net/proxy"
)

type Dialer interface {
	Dial(ctx context.Context, host string, port uint16) (net.Conn, error)
}

type NetDialer struct {
	Resolver      *DNSCache
	DialTimeout   time.Duration
	DefaultDialer net.Dialer
	Safety        SafetyConfig

	// ProxyDialer routes the actual TCP dial through a SOCKS5 proxy
	// (e.g. Cloudflare WARP) when set. The SSRF guard still runs
	// against the *resolved destination IP* — we resolve locally
	// before handing the (resolved) hostport to the proxy, so a
	// compromised client cannot use the proxy to bypass our SSRF
	// gate. Trade-off: DNS-via-proxy (target sites see only the
	// proxy IP, never the VPS resolver) is sacrificed; only the
	// dial itself goes through the proxy. The user-visible
	// Cloudflare-egress-IP property is preserved (target servers
	// see the proxy's egress IP for the TCP connection).
	ProxyDialer proxy.Dialer
}

// NewNetDialer constructs a server-side outbound dialer. If
// upstreamProxy is non-empty, it must be a "socks5://host:port" URL;
// outbound dials are routed through that proxy after the SSRF guard
// runs locally on the resolved destination IP.
func NewNetDialer(timeout time.Duration, upstreamProxy string) (*NetDialer, error) {
	d := &NetDialer{
		DialTimeout:   timeout,
		DefaultDialer: net.Dialer{Timeout: timeout},
		Resolver:      NewDNSCache(60 * time.Second),
	}
	if upstreamProxy != "" {
		u, err := url.Parse(upstreamProxy)
		if err != nil {
			return nil, fmt.Errorf("upstream proxy URL %q: %w", upstreamProxy, err)
		}
		if u.Scheme != "socks5" {
			return nil, fmt.Errorf("upstream proxy: only socks5:// scheme supported (got %q)", u.Scheme)
		}
		pd, err := proxy.FromURL(u, &d.DefaultDialer)
		if err != nil {
			return nil, fmt.Errorf("upstream proxy dialer: %w", err)
		}
		d.ProxyDialer = pd
	}
	return d, nil
}

// contextDialer is the optional context-aware extension to
// proxy.Dialer. The stdlib SOCKS5 implementation in golang.org/x/net
// satisfies this since Go 1.16; we type-assert at call time so old
// proxy.Dialer implementations still work via the basic Dial method.
type contextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Dial resolves host, validates the resulting IP against the safety policy
// (SSRF guard), and only then opens the TCP connection. Hostnames that
// resolve to multiple IPs are rejected if any candidate is unsafe — we
// never silently fall back to a "different" IP.
//
// When ProxyDialer is set, the (resolved, safety-checked) hostport is
// dialed through the SOCKS5 proxy rather than directly. SSRF runs
// locally either way.
//
// The returned connection has Nagle's algorithm disabled and TCP keepalive
// armed. Nagle adds up to 40 ms of kernel buffering per write batch, which
// directly inflates TLS handshake-record latency; keepalive prevents NAT
// or upstream-edge silent reaper kills on long-lived idle sessions.
func (d *NetDialer) Dial(ctx context.Context, host string, port uint16) (net.Conn, error) {
	ip, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := IsUnsafe(ip, d.Safety); err != nil {
		return nil, err
	}
	hostport := net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	var conn net.Conn
	switch {
	case d.ProxyDialer != nil:
		if cd, ok := d.ProxyDialer.(contextDialer); ok {
			conn, err = cd.DialContext(ctx, "tcp", hostport)
		} else {
			conn, err = d.ProxyDialer.Dial("tcp", hostport)
		}
	default:
		dialer := d.DefaultDialer
		if d.DialTimeout > 0 && dialer.Timeout == 0 {
			dialer.Timeout = d.DialTimeout
		}
		conn, err = dialer.DialContext(ctx, "tcp", hostport)
	}
	if err != nil {
		return nil, err
	}
	tuneUpstream(conn)
	return conn, nil
}

// tuneUpstream applies low-latency TCP settings to a freshly-dialed
// upstream connection. SOCKS5 proxy wrappers sometimes return a non-
// TCPConn; in that case we skip silently rather than fail the dial.
func tuneUpstream(conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetNoDelay(true)
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(30 * time.Second)
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
