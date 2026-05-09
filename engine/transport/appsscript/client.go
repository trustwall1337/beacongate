// Package appsscript implements the BeaconGate Apps Script transport: the
// censorship-evasion path that tunnels every encrypted batch through a
// user-deployed Google Apps Script web app.
//
// Wire shape (one POST = one batch):
//
//	Client → Google IP (via SNI fronting) → script.google.com/macros/s/{id}/exec
//	  POST text/plain, body = base64(sealed batch bytes)
//
//	Code.gs forwards as binary to the operator's BeaconGate server.
//	Server responds binary; Code.gs base64-encodes and returns.
//	Client base64-decodes and returns the sealed reply to the caller.
//
// On the network, a passive observer sees TLS to a Google IP with
// SNI=www.google.com (or a configured rotation entry) and HTTP
// Host: script.google.com. Blocking this path cleanly requires also
// blocking script.google.com itself.
//
// IMPORTANT: this transport does NOT make blocking impossible. It raises
// the cost of blocking. Residual risks (traffic-pattern analysis,
// TLS-fingerprint analysis, Google-side classifiers, URL-pattern
// blocking of /macros/s/.../exec) are not eliminated. See SECURITY.md.
package appsscript

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/trustwall1337/beacongate/engine/transport"
)

const (
	defaultGoogleHost     = "216.239.38.120:443"
	defaultUserAgent      = "beacongate-appsscript/1.1"
	defaultRequestTimeout = 35 * time.Second // long-poll (25s) + slack
	contentTypeText       = "text/plain"

	// Maximum response body size we accept from a single Apps Script
	// invocation. Generous bound because base64-encoded sealed batches
	// can be ~10 MiB; HTML error pages or quota notices are dropped
	// well below this so the cap is mostly defensive.
	maxResponseBody = 32 * 1024 * 1024
)

// Config bundles everything the appsscript transport needs.
//
// In appsscript mode, the operator does NOT set server.url at the
// config level — the script URL is constructed from ScriptKeys here.
// (See plan A8: server.url MUST be empty/omitted for appsscript.)
type Config struct {
	// ScriptKeys is the list of Apps Script deployment IDs. At least
	// one is required. The transport round-robins across them and
	// fails over to the next on per-batch error (bounded per A9 #3).
	ScriptKeys []string
	// ScriptAccounts is an optional parallel slice of operator labels
	// (e.g. "alpha-account") for stats grouping. Missing entries are
	// treated as unlabeled.
	ScriptAccounts []string

	// Fronting configures the SNI-fronted HTTP clients. See
	// FrontingConfig for details. When zero-valued, defaults to
	// {GoogleIP: defaultGoogleHost, SNIHosts: ["www.google.com"]}.
	Fronting FrontingConfig

	// RequestTimeout caps each HTTP request. Zero =
	// defaultRequestTimeout, which is sized for the server's 25s
	// long-poll plus slack.
	RequestTimeout time.Duration

	// UserAgent overrides the default UA on each request.
	UserAgent string

	// HTTPClients lets tests inject pre-built clients, bypassing the
	// fronting layer entirely. When non-nil, Fronting is ignored.
	HTTPClients []*http.Client

	// ScriptURLs is a TEST-ONLY override. When non-empty, the
	// transport uses these URLs verbatim instead of constructing
	// `https://script.google.com/macros/s/{key}/exec` from
	// ScriptKeys. The slice must be parallel to ScriptKeys (same
	// length). Production deployments leave this empty.
	ScriptURLs []string
}

// Client is a transport.ClientTransport implementation backed by Google
// Apps Script. Safe for concurrent use; one *Client per process is
// typical (the Pump above it already serializes outbound batches into
// one in-flight request at a time, but multiple parallel calls are
// supported for tests and future fan-out).
type Client struct {
	cfg       Config
	pool      *endpointPool
	clients   *httpClientPool
	userAgent string
	timeout   time.Duration

	closedMu sync.Mutex
	closed   bool

	// quotaCancel stops the background quota-poll loop when Close is
	// called. nil before quota loop starts (in tests we may skip it).
	quotaCancel context.CancelFunc
	quotaDone   chan struct{}
}

// New constructs the transport. ScriptKeys must contain at least one
// deployment ID. Returns the transport with prewarm already firing in
// the background and the quota loop starting.
func New(cfg Config) (*Client, error) {
	if len(cfg.ScriptKeys) == 0 {
		return nil, errors.New("appsscript transport: ScriptKeys must contain at least one deployment ID")
	}
	for i, key := range cfg.ScriptKeys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("appsscript transport: ScriptKeys[%d] is empty", i)
		}
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if cfg.Fronting.GoogleIP == "" {
		cfg.Fronting.GoogleIP = defaultGoogleHost
	}

	if len(cfg.ScriptURLs) > 0 && len(cfg.ScriptURLs) != len(cfg.ScriptKeys) {
		return nil, fmt.Errorf("appsscript transport: ScriptURLs must be parallel to ScriptKeys (got %d urls, %d keys)", len(cfg.ScriptURLs), len(cfg.ScriptKeys))
	}
	pool := newEndpointPoolWithURLs(cfg.ScriptKeys, cfg.ScriptAccounts, cfg.ScriptURLs)
	// Warn if multi-deployment but everything collapsed into the
	// single anonymous bucket — operator almost always meant to label.
	if len(cfg.ScriptKeys) >= 2 && len(pool.buckets) == 1 && pool.eps[0].account == "" {
		// Use stderr directly (no logger plumbed into Config); this is
		// loud enough that operators won't miss it during first run.
		fmt.Fprintln(os.Stderr,
			"appsscript: WARN — multiple deployments configured but no `account` labels set; quota and per-account concurrency will be treated as ONE Google account. If your deployments are under multiple accounts, label them in script_keys: [{\"id\":\"...\",\"account\":\"alpha\"}, ...]")
	}

	var clients *httpClientPool
	if len(cfg.HTTPClients) > 0 {
		clients = &httpClientPool{
			clients: cfg.HTTPClients,
			hosts:   []string{"injected"},
			stopCh:  make(chan struct{}),
		}
	} else {
		clients = newHTTPClientPool(cfg.Fronting, cfg.RequestTimeout)
	}

	c := &Client{
		cfg:       cfg,
		pool:      pool,
		clients:   clients,
		userAgent: cfg.UserAgent,
		timeout:   cfg.RequestTimeout,
		quotaDone: make(chan struct{}),
	}
	c.startQuotaLoop()
	return c, nil
}

// Roundtrip sends a sealed batch through the Apps Script forwarder and
// returns the sealed reply.
//
// Failover is bounded to two attempts per call (Workstream A9 invariant
// #3). The first attempt picks via the pool's round-robin; on a
// transport-level or HTTP error, one fallback attempt is made on the
// next live deployment. Persistent fleet-wide failure surfaces to the
// caller as transport.ErrUnreachable; the caller (the Pump) decides
// whether to retry the batch later.
func (c *Client) Roundtrip(ctx context.Context, batch []byte) ([]byte, error) {
	if c.isClosed() {
		return nil, transport.ErrClosed
	}
	if len(batch) == 0 {
		return nil, fmt.Errorf("%w: empty batch", transport.ErrInvalidResponse)
	}

	encoded := base64.StdEncoding.EncodeToString(batch)
	now := time.Now()

	primary := c.pool.pick(now)
	if primary < 0 {
		// Every endpoint blacklisted. Try one anyway (index 0) — better
		// to attempt than to stall the caller; the failure will be
		// reported up and may unstick a flaky deployment.
		primary = 0
	}

	resp, err := c.attempt(ctx, primary, encoded)
	if err == nil {
		c.pool.recordSuccess(primary)
		return resp, nil
	}

	// Single bounded failover (A9 #3): try one alternate, no further
	// cycling within this call.
	c.pool.recordFailure(primary, time.Now(), isQuotaError(err))
	if !shouldFailover(err) || ctx.Err() != nil {
		return nil, err
	}
	fallback := c.pool.pickFallback(primary, time.Now())
	if fallback < 0 {
		return nil, err
	}
	resp, err2 := c.attempt(ctx, fallback, encoded)
	if err2 == nil {
		c.pool.recordSuccess(fallback)
		return resp, nil
	}
	c.pool.recordFailure(fallback, time.Now(), isQuotaError(err2))
	// Surface the last error; both deployments rejected the batch.
	return nil, err2
}

// attempt is one POST to one deployment. Returns the decoded sealed
// reply on success, or a wrapped transport sentinel error.
func (c *Client) attempt(ctx context.Context, idx int, encodedBody string) ([]byte, error) {
	url := c.pool.urlAt(idx)
	if url == "" {
		return nil, fmt.Errorf("%w: endpoint %d has no URL", transport.ErrUnreachable, idx)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(encodedBody))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", transport.ErrInvalidResponse, err)
	}
	req.Header.Set("Content-Type", contentTypeText)
	req.Header.Set("User-Agent", c.userAgent)

	httpClient := c.clients.pick()
	if httpClient == nil {
		return nil, fmt.Errorf("%w: no http client available", transport.ErrUnreachable)
	}

	httpResp, err := httpClient.Do(req)
	if err != nil {
		// %w on the inner err preserves the error chain so the pump's
		// errors.Is(err, context.Canceled) check at sessions.go:439
		// can recognise long-poll cancellations as expected (not faults).
		// %v here would drop the chain and every cancelled long-poll
		// would log as pump.exchange_failed and stall the data path.
		// On any non-cancellation transport error we also force
		// closing of idle h2 conns across the pool — the wedged-conn
		// failure mode (Issue A) recovers on the very next request
		// rather than waiting for the periodic retirement tick.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			c.clients.retireAll()
		}
		return nil, fmt.Errorf("%w: %w", transport.ErrUnreachable, err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody))
	// Every HTTP response we read consumed one Apps Script invocation,
	// regardless of status — bump the daily counter even on 403/HTML.
	// Off the request hot path: bumpDailyCount only takes a brief
	// mutex; no I/O. (A9 #4 satisfied — quota *polling* is off-path,
	// quota *counting* is one atomic-ish increment per response.)
	c.bumpDailyCount(idx)

	if readErr != nil {
		return nil, fmt.Errorf("%w: read body: %w", transport.ErrUnreachable, readErr)
	}

	switch {
	case httpResp.StatusCode >= 200 && httpResp.StatusCode < 300:
		// fall through to body decode
	case httpResp.StatusCode == http.StatusForbidden:
		// Apps Script quota or disabled deployment. Treat as quota
		// error so backoff uses the long TTL.
		return nil, fmt.Errorf("%w: status 403 (likely quota)", transport.ErrUpstreamRejected)
	case httpResp.StatusCode >= 400 && httpResp.StatusCode < 500:
		return nil, fmt.Errorf("%w: status %d", transport.ErrUpstreamRejected, httpResp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: status %d", transport.ErrUnreachable, httpResp.StatusCode)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty body", transport.ErrInvalidResponse)
	}

	// Tolerate either padded or unpadded base64 input (defensive
	// interop — under normal operation Code.gs emits padded form).
	trimmed := bytes.TrimRight(bytes.TrimSpace(body), "=")
	// Re-pad for StdEncoding's strict decoder.
	if pad := (4 - len(trimmed)%4) % 4; pad > 0 {
		trimmed = append(trimmed, bytes.Repeat([]byte("="), pad)...)
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(trimmed)))
	n, err := base64.StdEncoding.Decode(decoded, trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: base64 decode: %w", transport.ErrInvalidResponse, err)
	}
	return decoded[:n], nil
}

// Diagnose performs a doGet against each configured deployment in
// parallel and reports an aggregate health verdict. Latency is the
// median of successful probes; Healthy is true when at least one
// deployment answers with the expected JSON shape.
func (c *Client) Diagnose(ctx context.Context) (transport.Diagnostics, error) {
	if c.isClosed() {
		return transport.Diagnostics{}, transport.ErrClosed
	}
	return c.diagnoseAll(ctx)
}

// Close stops the background quota loop and marks the client closed.
// Subsequent Roundtrip / Diagnose calls return transport.ErrClosed.
func (c *Client) Close() error {
	c.closedMu.Lock()
	if c.closed {
		c.closedMu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.quotaCancel
	c.closedMu.Unlock()
	if cancel != nil {
		cancel()
		<-c.quotaDone
	}
	if c.clients != nil {
		c.clients.close()
	}
	return nil
}

func (c *Client) isClosed() bool {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()
	return c.closed
}

// shouldFailover decides whether a primary-attempt error is worth a
// second attempt against a different deployment. We failover on
// transport reachability and 4xx (quota / per-deployment ToS) errors.
// We do NOT failover on context cancellation (the caller gave up) or
// invalid-response shape (our wire bytes are bad — same on both
// deployments).
func shouldFailover(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, transport.ErrInvalidResponse) {
		return false
	}
	return errors.Is(err, transport.ErrUnreachable) || errors.Is(err, transport.ErrUpstreamRejected)
}

// isQuotaError reports whether the failure looks like Apps Script
// quota exhaustion (HTTP 403 from the deployment). When true, the
// endpoint backs off for the long quotaBlacklistTTL window rather than
// the exponential failure schedule.
func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "quota")
}

var _ transport.ClientTransport = (*Client)(nil)
