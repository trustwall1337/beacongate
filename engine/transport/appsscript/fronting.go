package appsscript

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

// FrontingConfig describes how to make a TCP connection look like
// ordinary HTTPS traffic to Google: dial GoogleIP directly, do a TLS
// handshake with a Google-owned SNI hostname, then let Go's stdlib set
// HTTP Host = URL.Host (= script.google.com) so Google's edge routing
// picks up the request.
//
// Multiple SNIHosts create independent http.Clients with separate TLS
// session caches, since a TLS resumption ticket from one Google front
// (e.g. www.google.com) does not resume against a different front (e.g.
// mail.google.com).
type FrontingConfig struct {
	// GoogleIP is the TCP-layer destination, in "ip:port" form. When
	// empty, the dialer falls back to system DNS for the URL's host —
	// useful for tests that want to dial a fake server without spoofing.
	GoogleIP string
	// SNIHosts is the rotation list. Defaults to ["www.google.com"]
	// when empty.
	SNIHosts []string
}

// httpClientPool holds one *http.Client per configured SNI host, plus
// round-robin selection state. Each client uses its own TLS session
// cache (resumption is bound to SNI server-side).
type httpClientPool struct {
	clients []*http.Client
	hosts   []string
	caches  map[string]tls.ClientSessionCache
	next    atomic.Uint64
}

// newHTTPClientPool constructs the pool. requestTimeout caps every HTTP
// request; choose it >= the server's long-poll window plus slack.
//
// Per Workstream A9 invariant #7, the pool is built eagerly at
// construction time and prewarm fires asynchronously immediately so
// the first user-facing request can resume rather than handshake.
func newHTTPClientPool(cfg FrontingConfig, requestTimeout time.Duration) *httpClientPool {
	hosts := cfg.SNIHosts
	if len(hosts) == 0 {
		hosts = []string{"www.google.com"}
	}
	caches := make(map[string]tls.ClientSessionCache, len(hosts))
	for _, sni := range hosts {
		if _, ok := caches[sni]; !ok {
			caches[sni] = tls.NewLRUClientSessionCache(8)
		}
	}
	clients := make([]*http.Client, len(hosts))
	for i, sni := range hosts {
		clients[i] = newFrontedClient(cfg.GoogleIP, sni, requestTimeout, caches[sni])
	}
	pool := &httpClientPool{
		clients: clients,
		hosts:   append([]string(nil), hosts...),
		caches:  caches,
	}
	// Fire prewarm in the background; it is best-effort and consumes no
	// Apps Script quota (raw TLS handshakes only). On failure the
	// goroutine returns silently and the first real request pays the
	// full handshake cost — correct fallback, just slower.
	go prewarmFrontedClients(cfg.GoogleIP, hosts, caches)
	return pool
}

// pick returns the next *http.Client in round-robin order. Atomic
// counter, lock-free.
func (p *httpClientPool) pick() *http.Client {
	if len(p.clients) == 0 {
		return nil
	}
	idx := int(p.next.Add(1)-1) % len(p.clients)
	return p.clients[idx]
}

// newFrontedClient builds a single *http.Client that dials googleIP and
// presents sniHost in the TLS ClientHello. The HTTP Host header on each
// request is left to Go's stdlib (= URL.Host), which for an Apps Script
// URL is `script.google.com` — exactly the target we want Google's edge
// routing to receive.
//
// TLS 1.3 minimum (downgrade attempts MUST fail rather than fall back
// to TLS 1.2 which would let an attacker strip cipher protections).
// Session ticket cache enables resumption; ALPN pinned to ["h2",
// "http/1.1"] so the resumption ticket — bound to ALPN — is reusable.
func newFrontedClient(googleIP, sniHost string, requestTimeout time.Duration, sessionCache tls.ClientSessionCache) *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if googleIP != "" {
				// Dial the configured Google IP regardless of what the
				// HTTP layer thinks the address should be. This is the
				// SNI-fronting trick: TCP destination locked to Google,
				// HTTP target is whatever the URL says.
				return dialer.DialContext(ctx, "tcp", googleIP)
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			ServerName:         sniHost,
			MinVersion:         tls.VersionTLS13,
			ClientSessionCache: sessionCache,
			NextProtos:         []string{"h2", "http/1.1"},
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		WriteBufferSize:       64 * 1024,
		ReadBufferSize:        64 * 1024,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	// HTTP/2 idle ping detection: a black-holed h2 connection otherwise
	// lingers until the kernel TCP keepalive fires (~2 hours by default),
	// stalling in-flight requests. 30s ping timeout matches Goose's tuning.
	if h2t, err := http2.ConfigureTransports(transport); err == nil && h2t != nil {
		h2t.ReadIdleTimeout = 30 * time.Second
		h2t.PingTimeout = 15 * time.Second
		// Raise the max DATA frame size from the spec default 16 KiB to
		// 1 MiB — base64-expanded batches can be ~10 MiB and the framing
		// overhead with default size is significant.
		h2t.MaxReadFrameSize = 1 << 20
	}

	return &http.Client{Transport: transport, Timeout: requestTimeout}
}

// prewarmFrontedClients fires one TLS dial per SNI host in the
// background to populate each SNI's session ticket cache.
//
// Critical detail: in TLS 1.3 the server sends NewSessionTicket *after*
// the handshake completes, on the data channel. Closing immediately
// after HandshakeContext drops the ticket on the floor. To capture the
// ticket we issue a tiny read with a short deadline; the read errors
// out on deadline but by then crypto/tls has consumed the
// post-handshake message and stored the ticket in the cache. (Direct
// port of GooseRelayVPN's mechanism.)
func prewarmFrontedClients(googleIP string, sniHosts []string, caches map[string]tls.ClientSessionCache) {
	const (
		dialTimeout   = 3 * time.Second
		ticketWindow  = 500 * time.Millisecond
		overallBudget = 5 * time.Second
	)
	dialer := &net.Dialer{Timeout: dialTimeout}
	for _, sni := range sniHosts {
		go func(sniHost string, cache tls.ClientSessionCache) {
			ctx, cancel := context.WithTimeout(context.Background(), overallBudget)
			defer cancel()
			addr := googleIP
			if addr == "" {
				addr = net.JoinHostPort(sniHost, "443")
			}
			rawConn, err := dialer.DialContext(ctx, "tcp", addr)
			if err != nil {
				return
			}
			defer func() { _ = rawConn.Close() }()
			tlsConn := tls.Client(rawConn, &tls.Config{
				ServerName:         sniHost,
				MinVersion:         tls.VersionTLS13,
				ClientSessionCache: cache,
				NextProtos:         []string{"h2", "http/1.1"},
			})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				return
			}
			_ = tlsConn.SetReadDeadline(time.Now().Add(ticketWindow))
			var buf [1]byte
			_, _ = tlsConn.Read(buf[:])
		}(sni, caches[sni])
	}
}
