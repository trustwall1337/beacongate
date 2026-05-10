package bindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/server/policy"
	serverruntime "github.com/trustwall1337/beacongate/server/runtime"
	"github.com/trustwall1337/beacongate/server/upstream"
)

// resetGlobals clears the package's tunnel state between tests. The
// facade is intentionally a singleton ("one tunnel per process"), so
// tests that exercise StartTunnel must clean up after themselves.
// Tests use t.Cleanup(resetGlobals) to be safe even on panic.
func resetGlobals(t *testing.T) {
	t.Helper()
	_ = StopTunnel() // idempotent — no-op if nothing running
	currentSink.Store(nil)
}

// itPolicyEvaluator mirrors test/integration's adapter so tests in
// this package can stand up a real BeaconGate server in-process. We
// deliberately import the same crypto + server packages the
// integration tests use rather than mocking, because the goal is to
// exercise the *whole* facade including transport construction.
type itPolicyEvaluator struct{ engine *policy.Engine }

func (p *itPolicyEvaluator) Evaluate(t any) serverruntime.PolicyDecision {
	// Always-allow policy for tests; the binding facade doesn't
	// care about target evaluation, only about lifecycle.
	return serverruntime.PolicyDecision{Allowed: true}
}

// startTestServer spins up an in-process BeaconGate server with a
// fresh random key and returns:
//   - the test server URL ("http://127.0.0.1:NNN/tunnel")
//   - a JSON-encoded ClientConfig pointing at it (using the "https"
//     transport because it's the simplest in-process path; the
//     facade exercises the same code path either way).
//   - a teardown function.
func startTestServer(t *testing.T) (tunnelURL, clientCfgJSON string, teardown func()) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := upstream.NewNetDialer(2*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	dialer.Safety.AllowPrivate = true
	engine := policy.NewEngine()
	srv := serverruntime.New(
		"server-bindings-test",
		crypto.SingleKeyRegistryFromSealer(sealer),
		dialer,
		nil, // nil → AllowAll, suitable for facade lifecycle tests
	)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	mux.Handle("/healthz", srv.Health())
	ts := httptest.NewServer(mux)

	cfg := config.ClientConfig{
		ClientID:   "test-friend",
		ListenAddr: "127.0.0.1:0", // 0 → kernel picks a free port
		Server: config.ClientServerConfig{
			URL: ts.URL + "/tunnel",
			Key: config.EncodeKey(key),
		},
		Transport: config.ClientTransportConfig{Type: "https"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	teardown = func() {
		ts.Close()
		_ = srv.Close()
		_ = engine // keep referenced to silence "unused" if engine is unused later
	}
	return ts.URL + "/tunnel", string(body), teardown
}

// --- ImportConfig tests ---

func TestImportConfigJSONHappyPath(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()

	snap, err := ImportConfig(cfgJSON)
	if err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if snap.ClientID != "test-friend" {
		t.Fatalf("ClientID: got %q want %q", snap.ClientID, "test-friend")
	}
	if snap.TransportType != "https" {
		t.Fatalf("TransportType: got %q want %q", snap.TransportType, "https")
	}
	if snap.raw == nil {
		t.Fatalf("raw config should be populated for downstream StartTunnel")
	}
}

func TestImportConfigBgURIHappyPath(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()

	// Round-trip through bg:// to verify the share-link path.
	var cfg config.ClientConfig
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	link, err := config.EncodeLink(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ImportConfig(link)
	if err != nil {
		t.Fatalf("ImportConfig(bg://): %v", err)
	}
	if snap.ClientID != "test-friend" {
		t.Fatalf("ClientID via bg://: got %q", snap.ClientID)
	}
}

func TestImportConfigBgURIWithSurroundingWhitespace(t *testing.T) {
	// Friends pasting from WhatsApp often include trailing newlines.
	// The trim should handle that without forcing the platform side
	// to pre-process the input.
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()
	var cfg config.ClientConfig
	_ = json.Unmarshal([]byte(cfgJSON), &cfg)
	link, _ := config.EncodeLink(&cfg)
	if _, err := ImportConfig("  \n  " + link + "\n"); err != nil {
		t.Fatalf("ImportConfig should trim whitespace: %v", err)
	}
}

func TestImportConfigRejectsEmpty(t *testing.T) {
	if _, err := ImportConfig(""); err == nil {
		t.Fatalf("expected error on empty input")
	}
	if _, err := ImportConfig("   \n\t  "); err == nil {
		t.Fatalf("expected error on whitespace-only input")
	}
}

func TestImportConfigRejectsInvalidJSON(t *testing.T) {
	_, err := ImportConfig(`{"client_id": "x", "listen_addr": "127.0.0.1:1080"`) // truncated
	if err == nil || !strings.Contains(err.Error(), "parse JSON") {
		t.Fatalf("expected JSON-parse error, got %v", err)
	}
}

func TestImportConfigRejectsInvalidConfig(t *testing.T) {
	// Valid JSON shape but fails ClientConfig.Validate (missing
	// transport.type).
	body := `{"client_id":"x","listen_addr":"127.0.0.1:1080","server":{"url":"https://x.example/t","key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},"transport":{}}`
	_, err := ImportConfig(body)
	if err == nil || !strings.Contains(err.Error(), "validate") {
		t.Fatalf("expected validate error, got %v", err)
	}
}

func TestImportConfigRejectsBadBgURI(t *testing.T) {
	_, err := ImportConfig("bg://this-is-not-a-real-link")
	if err == nil || !strings.Contains(err.Error(), "decode share-link") {
		t.Fatalf("expected share-link decode error, got %v", err)
	}
}

// --- Lifecycle tests ---

func TestStartStopTunnelHappyPath(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()

	snap, err := ImportConfig(cfgJSON)
	if err != nil {
		t.Fatal(err)
	}

	// Status before start: stopped.
	pre := Status()
	if pre.State != "stopped" {
		t.Fatalf("pre-start state: got %q want %q", pre.State, "stopped")
	}

	if err := StartTunnel(snap); err != nil {
		t.Fatalf("StartTunnel: %v", err)
	}

	// SOCKS listener should be bound somewhere; the runtime now
	// reports a non-stopped state.
	mid := Status()
	if mid.State == "stopped" {
		t.Fatalf("post-start state should not be 'stopped'")
	}
	if mid.ClientID != "test-friend" {
		t.Fatalf("post-start ClientID: got %q", mid.ClientID)
	}

	if err := StopTunnel(); err != nil {
		t.Fatalf("StopTunnel: %v", err)
	}

	post := Status()
	if post.State != "stopped" {
		t.Fatalf("post-stop state: got %q want %q", post.State, "stopped")
	}
}

func TestStartTunnelRefusesIfAlreadyRunning(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()
	snap, _ := ImportConfig(cfgJSON)
	if err := StartTunnel(snap); err != nil {
		t.Fatal(err)
	}
	defer StopTunnel()

	err := StartTunnel(snap)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second StartTunnel must reject; got %v", err)
	}
}

func TestStartTunnelFailsOnPortInUse(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()

	// Bind an arbitrary port first so the SOCKS listener will
	// fail to bind it.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	// Rewrite the config to use that port. Cheap: just patch the
	// JSON.
	cfgJSON = strings.Replace(cfgJSON, `"listen_addr":"127.0.0.1:0"`,
		fmt.Sprintf(`"listen_addr":%q`, blocker.Addr().String()), 1)

	snap, err := ImportConfig(cfgJSON)
	if err != nil {
		t.Fatal(err)
	}
	err = StartTunnel(snap)
	if err == nil {
		_ = StopTunnel()
		t.Fatalf("StartTunnel must fail when SOCKS port is in use")
	}
	if !strings.Contains(err.Error(), "SOCKS listen") {
		t.Fatalf("error should mention SOCKS listen failure, got %v", err)
	}

	// And the package globals should have been cleaned up; a
	// fresh StartTunnel with an unused port should work.
	if Status().State != "stopped" {
		t.Fatalf("after failed start, Status must be 'stopped'")
	}
}

func TestStopTunnelIdempotent(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	// No tunnel running.
	if err := StopTunnel(); err != nil {
		t.Fatalf("StopTunnel on idle: %v", err)
	}
	// Second call still fine.
	if err := StopTunnel(); err != nil {
		t.Fatalf("StopTunnel x2 on idle: %v", err)
	}
}

func TestStartTunnelRejectsNilOrUnimported(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	if err := StartTunnel(nil); err == nil {
		t.Fatalf("nil snapshot should reject")
	}
	bare := &ConfigSnapshot{ClientID: "x"} // raw is nil
	if err := StartTunnel(bare); err == nil {
		t.Fatalf("snapshot with nil raw should reject")
	}
}

// --- Status / Stats / Version ---

func TestStatusWhenStoppedReturnsStoppedSnapshot(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	s := Status()
	if s == nil {
		t.Fatal("Status must never return nil")
	}
	if s.State != "stopped" {
		t.Fatalf("State: got %q want %q", s.State, "stopped")
	}
	if s.ClientID != "" || s.ListenAddr != "" || s.TransportType != "" {
		t.Fatalf("stopped snapshot should have empty scalar fields, got %+v", s)
	}
}

func TestGetStatsAlwaysSafe(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	// v1 stub: always zeros, never nil, even with no tunnel.
	s := GetStats()
	if s == nil {
		t.Fatal("GetStats must never return nil")
	}
	if s.BytesIn != 0 || s.BytesOut != 0 || s.SessionCount != 0 {
		t.Fatalf("v1 GetStats stub should be zeroed, got %+v", s)
	}
}

func TestVersionNonEmpty(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version must be non-empty")
	}
	if !strings.Contains(v, ".") {
		t.Fatalf("Version should be 'major.minor', got %q", v)
	}
}

// --- LogSink tests ---

// captureSink is a thread-safe LogSink that accumulates calls per
// severity, used to verify the slog → LogSink bridge.
type captureSink struct {
	mu             sync.Mutex
	debugs, infos  []string
	warns, errors_ []string
}

func (c *captureSink) Debug(m string) { c.mu.Lock(); c.debugs = append(c.debugs, m); c.mu.Unlock() }
func (c *captureSink) Info(m string)  { c.mu.Lock(); c.infos = append(c.infos, m); c.mu.Unlock() }
func (c *captureSink) Warn(m string)  { c.mu.Lock(); c.warns = append(c.warns, m); c.mu.Unlock() }
func (c *captureSink) Error(m string) {
	c.mu.Lock()
	c.errors_ = append(c.errors_, m)
	c.mu.Unlock()
}

func TestSetLogSinkRoutesByLevel(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	sink := &captureSink{}
	SetLogSink(sink)

	logger := currentLogger()
	logger.Debug("dbg-msg", "k", "v")
	logger.Info("info-msg")
	logger.Warn("warn-msg")
	logger.Error("err-msg")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.debugs) != 1 || !strings.Contains(sink.debugs[0], "dbg-msg") {
		t.Fatalf("debug routing: %+v", sink.debugs)
	}
	if !strings.Contains(sink.debugs[0], "k=v") {
		t.Fatalf("attrs should be appended; got %q", sink.debugs[0])
	}
	if len(sink.infos) != 1 || !strings.Contains(sink.infos[0], "info-msg") {
		t.Fatalf("info routing: %+v", sink.infos)
	}
	if len(sink.warns) != 1 || !strings.Contains(sink.warns[0], "warn-msg") {
		t.Fatalf("warn routing: %+v", sink.warns)
	}
	if len(sink.errors_) != 1 || !strings.Contains(sink.errors_[0], "err-msg") {
		t.Fatalf("error routing: %+v", sink.errors_)
	}
}

func TestSetLogSinkNilSilencesGracefully(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	SetLogSink(nil)
	logger := currentLogger()
	// Must not panic, even with a nil sink.
	logger.Info("dropped")
	logger.Error("also-dropped")
}

func TestSetLogSinkSwapWhileRunning(t *testing.T) {
	t.Cleanup(func() { resetGlobals(t) })
	_, cfgJSON, teardown := startTestServer(t)
	defer teardown()

	first := &captureSink{}
	SetLogSink(first)

	snap, _ := ImportConfig(cfgJSON)
	if err := StartTunnel(snap); err != nil {
		t.Fatal(err)
	}
	defer StopTunnel()

	// Swap the sink mid-run; subsequent logs should reach the
	// new sink, not the old one. We don't trigger any specific
	// log here because the runtime's log volume is event-driven;
	// the test verifies the swap mechanism doesn't panic.
	second := &captureSink{}
	SetLogSink(second)

	// The runtime's logger pointer should now reference the new
	// handler. We don't have a public accessor for it, but
	// currentLogger should return the bridge wired to `second`.
	currentLogger().Info("post-swap")
	second.mu.Lock()
	if len(second.infos) != 1 {
		second.mu.Unlock()
		t.Fatalf("post-swap log should land on second sink")
	}
	second.mu.Unlock()

	first.mu.Lock()
	for _, m := range first.infos {
		if strings.Contains(m, "post-swap") {
			first.mu.Unlock()
			t.Fatal("post-swap log leaked to first sink")
		}
	}
	first.mu.Unlock()
}

// --- Sanity: the var _ = errors line keeps `errors` import live in
// case a future test wants it; gomobile-bound packages need clean
// imports because cyclic-detection in the bind tooling is strict. ---
var _ = errors.New
