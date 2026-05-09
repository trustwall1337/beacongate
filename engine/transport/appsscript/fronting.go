package appsscript

import (
	"context"
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
//
// **uTLS:** as of v1.1.0 the TLS handshake is performed by
// github.com/refraction-networking/utls presenting a pinned Chrome
// ClientHello fingerprint (see utls_dial.go). This is the
// censorship-evasion property — without uTLS, the handshake
// fingerprints as "Go" and is detectable on the wire even though the
// destination is a real Google IP.
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

// newFrontedClient builds a single *http.Client that dials googleIP and
// presents sniHost in the TLS ClientHello. The HTTP Host header on each
// request is left to Go's stdlib (= URL.Host), which for an Apps Script
// URL is `script.google.com` — exactly the target we want Google's edge
// routing to receive.
//
// **TLS layer:** uTLS does the handshake (pinned Chrome 131 fingerprint,
// see utls_dial.go), so a wire observer sees what looks like a real
// Chrome browser talking to Google. TLS 1.3 minimum is enforced inside
// the uTLS Config; downgrade attempts MUST fail rather than fall back
// to TLS 1.2.
//
// ALPN advertises ["h2", "http/1.1"] in the ClientHello — same as real
// Chrome. After handshake, Go's http.Transport detects the negotiated
// protocol via our utlsConnWrapper.ConnectionState() and routes through
// http2.Transport (h2) or net/http's HTTP/1.1 path accordingly.
func newFrontedClient(googleIP, sniHost string, requestTimeout time.Duration, sessionCache uTLSSessionCache) *http.Client {
	transport := &http.Transport{
		// DialTLSContext is the integration point for uTLS: when set,
		// http.Transport calls this for HTTPS connections instead of
		// doing its own TLS handshake. We hand back a wrapper conn that
		// exposes tls.ConnectionState so http.Transport's HTTP/2
		// detection still works.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialUTLS(ctx, googleIP, sniHost, sessionCache, []string{"h2", "http/1.1"})
		},
		// TLSClientConfig is unused once DialTLSContext is set, but
		// leave it nil rather than constructing an unused Config —
		// avoids confusion about which TLS path is active.
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
