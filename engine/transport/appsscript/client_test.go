package appsscript

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/transport"
)

// fakeAppsScript implements the Code.gs forwarder behavior described in
// plan A3: base64-decode the request body, POST to the upstream as
// binary, base64-encode the upstream response, return as text. Used in
// every test as a stand-in for the real Apps Script web app.
type fakeAppsScript struct {
	upstream *httptest.Server
	// quotaJSON, when non-empty, is what doGet returns. Default is the
	// healthy-shape JSON.
	quotaJSON string
	// failNextPost, when non-zero, causes the next N doPost requests to
	// return failStatus before forwarding. Useful to exercise failover.
	failNextPost int32
	failStatus   int

	postCount atomic.Int64
	getCount  atomic.Int64
}

func (f *fakeAppsScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.getCount.Add(1)
		body := f.quotaJSON
		if body == "" {
			body = `{"ok":true,"date":"2026-05-09","count":42,"version":1,"protocol":1}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	case http.MethodPost:
		f.postCount.Add(1)
		// Optional injected failure
		if remaining := atomic.LoadInt32(&f.failNextPost); remaining > 0 {
			if atomic.AddInt32(&f.failNextPost, -1) >= 0 {
				status := f.failStatus
				if status == 0 {
					status = http.StatusInternalServerError
				}
				http.Error(w, "injected failure", status)
				return
			}
		}
		// Read text/plain base64 body, decode, forward to upstream.
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		// Per plan A3: standard alphabet, accept either padded or unpadded.
		trimmed := bytes.TrimRight(bytes.TrimSpace(raw), "=")
		if pad := (4 - len(trimmed)%4) % 4; pad > 0 {
			trimmed = append(trimmed, bytes.Repeat([]byte("="), pad)...)
		}
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(trimmed)))
		n, err := base64.StdEncoding.Decode(decoded, trimmed)
		if err != nil {
			http.Error(w, "base64", http.StatusBadRequest)
			return
		}
		// Forward to upstream as binary.
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, f.upstream.URL, bytes.NewReader(decoded[:n]))
		if err != nil {
			http.Error(w, "build", http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "forward", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		// Encode upstream binary back to base64 text for the client.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(respBody))
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// newAppsScriptHarness wires up: an upstream "VPS" handler (the
// caller-provided fn), a fake Apps Script forwarder, and an
// appsscript.Client pointed at the forwarder via ScriptURLs override.
// Returns the client plus a cleanup func.
func newAppsScriptHarness(t *testing.T, upstreamHandler http.HandlerFunc) (*Client, *fakeAppsScript, func()) {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	fake := &fakeAppsScript{upstream: upstream}
	scriptSrv := httptest.NewServer(fake)
	cli, err := New(Config{
		ScriptKeys:  []string{"DEPLOY_ID_1"},
		ScriptURLs:  []string{scriptSrv.URL},
		HTTPClients: []*http.Client{scriptSrv.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = cli.Close()
		scriptSrv.Close()
		upstream.Close()
	}
	return cli, fake, cleanup
}

func TestRoundtripEchoThroughAppsScript(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
	cli, fake, cleanup := newAppsScriptHarness(t, echo)
	defer cleanup()

	want := []byte("hello-via-apps-script-with-binary-payload-\x00\x01\x02\xff")
	got, err := cli.Roundtrip(context.Background(), want)
	if err != nil {
		t.Fatalf("Roundtrip: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, want)
	}
	if fake.postCount.Load() != 1 {
		t.Fatalf("expected exactly 1 post, got %d", fake.postCount.Load())
	}
}

func TestRoundtripFailoverToSecondDeployment(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}

	// Two upstreams (could share, but separate keeps the forwarder
	// behavior cleanly attributable).
	upstream := httptest.NewServer(http.HandlerFunc(echo))
	defer upstream.Close()

	// Fake A always 500s. Fake B is healthy.
	fakeA := &fakeAppsScript{upstream: upstream, failNextPost: 1000, failStatus: http.StatusInternalServerError}
	fakeB := &fakeAppsScript{upstream: upstream}
	srvA := httptest.NewServer(fakeA)
	srvB := httptest.NewServer(fakeB)
	defer srvA.Close()
	defer srvB.Close()

	cli, err := New(Config{
		ScriptKeys:  []string{"A", "B"},
		ScriptURLs:  []string{srvA.URL, srvB.URL},
		HTTPClients: []*http.Client{srvA.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	want := []byte("failover-payload")
	got, err := cli.Roundtrip(context.Background(), want)
	if err != nil {
		t.Fatalf("Roundtrip: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, want)
	}
	// Failover must be bounded: at most ONE failover (A9 #3) so postA + postB <= 2.
	total := fakeA.postCount.Load() + fakeB.postCount.Load()
	if total != 2 {
		t.Fatalf("expected exactly 2 posts (1 failed primary + 1 successful fallback), got %d (A=%d B=%d)",
			total, fakeA.postCount.Load(), fakeB.postCount.Load())
	}
	if fakeB.postCount.Load() != 1 {
		t.Fatalf("expected fallback B to receive 1 post, got %d", fakeB.postCount.Load())
	}
}

func TestRoundtripBoundedFailoverDoesNotCycle(t *testing.T) {
	// Three deployments all failing — failover must NOT cycle through
	// all three; should attempt at most 2 total per A9 #3.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	fakes := make([]*fakeAppsScript, 3)
	srvs := make([]*httptest.Server, 3)
	for i := range fakes {
		fakes[i] = &fakeAppsScript{upstream: upstream, failNextPost: 1000, failStatus: http.StatusInternalServerError}
		srvs[i] = httptest.NewServer(fakes[i])
		defer srvs[i].Close()
	}

	cli, err := New(Config{
		ScriptKeys:  []string{"A", "B", "C"},
		ScriptURLs:  []string{srvs[0].URL, srvs[1].URL, srvs[2].URL},
		HTTPClients: []*http.Client{srvs[0].Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	_, err = cli.Roundtrip(context.Background(), []byte("never delivered"))
	if err == nil {
		t.Fatalf("expected error when all deployments fail")
	}
	total := int64(0)
	for _, f := range fakes {
		total += f.postCount.Load()
	}
	if total > 2 {
		t.Fatalf("A9 #3 violation: failover cycled past 2 attempts, got %d total posts", total)
	}
}

func TestRoundtripQuotaErrorBackoff(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	fake := &fakeAppsScript{upstream: upstream, failNextPost: 1, failStatus: http.StatusForbidden}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cli, err := New(Config{
		ScriptKeys:  []string{"A"},
		ScriptURLs:  []string{srv.URL},
		HTTPClients: []*http.Client{srv.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	_, err = cli.Roundtrip(context.Background(), []byte("x"))
	if err == nil || !errors.Is(err, transport.ErrUpstreamRejected) {
		t.Fatalf("expected ErrUpstreamRejected on 403, got %v", err)
	}
	// Endpoint should now be in long quotaBlacklistTTL backoff.
	snap := cli.pool.snapshot()
	if got := time.Until(snap[0].blacklistedTill); got < quotaBlacklistTTL/2 {
		t.Fatalf("expected long-window backoff after 403, got %s", got)
	}
}

func TestRoundtripContextCancel(t *testing.T) {
	// Upstream sleeps with bounded duration so httptest.Server.Close
	// can drain on test exit; the client's deadline still fires
	// first, exercising the cancel path.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer upstream.Close()
	fake := &fakeAppsScript{upstream: upstream}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cli, err := New(Config{
		ScriptKeys:  []string{"A"},
		ScriptURLs:  []string{srv.URL},
		HTTPClients: []*http.Client{srv.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = cli.Roundtrip(ctx, []byte("x"))
	if err == nil {
		t.Fatalf("expected error on context timeout")
	}
	// REGRESSION GUARD: the wrapped error MUST satisfy errors.Is for
	// BOTH transport.ErrUnreachable (so callers can classify it as a
	// transport-level fault) AND context.DeadlineExceeded (so the
	// pump's long-poll loop in client/runtime/sessions.go can recognise
	// expected cancellations and not spam pump.exchange_failed).
	// Previous code used `fmt.Errorf("%w: %v", ErrUnreachable, err)`
	// which lost the inner chain — symptoms in the field: tunnel data
	// path stalling on the second SOCKS request because the long-poll
	// got logged as a fault and back-pressure built up.
	if !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("expected errors.Is(err, transport.ErrUnreachable) to be true; got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected errors.Is(err, context.DeadlineExceeded) to be true; got %v", err)
	}
}

// TestRoundtripContextCanceledPreservesChain reproduces the exact
// production scenario behind Bug #2: the pump's long-poll cancels its
// context (via signalFlush) when new outbound work arrives. The HTTP
// client returns context.Canceled, the transport wraps it, and the
// pump checks `errors.Is(err, context.Canceled)` to know whether to
// silently drop or log+escalate. Before the %v→%w fix this assertion
// returned false, every cancelled long-poll logged as a fault, and
// the data path stalled until the next probe tick.
func TestRoundtripContextCanceledPreservesChain(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer upstream.Close()
	srv := httptest.NewServer(&fakeAppsScript{upstream: upstream})
	defer srv.Close()
	cli, err := New(Config{
		ScriptKeys:  []string{"A"},
		ScriptURLs:  []string{srv.URL},
		HTTPClients: []*http.Client{srv.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel from a goroutine after the request is in-flight.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = cli.Roundtrip(ctx, []byte("x"))
	if err == nil {
		t.Fatalf("expected error on context cancel")
	}
	if !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("expected errors.Is(err, transport.ErrUnreachable) to be true; got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected errors.Is(err, context.Canceled) to be true; got %v", err)
	}
}

func TestDiagnoseAggregatesProbes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	fakeOK := &fakeAppsScript{upstream: upstream}
	fakeBad := &fakeAppsScript{upstream: upstream, quotaJSON: "<html>error</html>"}
	srvOK := httptest.NewServer(fakeOK)
	srvBad := httptest.NewServer(fakeBad)
	defer srvOK.Close()
	defer srvBad.Close()
	cli, err := New(Config{
		ScriptKeys:  []string{"OK", "BAD"},
		ScriptURLs:  []string{srvOK.URL, srvBad.URL},
		HTTPClients: []*http.Client{srvOK.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	d, err := cli.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	// At least one healthy → overall healthy=true (the BAD one returns
	// reachable HTTP just with non-JSON body, treated as
	// reachable+legacy in probeOne — both are reachable here, so test
	// just confirms healthy=true).
	if !d.Healthy {
		t.Fatalf("expected healthy=true, got %+v", d)
	}
}

func TestNewRejectsEmptyScriptKeys(t *testing.T) {
	_, err := New(Config{ScriptKeys: nil})
	if err == nil {
		t.Fatalf("expected error on empty ScriptKeys")
	}
	_, err = New(Config{ScriptKeys: []string{""}})
	if err == nil {
		t.Fatalf("expected error on whitespace-only key")
	}
	_, err = New(Config{ScriptKeys: []string{"X"}, ScriptURLs: []string{"a", "b"}})
	if err == nil {
		t.Fatalf("expected error on ScriptURLs/ScriptKeys length mismatch")
	}
}

func TestBuildScriptURL(t *testing.T) {
	got := buildScriptURL("ABC123")
	want := "https://script.google.com/macros/s/ABC123/exec"
	if got != want {
		t.Fatalf("buildScriptURL = %q, want %q", got, want)
	}
}

func TestNextQuotaResetCrossesMidnightPacific(t *testing.T) {
	pacific := quotaResetTZ
	// 11:00 PM Pacific
	now := time.Date(2026, 5, 9, 23, 0, 0, 0, pacific)
	next := nextQuotaReset(now)
	if !next.After(now) || next.Sub(now) != time.Hour {
		t.Fatalf("nextQuotaReset(11pm Pacific) = %s, want exactly 1h after %s", next, now)
	}
	// 1:00 AM Pacific
	now = time.Date(2026, 5, 9, 1, 0, 0, 0, pacific)
	next = nextQuotaReset(now)
	if next.Sub(now) != 23*time.Hour {
		t.Fatalf("nextQuotaReset(1am Pacific) = %s, want 23h after %s", next, now)
	}
}

func TestEndpointPoolFailureBackoffExponential(t *testing.T) {
	pool := newEndpointPool([]string{"A"}, nil)
	now := time.Now()

	pool.recordFailure(0, now, false)
	first := pool.eps[0].blacklistedTill.Sub(now)
	if first < endpointBlacklistBaseTTL || first > endpointBlacklistBaseTTL*2 {
		t.Fatalf("first failure backoff = %s, want ~%s", first, endpointBlacklistBaseTTL)
	}

	pool.recordFailure(0, now, false)
	second := pool.eps[0].blacklistedTill.Sub(now)
	if second <= first {
		t.Fatalf("second failure backoff %s should exceed first %s", second, first)
	}

	// Quota error should switch to long TTL regardless of failCount.
	pool.recordFailure(0, now, true)
	quota := pool.eps[0].blacklistedTill.Sub(now)
	if quota != quotaBlacklistTTL {
		t.Fatalf("quota error TTL = %s, want %s", quota, quotaBlacklistTTL)
	}

	// Success clears state.
	pool.recordSuccess(0)
	if !pool.eps[0].blacklistedTill.IsZero() || pool.eps[0].failCount != 0 {
		t.Fatalf("recordSuccess did not clear backoff state")
	}
}

func TestEndpointPoolPickRoundRobinSkipsBlacklisted(t *testing.T) {
	pool := newEndpointPool([]string{"A", "B", "C"}, nil)
	now := time.Now()
	pool.eps[0].blacklistedTill = now.Add(time.Hour)
	pool.eps[2].blacklistedTill = now.Add(time.Hour)
	for i := 0; i < 5; i++ {
		idx := pool.pick(now)
		if idx != 1 {
			t.Fatalf("pick #%d returned %d, expected 1 (only live endpoint)", i, idx)
		}
	}
	// All blacklisted → returns -1
	pool.eps[1].blacklistedTill = now.Add(time.Hour)
	if pool.pick(now) != -1 {
		t.Fatalf("expected -1 when all endpoints blacklisted")
	}
}

func TestEndpointPoolPickFallback(t *testing.T) {
	pool := newEndpointPool([]string{"A", "B", "C"}, nil)
	now := time.Now()
	if got := pool.pickFallback(0, now); got != 1 {
		t.Fatalf("fallback from A = %d, want 1", got)
	}
	if got := pool.pickFallback(2, now); got != 0 {
		t.Fatalf("fallback from C wraps = %d, want 0", got)
	}
	// Single endpoint → no fallback possible.
	pool1 := newEndpointPool([]string{"A"}, nil)
	if got := pool1.pickFallback(0, now); got != -1 {
		t.Fatalf("single-endpoint fallback = %d, want -1", got)
	}
}

func TestRecordScriptStatsParsesGoodJSON(t *testing.T) {
	cli, _, cleanup := newAppsScriptHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()
	body := []byte(`{"ok":true,"date":"2026-05-09","count":1234,"version":1,"protocol":1}`)
	cli.recordScriptStatsLocked(0, body)
	snap := cli.pool.snapshot()
	if snap[0].scriptCount != 1234 {
		t.Fatalf("scriptCount = %d, want 1234", snap[0].scriptCount)
	}
	if snap[0].scriptCountAt.IsZero() {
		t.Fatalf("scriptCountAt not set on successful parse")
	}
}

func TestRecordScriptStatsTolerantOfLegacyHTML(t *testing.T) {
	cli, _, cleanup := newAppsScriptHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()
	cli.recordScriptStatsLocked(0, []byte("<html>not json</html>"))
	snap := cli.pool.snapshot()
	if snap[0].scriptCount != 0 {
		t.Fatalf("legacy body should not set scriptCount, got %d", snap[0].scriptCount)
	}
	if !snap[0].scriptStatsErrLogged {
		t.Fatalf("legacy body should set scriptStatsErrLogged once")
	}
}

func TestSanityScriptStatsResponseShape(t *testing.T) {
	// Catches accidental field rename mismatches with apps_script/Code.gs.
	body := []byte(`{"ok":true,"date":"2026-05-09","count":42,"version":1,"protocol":1}`)
	var s scriptStatsResponse
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if !s.OK || s.Count != 42 || s.Date != "2026-05-09" {
		t.Fatalf("unmarshal: %+v", s)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	cli, _, cleanup := newAppsScriptHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()
	if err := cli.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := cli.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := cli.Roundtrip(context.Background(), []byte("x")); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("expected ErrClosed after Close, got %v", err)
	}
}
