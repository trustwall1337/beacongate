package appsscript

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Bench harness for plan A7 #8–13 (performance acceptance criteria).
//
// These benchmarks run under `go test -bench=.` and are intended to
// produce reproducible numbers in CI. The harness is intentionally
// in-process — a real Apps Script + VPS round-trip would dominate
// the measurement and add network noise. What we measure here is
// the BeaconGate-internal cost: base64 encode/decode, AEAD seal/open
// (in the calling layer), round-trip dispatch, response handling.
//
// To get end-to-end p50/p95 against a real Apps Script deployment,
// see `apps_script/README.md` "Measurement" section and run a real
// SOCKS handshake through `beacongate-client` instrumented with a
// timing logger.

// benchHarness wires an appsscript.Client to an in-process fake
// Apps Script that does the base64 transform + immediate echo. The
// upstream is the fake VPS that echoes its input. Closure resources
// must be released by the caller via cleanup().
func benchHarness(b *testing.B) (cli *Client, cleanup func()) {
	b.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	fake := &fakeAppsScript{upstream: upstream}
	scriptSrv := httptest.NewServer(fake)
	c, err := New(Config{
		ScriptKeys:  []string{"BENCH_DEPLOY_ID"},
		ScriptURLs:  []string{scriptSrv.URL},
		HTTPClients: []*http.Client{scriptSrv.Client()},
	})
	if err != nil {
		b.Fatal(err)
	}
	return c, func() {
		_ = c.Close()
		scriptSrv.Close()
		upstream.Close()
	}
}

// BenchmarkRoundtripSmallBatch measures the per-batch cost for a
// small (256 B) sealed payload, which is the chatty interactive
// case. Plan A7 #9 budget: p50 ≤ 800ms; this in-process harness
// should be sub-millisecond and document the BeaconGate-internal
// floor.
func BenchmarkRoundtripSmallBatch(b *testing.B) {
	cli, cleanup := benchHarness(b)
	defer cleanup()
	payload := bytes.Repeat([]byte("x"), 256)
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := cli.Roundtrip(ctx, payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRoundtripLargeBatch measures the bulk-transfer cost.
// 64 KB ≈ a typical drained DATA chunk from the server's long-poll.
// Establishes the per-byte cost above the per-call overhead.
func BenchmarkRoundtripLargeBatch(b *testing.B) {
	cli, cleanup := benchHarness(b)
	defer cleanup()
	payload := bytes.Repeat([]byte("x"), 64*1024)
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for i := 0; i < b.N; i++ {
		_, err := cli.Roundtrip(ctx, payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRoundtripParallel exercises concurrent calls (plan A7 #10:
// 8 parallel sessions @ 1 MB/s aggregate, p95 ≤ 2500ms). The
// `-cpu` flag controls the parallelism count; default is GOMAXPROCS.
func BenchmarkRoundtripParallel(b *testing.B) {
	cli, cleanup := benchHarness(b)
	defer cleanup()
	payload := bytes.Repeat([]byte("x"), 4*1024)
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := cli.Roundtrip(ctx, payload)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkBase64EncodeDecode isolates the per-call base64 cost so
// the operator can see what fraction of Roundtrip time is the
// transport boundary (vs everything else like HTTP, AEAD, etc.).
// Useful as a regression baseline for plan A2's encoding choice.
func BenchmarkBase64EncodeDecode(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 4*1024)
	b.ResetTimer()
	b.SetBytes(int64(len(payload)))
	for i := 0; i < b.N; i++ {
		encoded := base64.StdEncoding.EncodeToString(payload)
		dst := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
		_, _ = base64.StdEncoding.Decode(dst, []byte(encoded))
	}
}

// TestPerformanceAcceptanceFloor is a guarded acceptance check, NOT
// a benchmark. It runs a small number of operations sequentially
// against the in-process harness and asserts a generous upper-bound
// per Roundtrip (10× the in-process baseline, which leaves headroom
// for slow CI workers). This is the closest CI surrogate for plan
// A7 #8–9 we can run without a real Apps Script deployment.
func TestPerformanceAcceptanceFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("performance acceptance floor (-short)")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	fake := &fakeAppsScript{upstream: upstream}
	scriptSrv := httptest.NewServer(fake)
	defer scriptSrv.Close()
	cli, err := New(Config{
		ScriptKeys:  []string{"PERF"},
		ScriptURLs:  []string{scriptSrv.URL},
		HTTPClients: []*http.Client{scriptSrv.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	const (
		samples = 50
		// Generous in-process upper bound: 100ms/round trip. A real
		// Apps Script deployment will far exceed this floor; the
		// CI test only asserts that the *internal* cost is bounded.
		maxLatency = 100 * time.Millisecond
	)
	payload := bytes.Repeat([]byte("x"), 1024)
	durs := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		_, err := cli.Roundtrip(context.Background(), payload)
		if err != nil {
			t.Fatal(err)
		}
		durs[i] = time.Since(start)
	}
	// p95 check.
	sorted := append([]time.Duration(nil), durs...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	p95 := sorted[(len(sorted)*95)/100]
	if p95 > maxLatency {
		t.Errorf("in-process p95 = %s exceeds the %s acceptance floor; either the harness is slow or the transport regressed", p95, maxLatency)
	}
}

// silence unused-import warning
var _ = sync.WaitGroup{}
