package appsscript

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
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
//
// **Transport routing (v1.1.0):** Apps Script via Google's edge always
// negotiates HTTP/2. We therefore drive requests through
// `http2.Transport` directly — bypassing `net/http.Transport` and its
// concrete `*tls.Conn` type assertion that's incompatible with uTLS's
// `*utls.UConn` since Go 1.21. The uTLS handshake advertises both "h2"
// and "http/1.1" in ALPN (Chrome-realistic) and verifies "h2" was
// selected; if Google ever falls back to HTTP/1.1 we error rather than
// silently degrade through an incompatible code path.
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
// round-robin selection state. Each client uses its own uTLS session
// cache (resumption is bound to SNI server-side).
type httpClientPool struct {
	clients []*http.Client
	hosts   []string
	caches  *utlsCacheRegistry
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
	caches := newUTLSCacheRegistry()
	clients := make([]*http.Client, len(hosts))
	for i, sni := range hosts {
		clients[i] = newFrontedClient(cfg.GoogleIP, sni, requestTimeout, caches.get(sni))
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

// newFrontedClient builds a single *http.Client whose underlying
// transport is `http2.Transport`, using a custom DialTLSContext that
// performs the uTLS handshake. The HTTP Host header on each request is
// left to Go's stdlib (= URL.Host), which for an Apps Script URL is
// `script.google.com` — exactly the target we want Google's edge
// routing to receive.
//
// **Why http2.Transport and not http.Transport:** Go's net/http
// transport detects HTTP/2 via a concrete `pconn.conn.(*tls.Conn)`
// type assertion (transport.go:1763 in Go 1.21+). Our uTLS conn is
// a `*utls.UConn`, not a `*tls.Conn`, so the assertion fails,
// `pconn.tlsState` stays nil, and the HTTP/2 dispatch path is
// silently skipped — leaving Go to parse Google's HTTP/2 SETTINGS
// frame as if it were HTTP/1.1. http2.Transport.DialTLSContext
// avoids this problem entirely: it only knows it got a net.Conn
// after a successful TLS handshake, then drives h2 directly.
//
// **TLS layer:** uTLS does the handshake (pinned Chrome 131 fingerprint,
// see utls_dial.go), so a wire observer sees what looks like a real
// Chrome browser talking to Google. TLS 1.3 minimum is enforced inside
// the uTLS Config; downgrade attempts MUST fail rather than fall back
// to TLS 1.2.
//
// ALPN advertises ["h2", "http/1.1"] in the ClientHello — same as real
// Chrome. After handshake, the dialer asserts "h2" was selected;
// if not (which should be impossible against modern Google fronts),
// it errors rather than handing a partially-negotiated conn to http2.
func newFrontedClient(googleIP, sniHost string, requestTimeout time.Duration, sessionCache uTLSSessionCache) *http.Client {
	transport := &http2.Transport{
		// http2 calls this for every new connection. We ignore the
		// `network` and `addr` it passes (those derive from the URL)
		// and use our SNI-fronted googleIP instead. The `_ *tls.Config`
		// is also ignored — uTLS uses its own utls.Config built in
		// dialUTLS.
		DialTLSContext: func(ctx context.Context, _ string, _ string, _ *tls.Config) (net.Conn, error) {
			conn, err := dialUTLS(ctx, googleIP, sniHost, sessionCache, []string{"h2", "http/1.1"})
			if err != nil {
				return nil, err
			}
			// Verify h2 was negotiated. If Google somehow returns
			// http/1.1, we've configured ALPN to allow it but we
			// don't have an HTTP/1.1 path — error rather than
			// hand a non-h2 conn to http2.Transport.
			alpn := conn.UConn.ConnectionState().NegotiatedProtocol
			if alpn != "h2" {
				_ = conn.Close()
				return nil, fmt.Errorf("appsscript: ALPN %q is not \"h2\"; http2.Transport requires h2", alpn)
			}
			return conn, nil
		},
		// Idle ping: a black-holed h2 connection would otherwise linger
		// until the kernel TCP keepalive fires (~2 hours by default),
		// stalling in-flight requests. 30s ReadIdleTimeout + 15s
		// PingTimeout is conservative enough to survive momentary
		// network blips without false-positive disconnects.
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     15 * time.Second,
		// Raise the max DATA frame size from the spec default 16 KiB to
		// 1 MiB — base64-expanded batches can be ~10 MiB and the
		// framing overhead with default size is significant.
		MaxReadFrameSize: 1 << 20,
	}

	return &http.Client{Transport: transport, Timeout: requestTimeout}
}

// prewarmFrontedClients fires one TLS dial per SNI host in the
// background to populate each SNI's session ticket cache.
//
// **uTLS:** the prewarm uses the same dialUTLS path the real requests
// use — same fingerprint, same ALPN, same session cache. If we used
// stdlib crypto/tls here, the cached ticket might not resume cleanly
// against the uTLS handshake on the next real request (different code
// paths can yield ticket binding mismatches even when the wire format
// agrees).
//
// Critical detail (carried over from the stdlib version): in TLS 1.3 the
// server sends NewSessionTicket *after* the handshake completes, on the
// data channel. Closing immediately after HandshakeContext drops the
// ticket on the floor. To capture the ticket we issue a tiny read with
// a short deadline; the read errors out on deadline but by then the
// post-handshake message has been consumed and the ticket stored in the
// cache.
func prewarmFrontedClients(googleIP string, sniHosts []string, caches *utlsCacheRegistry) {
	const (
		ticketWindow  = 500 * time.Millisecond
		overallBudget = 5 * time.Second
	)
	for _, sni := range sniHosts {
		go func(sniHost string) {
			ctx, cancel := context.WithTimeout(context.Background(), overallBudget)
			defer cancel()
			conn, err := dialUTLS(ctx, googleIP, sniHost, caches.get(sniHost), []string{"h2", "http/1.1"})
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(ticketWindow))
			var buf [1]byte
			_, _ = conn.Read(buf[:])
		}(sni)
	}
}

// Compile-time assertion: uTLS UConn satisfies utls.UConn-implementing
// the net.Conn methods we need (the http2 transport only requires
// net.Conn; nothing extra).
var _ utlsConnReturn = (*utls.UConn)(nil)

// utlsConnReturn documents the conn shape dialUTLS hands to
// http2.Transport. We deliberately do NOT require ConnectionState()
// here — http2.Transport doesn't need it for h2 dispatch (h2 is
// detected by the dialer, not by the transport via type assertion).
type utlsConnReturn interface {
	net.Conn
}
