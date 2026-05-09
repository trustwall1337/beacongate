// Command beacongate-client is the BeaconGate client process. It loads a
// JSON config, brings up the configured transport (Google by default), and
// serves a local SOCKS5 listener that bridges traffic through the relay.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trustwall1337/beacongate/client/control"
	clientruntime "github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/client/socks"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/transport"
	"github.com/trustwall1337/beacongate/engine/transport/appsscript"
	httpstransport "github.com/trustwall1337/beacongate/engine/transport/https"
)

// buildLogger constructs a slog.Logger writing to stderr at the requested
// level (debug|info|warn|error) in the requested format (text|json).
func buildLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func main() {
	cfgPath := flag.String("config", "client_config.json", "path to client config JSON")
	controlAddr := flag.String("control-addr", "", "local control HTTP listen address (e.g. 127.0.0.1:9091)")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	logFormat := flag.String("log-format", "text", "log format: text|json")
	validateOnly := flag.Bool("validate-only", false, "validate the config and exit without starting the runtime; prints {\"ok\":bool,...} to stdout")
	flag.Parse()

	logger := buildLogger(*logLevel, *logFormat)
	slog.SetDefault(logger)

	if *validateOnly {
		os.Exit(runValidateOnly(*cfgPath))
	}

	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	clientLogger := logger.With("service", "client", "client_id", cfg.ClientID)

	tr, err := buildTransport(cfg)
	if err != nil {
		log.Fatalf("transport: %v", err)
	}
	rt, err := clientruntime.New(cfg, tr)
	if err != nil {
		log.Fatalf("runtime: %v", err)
	}
	rt.SetLogger(clientLogger)
	rt.SetActiveProfile(*cfgPath)
	defer func() { _ = rt.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	diagCtx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	report := rt.RunStartupDiagnostics(diagCtx)
	dcancel()
	clientLogger.Info("startup.diagnostics",
		"transport_healthy", report.Transport.Healthy,
		"probe_ok", report.ProbeOK,
		"probe_err", report.ProbeErr)
	if report.Transport.Healthy && report.ProbeOK {
		clientLogger.Info("preflight.ok",
			"summary", "relay healthy, AES key matches end-to-end",
			"elapsed_ms", report.Elapsed.Milliseconds())
	}

	pump := clientruntime.NewPump(rt)
	pump.Start()
	defer func() { _ = pump.Close() }()

	srv := socks.NewServer(pump)
	if cfg.Socks.Username != "" {
		srv.SetAuth(socks.AuthConfig{Username: cfg.Socks.Username, Password: cfg.Socks.Password})
	}
	go func() {
		if err := srv.ListenAndServe(cfg.ListenAddr); err != nil {
			clientLogger.Error("socks.serve_failed", "error", err.Error())
		}
	}()
	clientLogger.Info("startup.listening", "addr", cfg.ListenAddr)

	if *controlAddr != "" {
		api := control.New(rt)
		go func() {
			clientLogger.Info("control_api.listening", "addr", *controlAddr)
			if err := http.ListenAndServe(*controlAddr, api.Handler()); err != nil {
				clientLogger.Error("control_api.failed", "error", err.Error())
			}
		}()
	}

	<-ctx.Done()
	clientLogger.Info("shutting_down")
	_ = srv.Close()
}

func buildTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	switch cfg.Transport.Type {
	case "https":
		return buildHTTPSTransport(cfg)
	case "appsscript":
		return buildAppsScriptTransport(cfg)
	case "google":
		// Deprecated alias for "https". The package was renamed in v1.1
		// to match what it actually is (a generic HTTPS transport, not a
		// Google-disguised path). Accept the old type string for one
		// release so existing deployments keep working.
		log.Printf("transport.type=\"google\" is deprecated; rename to \"https\" in your config")
		return buildHTTPSTransport(cfg)
	}
	return nil, fmt.Errorf("unknown transport type %q", cfg.Transport.Type)
}

// buildHTTPSTransport assembles the direct HTTPS transport. Reads
// optional pinned_roots_path from transport.options to enable
// certificate pinning (plan C2): when set, points at a PEM file on
// disk; the file's certs become the only roots the transport will
// trust for TLS verification. Useful for self-hosted relays where
// the operator controls the cert chain.
func buildHTTPSTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	httpsCfg := httpstransport.Config{
		URL:          cfg.Server.URL,
		HealthURL:    cfg.Transport.OptionString("health_url"),
		FrontingHost: httpstransport.SanitizeFrontingHost(cfg.Transport.OptionString("fronting_host")),
	}
	if path := strings.TrimSpace(cfg.Transport.OptionString("pinned_roots_path")); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("https transport: read pinned_roots_path %q: %w", path, err)
		}
		httpsCfg.PinnedRootsPEM = pem
	}
	return httpstransport.New(httpsCfg)
}

// buildAppsScriptTransport assembles an appsscript transport from
// transport.options.
//
// **`script_keys` accepts both shapes** (per v1.1.0 schema flex):
//   - legacy comma-separated string: "ID1,ID2"
//   - Goose-natural array-of-objects: [{"id":"ID1","account":"alpha"}, ...]
//
// `script_accounts` is the parallel labels list (legacy comma-separated
// string only); when `script_keys` uses the object form, account labels
// are embedded per-entry and `script_accounts` is ignored.
//
// In appsscript mode, server.url MUST be empty/omitted — the script
// URLs are constructed from script_keys. Validate() in the config
// loader catches this; this defensive check is belt-and-suspenders
// in case BuildAppsScriptTransport is ever called from a code path
// that bypasses LoadClient.
func buildAppsScriptTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	if cfg.Server.URL != "" {
		return nil, fmt.Errorf("appsscript transport: server.url must be empty in appsscript mode (got %q); the script URL is constructed from transport.options.script_keys", cfg.Server.URL)
	}
	scriptKeys, err := config.ParseScriptKeys(cfg.Transport.Options["script_keys"])
	if err != nil {
		return nil, fmt.Errorf("appsscript transport: %w", err)
	}
	if len(scriptKeys) == 0 {
		return nil, fmt.Errorf("appsscript transport: transport.options.script_keys parsed to zero entries")
	}
	keys := config.ScriptKeyIDs(scriptKeys)
	accounts := config.ScriptKeyAccounts(scriptKeys)
	// If the operator used the legacy string shape and supplied a
	// parallel script_accounts string, splice those in over the
	// (currently empty) per-key accounts.
	if hasOnlyEmptyStrings(accounts) {
		if legacy := splitAndTrim(cfg.Transport.OptionString("script_accounts")); len(legacy) > 0 {
			// Use legacy values up to len(keys); pad with "" if shorter.
			merged := make([]string, len(keys))
			for i := range merged {
				if i < len(legacy) {
					merged[i] = legacy[i]
				}
			}
			accounts = merged
		}
	}

	fronting := appsscript.FrontingConfig{
		GoogleIP: cfg.Transport.OptionString("google_host"),
		SNIHosts: splitAndTrim(cfg.Transport.OptionString("sni")),
	}
	return appsscript.New(appsscript.Config{
		ScriptKeys:     keys,
		ScriptAccounts: accounts,
		Fronting:       fronting,
	})
}

// hasOnlyEmptyStrings reports whether the slice has zero non-empty
// entries.
func hasOnlyEmptyStrings(s []string) bool {
	for _, v := range s {
		if v != "" {
			return false
		}
	}
	return true
}

// runValidateOnly loads and validates the config at cfgPath, prints a
// JSON result to stdout, and returns the exit code. Used by ops/prepare-bundle.sh
// and by the verify.sh script bundled into the operator handoff zip.
//
// Output schema:
//
//	{"ok":true,"client_id":"...","transport":"appsscript","listen_addr":"127.0.0.1:1080"}
//	{"ok":false,"error":"<message>"}
func runValidateOnly(cfgPath string) int {
	enc := json.NewEncoder(os.Stdout)
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		_ = enc.Encode(map[string]any{"ok": false, "error": err.Error()})
		return 1
	}
	_ = enc.Encode(map[string]any{
		"ok":          true,
		"client_id":   cfg.ClientID,
		"transport":   cfg.Transport.Type,
		"listen_addr": cfg.ListenAddr,
	})
	return 0
}

// splitAndTrim splits a comma-separated string and trims whitespace
// from every entry. Empty entries are dropped.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
