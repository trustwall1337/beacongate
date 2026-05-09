package appsscript

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// TestPoolDefaultsToMultipleSNIRotation locks in the v1.1.1 fix for
// Issue A (5–10 min wedging): an unconfigured FrontingConfig must
// produce more than one SNI host so traffic spreads across multiple
// independent h2 ClientConns to different Google edge boxes. Without
// rotation, every request rides one conn and a single edge wedge
// stalls the whole tunnel.
func TestPoolDefaultsToMultipleSNIRotation(t *testing.T) {
	pool := newHTTPClientPool(FrontingConfig{}, 5*time.Second)
	defer pool.close()
	if got := len(pool.hosts); got < 2 {
		t.Fatalf("default SNI hosts = %d (%v); want >= 2 to spread load across multiple Google edges", got, pool.hosts)
	}
	if len(pool.hosts) != len(pool.clients) {
		t.Fatalf("hosts %d != clients %d — pool fields out of sync", len(pool.hosts), len(pool.clients))
	}
}

// TestPoolPickRoundRobins confirms pick() distributes across all
// configured slots. With multi-SNI defaults this means a healthy
// tunnel spreads requests across separate h2 conns instead of pinning
// to one.
func TestPoolPickRoundRobins(t *testing.T) {
	pool := newHTTPClientPool(FrontingConfig{
		SNIHosts: []string{"a.example", "b.example", "c.example"},
	}, 5*time.Second)
	defer pool.close()
	seen := map[*http.Client]int{}
	for i := 0; i < 30; i++ {
		seen[pool.pick()]++
	}
	if got := len(seen); got != 3 {
		t.Fatalf("pick visited %d distinct clients in 30 calls; want 3 (round-robin must touch every slot)", got)
	}
	for client, n := range seen {
		if n < 8 || n > 12 {
			t.Errorf("client %p got %d picks in 30 calls; want ~10 (round-robin distribution)", client, n)
		}
	}
}

// TestPoolRetireAllInvokesEveryTransport confirms retireAll() touches
// every slot's *http2.Transport. retireAll() is the recovery path on
// any RoundTrip error — the wedged-conn failure mode (Issue A) is
// fixed by dropping idle conns immediately so the next request
// re-dials. CloseIdleConnections itself is stdlib-tested; this test
// just locks down that we call it on every transport.
func TestPoolRetireAllInvokesEveryTransport(t *testing.T) {
	// Three slots, each with a tracking transport that increments a
	// counter when CloseIdleConnections fires (via the RoundTripper
	// interface — http2.Transport.CloseIdleConnections does this
	// internally; we wrap it).
	calls := make([]*atomic.Int64, 3)
	clients := make([]*http.Client, 3)
	for i := range clients {
		calls[i] = &atomic.Int64{}
		clients[i] = &http.Client{Transport: &countingTransport{
			inner: &http2.Transport{},
			calls: calls[i],
		}}
	}
	// Build the pool by hand — retireAll only inspects c.Transport so
	// we can bypass newHTTPClientPool's TLS setup.
	pool := &httpClientPool{
		clients: clients,
		hosts:   []string{"a", "b", "c"},
		stopCh:  make(chan struct{}),
	}
	defer pool.close()

	pool.retireAll()
	for i, c := range calls {
		if got := c.Load(); got != 1 {
			t.Errorf("slot %d CloseIdleConnections call count = %d; want 1 (retireAll must touch every transport)", i, got)
		}
	}
}

// countingTransport wraps an http2.Transport and bumps a counter on
// each CloseIdleConnections call. Only used by the test above to
// prove retireAll's call-fanout behavior in isolation from real I/O.
type countingTransport struct {
	inner *http2.Transport
	calls *atomic.Int64
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return c.inner.RoundTrip(r)
}

func (c *countingTransport) CloseIdleConnections() {
	c.calls.Add(1)
	c.inner.CloseIdleConnections()
}
