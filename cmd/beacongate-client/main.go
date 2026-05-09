// Command beacongate-client is the BeaconGate client process. It loads a
// JSON config, brings up the configured transport (Google by default), and
// serves a local SOCKS5 listener that bridges traffic through the relay.
package main

import (
	"context"
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
	flag.Parse()

	logger := buildLogger(*logLevel, *logFormat)
	slog.SetDefault(logger)

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
	defer rt.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	diagCtx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	report := rt.RunStartupDiagnostics(diagCtx)
	dcancel()
	clientLogger.Info("startup.diagnostics",
		"transport_healthy", report.Transport.Healthy,
		"probe_ok", report.ProbeOK,
		"probe_err", report.ProbeErr)

	pump := clientruntime.NewPump(rt)
	pump.Start()
	defer pump.Close()

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
	srv.Close()
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
		HealthURL:    cfg.Transport.Options["health_url"],
		FrontingHost: httpstransport.SanitizeFrontingHost(cfg.Transport.Options["fronting_host"]),
	}
	if path := strings.TrimSpace(cfg.Transport.Options["pinned_roots_path"]); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("https transport: read pinned_roots_path %q: %w", path, err)
		}
		httpsCfg.PinnedRootsPEM = pem
	}
	return httpstransport.New(httpsCfg)
}

// buildAppsScriptTransport assembles an appsscript transport from
// transport.options. Required: script_keys (comma-separated). Optional:
// script_accounts (parallel labels), google_host, sni (comma-separated
// rotation list).
//
// In appsscript mode, server.url MUST be empty/omitted — the script
// URLs are constructed from script_keys. We enforce that here so a
// stale server.url doesn't silently bypass the disguise.
func buildAppsScriptTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	if cfg.Server.URL != "" {
		return nil, fmt.Errorf("appsscript transport: server.url must be empty in appsscript mode (got %q); the script URL is constructed from transport.options.script_keys", cfg.Server.URL)
	}
	rawKeys := cfg.Transport.Options["script_keys"]
	if strings.TrimSpace(rawKeys) == "" {
		return nil, fmt.Errorf("appsscript transport: transport.options.script_keys is required")
	}
	keys := splitAndTrim(rawKeys)
	if len(keys) == 0 {
		return nil, fmt.Errorf("appsscript transport: transport.options.script_keys parsed to zero entries")
	}
	accounts := splitAndTrim(cfg.Transport.Options["script_accounts"])

	fronting := appsscript.FrontingConfig{
		GoogleIP: cfg.Transport.Options["google_host"],
		SNIHosts: splitAndTrim(cfg.Transport.Options["sni"]),
	}
	return appsscript.New(appsscript.Config{
		ScriptKeys:     keys,
		ScriptAccounts: accounts,
		Fronting:       fronting,
	})
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
