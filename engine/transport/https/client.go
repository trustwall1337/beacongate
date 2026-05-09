// Package https implements a direct HTTPS POST transport for BeaconGate.
//
// IMPORTANT: this is a generic HTTPS transport, NOT a censorship-evasion
// path. It opens a direct TLS connection to the URL the operator
// configured and posts encrypted batches there. A network observer sees
// ordinary HTTPS to whatever hostname is in the configured URL.
//
// The censorship-evasion path is implemented separately by the
// `engine/transport/appsscript` package. That transport tunnels through
// Google Apps Script so the network path terminates at a real Google IP
// with SNI=www.google.com.
//
// All this package does: dumb forwarder, opaque payload, optional
// FrontingHost header override, optional pinned TLS roots, TLS 1.3
// minimum.
//
// History: this package was previously named `google` (since v1.0), in
// the aspirational expectation that it would become the Google-fronted
// path. It never did — it stayed a generic HTTPS transport. v1.1
// renamed it to match reality and added the real Apps-Script-tunneled
// transport as a sibling package.
package https

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/trustwall1337/beacongate/engine/transport"
)

const (
	defaultTimeout    = 30 * time.Second
	defaultUserAgent  = "beacongate-https/1.1"
	contentTypeOpaque = "application/octet-stream"
)

// Config controls the Google HTTPS transport.
type Config struct {
	// URL is the endpoint that accepts encrypted batches via POST.
	URL string
	// HealthURL is an optional GET endpoint that reports server health.
	// When empty, Diagnose performs a HEAD request against URL.
	HealthURL string
	// FrontingHost, when non-empty, overrides the HTTP Host header. The
	// underlying TCP/TLS connection still resolves URL's hostname; only
	// the Host header carries FrontingHost.
	FrontingHost string
	// Timeout caps a single Roundtrip. Zero means defaultTimeout.
	Timeout time.Duration
	// UserAgent overrides the default UA. Zero value uses defaultUserAgent.
	UserAgent string
	// PinnedRootsPEM, when non-empty, restricts certificate verification
	// to this PEM-encoded bundle. Use this for cert pinning when the
	// operator controls the relay's TLS certificate. Empty means "use
	// the system root pool" (still TLS 1.3 minimum).
	PinnedRootsPEM []byte
	// HTTPClient lets callers (and tests) inject a pre-configured client.
	// When nil, a hardened default client (TLS 1.3 minimum) is used.
	HTTPClient *http.Client
}

// Client is a transport.ClientTransport implementation backed by HTTPS.
type Client struct {
	cfg    Config
	http   *http.Client
	closed bool
}

// New constructs the transport. URL must be non-empty. The default HTTP
// client enforces TLS 1.3 as the minimum protocol version; callers that
// need to talk to TLS-1.2-only hosts must supply their own HTTPClient.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("https transport: URL is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		tlsCfg, err := buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		httpClient = &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				TLSClientConfig:       tlsCfg,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          16,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	}
	return &Client{cfg: cfg, http: httpClient}, nil
}

// buildTLSConfig constructs a hardened tls.Config: TLS 1.3 minimum, plus
// a pinned root pool if the caller supplied one. SSL/TLS downgrade is the
// single most common way a network attacker tries to break a tunnel; this
// closes that door.
func buildTLSConfig(cfg Config) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	if len(cfg.PinnedRootsPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.PinnedRootsPEM) {
			return nil, errors.New("https transport: PinnedRootsPEM contained no usable certificates")
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}

// Roundtrip POSTs batch to the configured URL and returns the response body.
func (c *Client) Roundtrip(ctx context.Context, batch []byte) ([]byte, error) {
	if c.closed {
		return nil, transport.ErrClosed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(batch))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", transport.ErrInvalidResponse, err)
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", contentTypeOpaque)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", transport.ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("%w: read body: %v", transport.ErrUnreachable, readErr)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if len(body) == 0 {
			return nil, fmt.Errorf("%w: empty body", transport.ErrInvalidResponse)
		}
		return body, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, fmt.Errorf("%w: status %d", transport.ErrUpstreamRejected, resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: status %d", transport.ErrUnreachable, resp.StatusCode)
	}
}

// Diagnose reports health by issuing a lightweight request and timing it.
func (c *Client) Diagnose(ctx context.Context) (transport.Diagnostics, error) {
	if c.closed {
		return transport.Diagnostics{}, transport.ErrClosed
	}
	url := c.cfg.HealthURL
	method := http.MethodGet
	if url == "" {
		url = c.cfg.URL
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return transport.Diagnostics{}, err
	}
	c.applyHeaders(req)
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return transport.Diagnostics{Healthy: false, Detail: err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	latency := time.Since(start)
	healthy := resp.StatusCode < 400
	return transport.Diagnostics{
		Healthy: healthy,
		Latency: latency,
		Detail:  fmt.Sprintf("status %d", resp.StatusCode),
	}, nil
}

func (c *Client) Close() error {
	c.closed = true
	return nil
}

func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	if c.cfg.FrontingHost != "" {
		req.Host = c.cfg.FrontingHost
	}
}

var _ transport.ClientTransport = (*Client)(nil)
