package integration

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	clientruntime "github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/client/socks"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	httpstransport "github.com/trustwall1337/beacongate/engine/transport/https"
	"github.com/trustwall1337/beacongate/server/policy"
	serverruntime "github.com/trustwall1337/beacongate/server/runtime"
	"github.com/trustwall1337/beacongate/server/upstream"
)

// latencyHarness is a tunnel + simulated-Apps-Script wrapper used by
// the before/after latency comparison. Every /tunnel POST flows
// through perCallDelay (configurable) so the harness models the real
// Apps Script per-call overhead at smaller scale, and tunnelHits
// counts every POST so we can verify the round-trip-count reduction
// the protocol fusion delivers.
type latencyHarness struct {
	socksAddr  string
	tunnelHits *atomic.Int64
	cleanup    func()
	pump       *clientruntime.Pump
}

// delayingHandler wraps a base http.Handler with a synchronous sleep
// before delegating. This simulates Apps Script's per-doFetch latency
// floor (real ~1.8 s, scaled down to 100 ms for a fast CI bench).
type delayingHandler struct {
	delay time.Duration
	next  http.Handler
	hits  *atomic.Int64
}

func (d *delayingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/tunnel" {
		d.hits.Add(1)
		time.Sleep(d.delay)
	}
	d.next.ServeHTTP(w, r)
}

func setupLatencyHarness(tb testing.TB, perCallDelay time.Duration) *latencyHarness {
	tb.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		tb.Fatal(err)
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		tb.Fatal(err)
	}

	engine := policy.NewEngine()
	dialer, derr := upstream.NewNetDialer(2*time.Second, "")
	if derr != nil {
		tb.Fatal(derr)
	}
	dialer.Safety.AllowPrivate = true

	srv := serverruntime.New("server-bench", sealer, dialer, &itPolicyEvaluator{engine: engine})
	if os.Getenv("BG_BENCH_TRACE") == "1" {
		srv.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	// BG_BENCH_LEGACY_DRAIN=1 disables the active-drain window so the
	// bench measures the v1.1.0 baseline behaviour (request POST returns
	// after the short 25ms drain; response is delivered on the next
	// long-poll). Comparing this to the default (active drain enabled)
	// quantifies the protocol-fusion latency win.
	if os.Getenv("BG_BENCH_LEGACY_DRAIN") == "1" {
		srv.SetActiveDrainWindow(0)
	}

	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	mux.Handle("/healthz", srv.Health())

	hits := &atomic.Int64{}
	wrapped := &delayingHandler{delay: perCallDelay, next: mux, hits: hits}
	ts := httptest.NewServer(wrapped)

	cfg := &config.ClientConfig{
		ClientID:   "client-bench",
		ListenAddr: "127.0.0.1:0",
		Server:     config.ClientServerConfig{URL: ts.URL + "/tunnel", Key: config.EncodeKey(key)},
		Transport:  config.ClientTransportConfig{Type: "https"},
	}
	if err := cfg.Validate(); err != nil {
		tb.Fatal(err)
	}
	tr, err := httpstransport.New(httpstransport.Config{URL: cfg.Server.URL, HealthURL: ts.URL + "/healthz"})
	if err != nil {
		tb.Fatal(err)
	}
	rt, err := clientruntime.New(cfg, tr)
	if err != nil {
		tb.Fatal(err)
	}
	if os.Getenv("BG_BENCH_TRACE") == "1" {
		rt.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	pump := clientruntime.NewPump(rt)
	pump.Start()

	socksSrv := socks.NewServer(pump)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	go socksSrv.Serve(l)

	cleanup := func() {
		_ = socksSrv.Close()
		_ = pump.Close()
		_ = rt.Close()
		_ = srv.Close()
		ts.Close()
	}

	return &latencyHarness{
		socksAddr:  l.Addr().String(),
		tunnelHits: hits,
		cleanup:    cleanup,
		pump:       pump,
	}
}

// socksRequestResponse runs one SOCKS5 connect + write + read cycle
// through the tunnel and returns wall-clock latency.
func socksRequestResponse(tb testing.TB, socksAddr string, host string, port uint16, request, want []byte) time.Duration {
	tb.Helper()
	start := time.Now()
	c, err := net.Dial("tcp", socksAddr)
	if err != nil {
		tb.Fatal(err)
	}
	defer c.Close()

	// SOCKS5 greeting + connect
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		tb.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(c, greeting); err != nil {
		tb.Fatal(err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port&0xff))
	if _, err := c.Write(req); err != nil {
		tb.Fatal(err)
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		tb.Fatal(err)
	}
	tail := make([]byte, 6)
	if _, err := io.ReadFull(c, tail); err != nil {
		tb.Fatal(err)
	}
	if hdr[1] != 0 {
		tb.Fatalf("socks reply rep=%d", hdr[1])
	}

	// Send request, receive response
	if _, err := c.Write(request); err != nil {
		tb.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		tb.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		tb.Fatalf("response mismatch: got %q want %q", got, want)
	}
	return time.Since(start)
}

// percentile returns the p-th percentile of a sorted slice (p in [0,100]).
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted) * p) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// LatencySnapshot is one before/after measurement record. JSON-
// serialized and either logged to stdout or written to the file
// path in BG_BENCH_OUT for the operator to diff.
type LatencySnapshot struct {
	Label          string        `json:"label"`
	Samples        int           `json:"samples"`
	PerCallDelay   time.Duration `json:"per_call_delay_ns"`
	UpstreamDelay  time.Duration `json:"upstream_delay_ns"`
	P50            time.Duration `json:"p50_ns"`
	P90            time.Duration `json:"p90_ns"`
	P95            time.Duration `json:"p95_ns"`
	Mean           time.Duration `json:"mean_ns"`
	Min            time.Duration `json:"min_ns"`
	Max            time.Duration `json:"max_ns"`
	TunnelHits     int64         `json:"tunnel_hits"`
	HitsPerRequest float64       `json:"hits_per_request"`
	Timestamp      time.Time     `json:"timestamp"`
}

func (s LatencySnapshot) String() string {
	return fmt.Sprintf(
		"%s: samples=%d per_call=%v upstream=%v p50=%v p90=%v p95=%v mean=%v min=%v max=%v hits=%d hits/req=%.2f",
		s.Label, s.Samples, s.PerCallDelay, s.UpstreamDelay,
		s.P50.Round(time.Millisecond), s.P90.Round(time.Millisecond),
		s.P95.Round(time.Millisecond), s.Mean.Round(time.Millisecond),
		s.Min.Round(time.Millisecond), s.Max.Round(time.Millisecond),
		s.TunnelHits, s.HitsPerRequest,
	)
}

var (
	flagBenchSamples       = flag.Int("bg.samples", 20, "samples per latency measurement")
	flagBenchDelay         = flag.Duration("bg.delay", 100*time.Millisecond, "simulated Apps Script per-call delay")
	flagBenchUpstreamDelay = flag.Duration("bg.upstream", 200*time.Millisecond, "simulated upstream-server latency (TLS handshake + remote response)")
	flagBenchLabel         = flag.String("bg.label", "", "label for this measurement (e.g. 'baseline' or 'fused')")
	flagBenchOut           = flag.String("bg.out", "", "JSON output file (default: stdout only)")
)

// startSlowEcho is a configurable echo upstream that delays each
// response by upstreamDelay before sending it back. Models the
// real-world upstream-latency floor (TLS handshake + remote server)
// so the bench can distinguish the legacy 25 ms drain from the new
// active-drain window: with a 200 ms upstream, only the active drain
// can fold the response back into the same POST.
func startSlowEcho(t testing.TB, upstreamDelay time.Duration) (string, uint16, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 32*1024)
				for {
					n, rerr := c.Read(buf)
					if n > 0 {
						if upstreamDelay > 0 {
							time.Sleep(upstreamDelay)
						}
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if rerr != nil {
						return
					}
				}
			}(c)
		}
	}()
	host, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	return host, uint16(port), func() { l.Close() }
}

// TestLatencyMeasurement is the central before/after benchmark. It
// runs N sequential SOCKS GETs through the in-process tunnel with a
// configurable per-tunnel-POST delay (simulating Apps Script's
// per-call overhead) and reports p50/p90/p95 + tunnel-hit count.
//
// Usage (capture baseline, then re-run after changes):
//
//	GOTOOLCHAIN=local go test -run TestLatencyMeasurement ./test/integration/ \
//	    -bg.samples=20 -bg.delay=100ms -bg.label=baseline -bg.out=/tmp/bg-baseline.json
//
//	GOTOOLCHAIN=local go test -run TestLatencyMeasurement ./test/integration/ \
//	    -bg.samples=20 -bg.delay=100ms -bg.label=after -bg.out=/tmp/bg-after.json
//
// The "hits/req" metric is the round-trip count per logical SOCKS
// request — this is what protocol fusion drives down (3 → 1).
func TestLatencyMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement (-short)")
	}
	label := *flagBenchLabel
	if label == "" {
		label = "unlabeled"
	}

	echoHost, echoPort, echoStop := startSlowEcho(t, *flagBenchUpstreamDelay)
	defer echoStop()

	h := setupLatencyHarness(t, *flagBenchDelay)
	defer h.cleanup()

	// Warm-up: drive one request through the pump so any first-time
	// initialization (HTTP/2 handshake, key derivation cache) doesn't
	// skew the first measurement.
	want := []byte("warmup")
	_ = socksRequestResponse(t, h.socksAddr, echoHost, echoPort, want, want)

	// Reset hit counter AFTER warm-up so we measure only steady-state.
	h.tunnelHits.Store(0)

	samples := *flagBenchSamples
	durs := make([]time.Duration, 0, samples)

	payload := bytes.Repeat([]byte("x"), 256)
	// Space samples so the CLOSE round-trip from sample N doesn't queue
	// in front of sample N+1's OPEN+DATA on the single-goroutine pump.
	// Without this gap, every sample pays an extra perCallDelay for the
	// previous sample's CLOSE — that's a bench-setup artifact, not a
	// property of the protocol fusion. The gap is 4× perCallDelay so a
	// CLOSE has comfortably drained before the next dial fires.
	interSampleGap := 4 * *flagBenchDelay
	for i := 0; i < samples; i++ {
		d := socksRequestResponse(t, h.socksAddr, echoHost, echoPort, payload, payload)
		durs = append(durs, d)
		if i < samples-1 {
			time.Sleep(interSampleGap)
		}
	}

	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })

	var sum time.Duration
	for _, d := range durs {
		sum += d
	}
	mean := sum / time.Duration(len(durs))

	snap := LatencySnapshot{
		Label:         label,
		Samples:       samples,
		PerCallDelay:  *flagBenchDelay,
		UpstreamDelay: *flagBenchUpstreamDelay,
		P50:           percentile(durs, 50),
		P90:           percentile(durs, 90),
		P95:           percentile(durs, 95),
		Mean:          mean,
		Min:           durs[0],
		Max:           durs[len(durs)-1],
		TunnelHits:    h.tunnelHits.Load(),
		Timestamp:     time.Now(),
	}
	snap.HitsPerRequest = float64(snap.TunnelHits) / float64(samples)

	t.Log(snap.String())

	if out := *flagBenchOut; out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatalf("create out: %v", err)
		}
		defer f.Close()
		if err := json.NewEncoder(f).Encode(snap); err != nil {
			t.Fatalf("encode: %v", err)
		}
		t.Logf("snapshot written to %s", out)
	}
}
