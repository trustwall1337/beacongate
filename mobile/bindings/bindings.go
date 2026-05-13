package bindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	clientruntime "github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/client/socks"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport"
	"github.com/trustwall1337/beacongate/engine/transport/appsscript"
	httpstransport "github.com/trustwall1337/beacongate/engine/transport/https"
)

// One-tunnel-per-process state. Android only ever runs a single
// BeaconGate tunnel at a time; this matches the OS-level constraint
// that a VpnService instance owns a single TUN device.
//
// All globals are guarded by mu. ImportConfig is read-only and does
// not need the lock.
var (
	mu              sync.Mutex
	activeRT        *clientruntime.Runtime
	activePump      *clientruntime.Pump
	activeSOCKS     *socks.Server
	activeTransport transport.ClientTransport
)

// currentRuntime is a lock-free helper used by SetLogSink (which
// runs from the platform thread and shouldn't block on mu) to swap
// the logger of an already-running tunnel. Reads are racy with
// StartTunnel/StopTunnel but the result is only ever used to call
// the always-thread-safe Runtime.SetLogger.
func currentRuntime() *clientruntime.Runtime {
	mu.Lock()
	defer mu.Unlock()
	return activeRT
}

// ImportConfig parses jsonOrBgURI as either:
//
//   - A `bg://config?d=...&v=1` share-link (engine/config.DecodeLink).
//     This is the format `beacongate-admin export-link` produces.
//   - A raw client_config JSON document. This is the format
//     `beacongate-admin add-client` writes to the friend's `.bg`
//     file (despite the name; the file is just JSON).
//
// In both cases the input is validated via ClientConfig.Validate
// before the snapshot is returned. An invalid input returns an
// error that the platform UI surfaces verbatim.
//
// **Pure function:** does not start a tunnel, does not persist
// anything to disk. The platform side decides where to store the
// validated bytes (Android: EncryptedSharedPreferences).
func ImportConfig(jsonOrBgURI string) (*ConfigSnapshot, error) {
	trimmed := strings.TrimSpace(jsonOrBgURI)
	if trimmed == "" {
		return nil, errors.New("ImportConfig: input is empty")
	}

	var cfg *config.ClientConfig
	if strings.HasPrefix(trimmed, "bg://") {
		decoded, err := config.DecodeLink(trimmed)
		if err != nil {
			return nil, fmt.Errorf("ImportConfig: decode share-link: %w", err)
		}
		cfg = decoded
	} else {
		// Raw JSON path. Use a strict decoder so unknown fields
		// surface as errors instead of silently dropping
		// (mirrors engine/config.loadJSON's DisallowUnknownFields).
		var c config.ClientConfig
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("ImportConfig: parse JSON: %w", err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("ImportConfig: validate: %w", err)
		}
		cfg = &c
	}

	return &ConfigSnapshot{
		raw:           cfg,
		ClientID:      cfg.ClientID,
		ListenAddr:    cfg.ListenAddr,
		TransportType: cfg.Transport.Type,
	}, nil
}

// StartTunnel constructs the transport, runtime, pump, and SOCKS
// listener from cfg, and brings them up. Returns an error if a
// tunnel is already running, if any layer's construction fails, or
// if the SOCKS listener cannot bind cfg.raw.ListenAddr (e.g., port
// already in use).
//
// On error, all partially-constructed resources are torn down — the
// caller never has to worry about leaked goroutines or open ports.
//
// On success, the package globals (activeRT/Pump/SOCKS/Transport)
// hold strong references to the running components; subsequent
// Status / GetStats / StopTunnel calls operate on them.
//
// **Synchronous startup, asynchronous serve:** by the time this
// returns, the SOCKS listener has bound its port and is accepting
// connections. The blocking Accept loop runs on its own goroutine.
func StartTunnel(cfg *ConfigSnapshot) error {
	if cfg == nil || cfg.raw == nil {
		return errors.New("StartTunnel: cfg is nil or unimported")
	}

	mu.Lock()
	defer mu.Unlock()

	if activePump != nil {
		return errors.New("StartTunnel: a tunnel is already running; call StopTunnel first")
	}

	tr, err := buildTransport(cfg.raw)
	if err != nil {
		return fmt.Errorf("StartTunnel: build transport: %w", err)
	}

	rt, err := clientruntime.New(cfg.raw, tr)
	if err != nil {
		_ = tr.Close()
		return fmt.Errorf("StartTunnel: build runtime: %w", err)
	}
	rt.SetLogger(currentLogger())

	pump := clientruntime.NewPump(rt)
	if cfg.raw.CoalesceStepMs > 0 {
		pump.SetCoalesceWindow(time.Duration(cfg.raw.CoalesceStepMs) * time.Millisecond)
	}
	pump.Start()

	srv := socks.NewServer(pump)
	// Android VPN path sends DNS as UDP; tun2socks uses SOCKS5 UDP ASSOCIATE
	// for that traffic, so mobile must enable it on the local SOCKS listener.
	srv.EnableUDPAssociate(true)
	// Concurrent-association cap: tun2socks creates a NEW
	// ASSOCIATE per UDP flow (5-tuple), and Chrome's DNS-prefetch
	// burst fires 50+ queries per page load. With the earlier
	// limit of 48 we'd be rejected mid-burst before our 1.5s
	// idle TTL freed slots. 512 leaves comfortable headroom
	// without endangering FDs (we raise RLIMIT_NOFILE to 8192
	// at StartVpn — see mobile/bindings/tun2socks.go).
	srv.SetUDPAssociateLimit(512)
	if cfg.raw.Socks.Username != "" {
		srv.SetAuth(socks.AuthConfig{
			Username: cfg.raw.Socks.Username,
			Password: cfg.raw.Socks.Password,
		})
	}

	// Race the listener bind against a 100ms grace window. If the
	// bind fails (port in use, etc.), the goroutine returns the
	// error immediately and we tear everything down. If it
	// succeeds, the goroutine blocks in Accept and we return.
	listenErr := make(chan error, 1)
	go func() {
		listenErr <- srv.ListenAndServe(cfg.raw.ListenAddr)
	}()
	select {
	case err := <-listenErr:
		_ = srv.Close()
		_ = pump.Close()
		_ = rt.Close()
		return fmt.Errorf("StartTunnel: SOCKS listen %q: %w", cfg.raw.ListenAddr, err)
	case <-time.After(100 * time.Millisecond):
		// Listener is up. Drain the channel later via the
		// goroutine that owns the Accept loop; on Stop, srv.Close
		// makes ListenAndServe return nil.
	}

	activeRT = rt
	activePump = pump
	activeSOCKS = srv
	activeTransport = tr
	return nil
}

// StopTunnel tears down the active tunnel, closing the SOCKS
// listener, draining the pump, and releasing the transport's
// connections. Idempotent: calling StopTunnel when no tunnel is
// running returns nil.
//
// Order matters: SOCKS first (refuse new app connections), then
// pump (drain existing sessions), then runtime (closes the
// transport via Runtime.Close).
//
// Returns the first non-nil error encountered; subsequent layers
// are still torn down. Each layer's Close is safe to call multiple
// times.
func StopTunnel() error {
	mu.Lock()
	defer mu.Unlock()

	if activePump == nil {
		return nil
	}

	var firstErr error
	if err := activeSOCKS.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := activePump.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	// Runtime.Close also closes the underlying transport, so we
	// don't call activeTransport.Close() separately.
	if err := activeRT.Close(); err != nil && firstErr == nil {
		firstErr = err
	}

	activeRT = nil
	activePump = nil
	activeSOCKS = nil
	activeTransport = nil
	return firstErr
}

// Status returns a snapshot of the current tunnel state. When no
// tunnel is running, returns a snapshot with State="stopped" and
// empty/zero scalar fields — never nil, so the platform side can
// safely call accessors without null checks.
//
// **TransportHealthy** is computed via a 2-second-timeout call to
// Runtime.Diagnose() on each Status() invocation. If the platform
// polls Status() every second (typical), this stays cheap on the
// transport side because Diagnose's underlying probe has its own
// internal cache (engine/transport/appsscript caches ServerHello
// freshness for a few seconds).
func Status() *StatusSnapshot {
	mu.Lock()
	defer mu.Unlock()

	if activeRT == nil {
		return &StatusSnapshot{State: "stopped"}
	}

	cfg := activeRT.Config()
	lastErr, lastProbe := activeRT.StatusSnapshot()

	snap := &StatusSnapshot{
		State:         activeRT.State().String(),
		ClientID:      cfg.ClientID,
		ListenAddr:    cfg.ListenAddr,
		TransportType: cfg.Transport.Type,
		LastError:     lastErr,
	}
	if !lastProbe.IsZero() {
		snap.LastSuccessfulProbeMs = lastProbe.UnixMilli()
	}

	// Quick transport health check. 1.5s ceiling so a stuck
	// transport can't hang Status() for the platform UI.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if d, err := activeTransport.Diagnose(ctx); err == nil {
		snap.TransportHealthy = d.Healthy
	}
	return snap
}

// WaitUntilHealthy polls Status() until TransportHealthy is true or timeout
// elapses. Returns nil on success; otherwise a descriptive error.
func WaitUntilHealthy(timeoutMs int64) error {
	if timeoutMs <= 0 {
		timeoutMs = 10_000
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		s := Status()
		if s.TransportHealthy {
			return nil
		}
		if s.LastError != "" {
			// Keep polling briefly to allow transient startup errors to clear.
		}
		time.Sleep(250 * time.Millisecond)
	}
	s := Status()
	if s.LastError != "" {
		return fmt.Errorf("transport unhealthy: %s", s.LastError)
	}
	return errors.New("transport unhealthy: timeout waiting for healthy probe")
}

// GetStats returns byte and session counters for the active tunnel.
// Returns a zeroed Stats when no tunnel is running.
//
// **v1 stub:** all counter fields return 0 in this version. Real
// counters require Pump instrumentation (atomic byte increments on
// each upstream/downstream chunk); this is deferred to a follow-up
// patch once the Android UI for byte display is wired up.
func GetStats() *Stats {
	mu.Lock()
	defer mu.Unlock()
	return &Stats{} // all zero; v1 stub
}

// Version returns the BeaconGate protocol version the binary
// implements (e.g. "1.1"). Surfaced in the Android "About" screen
// so the operator can verify the friend is running a recent build.
func Version() string {
	return fmt.Sprintf("%d.%d", protocol.ProtocolVersionMajor, protocol.ProtocolVersionMinor)
}

// buildTransport mirrors cmd/beacongate-client/main.go's
// buildTransport but inlined here to avoid importing main. Keeps
// the bindings module self-contained so `gomobile bind ./mobile/bindings`
// has a closed dep graph.
//
// Supported types: "appsscript" (the censorship-resistant fronted
// path) and "https" (direct relay, used in dev/desktop). The
// "google" alias from v1.0 is intentionally NOT supported on mobile
// — if a friend's config still uses it, ImportConfig will accept it
// (Validate accepts the alias), and StartTunnel will return a
// helpful error pointing at the rename.
func buildTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	switch cfg.Transport.Type {
	case "appsscript":
		return buildAppsScriptTransport(cfg)
	case "https":
		return buildHTTPSTransport(cfg)
	case "google":
		return nil, errors.New(`buildTransport: transport type "google" was renamed to "https" in v1.1; update the .bg config`)
	}
	return nil, fmt.Errorf("buildTransport: unknown transport type %q", cfg.Transport.Type)
}

func buildHTTPSTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	// Mobile build does NOT support pinned_roots_path because the
	// Android sandbox makes filesystem-relative paths fragile.
	// Friends who need cert pinning use a `.bg` whose embedded
	// roots are baked into the transport options as PEM bytes
	// directly; for v1 we simply don't honor pinned_roots_path
	// from mobile. Operators distributing mobile configs should
	// rely on the public CA chain.
	return httpstransport.New(httpstransport.Config{
		URL:          cfg.Server.URL,
		HealthURL:    cfg.Transport.OptionString("health_url"),
		FrontingHost: httpstransport.SanitizeFrontingHost(cfg.Transport.OptionString("fronting_host")),
	})
}

func buildAppsScriptTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	if cfg.Server.URL != "" {
		return nil, fmt.Errorf("buildAppsScriptTransport: server.url must be empty in appsscript mode (got %q)", cfg.Server.URL)
	}
	scriptKeys, err := config.ParseScriptKeys(cfg.Transport.Options["script_keys"])
	if err != nil {
		return nil, fmt.Errorf("buildAppsScriptTransport: %w", err)
	}
	if len(scriptKeys) == 0 {
		return nil, errors.New("buildAppsScriptTransport: script_keys parsed to zero entries")
	}
	return appsscript.New(appsscript.Config{
		ScriptKeys:     config.ScriptKeyIDs(scriptKeys),
		ScriptAccounts: config.ScriptKeyAccounts(scriptKeys),
		Fronting: appsscript.FrontingConfig{
			GoogleIP: cfg.Transport.OptionString("google_host"),
			SNIHosts: splitAndTrim(cfg.Transport.OptionString("sni")),
		},
	})
}

// splitAndTrim mirrors the desktop client's helper. Local copy so
// we don't import cmd/beacongate-client (which gomobile would
// reject — it's main package).
func splitAndTrim(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
