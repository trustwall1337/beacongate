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
	"github.com/trustwall1337/beacongate/engine/transport/google"
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
	case "google":
		return google.New(google.Config{
			URL:          cfg.Server.URL,
			HealthURL:    cfg.Transport.Options["health_url"],
			FrontingHost: google.SanitizeFrontingHost(cfg.Transport.Options["fronting_host"]),
		})
	}
	return nil, fmt.Errorf("unknown transport type %q", cfg.Transport.Type)
}

var _ = os.Args // referenced via flag.Parse
