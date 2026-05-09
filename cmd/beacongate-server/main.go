// Command beacongate-server is the BeaconGate server process. It accepts
// encrypted batches over HTTP, enforces outbound policy, dials upstream
// destinations, and answers with batched responses.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/server/admin"
	"github.com/trustwall1337/beacongate/server/policy"
	serverruntime "github.com/trustwall1337/beacongate/server/runtime"
	"github.com/trustwall1337/beacongate/server/upstream"
)

// buildLogger constructs a slog.Logger writing to stderr at the requested
// level (debug|info|warn|error) in the requested format (text|json).
// Defaults are info + text; bad values fall back to those.
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

// policyEvaluator adapts a policy.Engine to runtime.PolicyEvaluator. Living
// in the binary keeps the dependency direction clean: server/runtime never
// imports server/policy, and server/policy never imports server/runtime.
type policyEvaluator struct{ engine *policy.Engine }

func (p policyEvaluator) Evaluate(target protocol.Target) serverruntime.PolicyDecision {
	d := p.engine.Evaluate(target.Host, target.Port)
	return serverruntime.PolicyDecision{Allowed: d.Allowed, Reason: d.Reason}
}

func main() {
	cfgPath := flag.String("config", "server_config.json", "path to server config JSON")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	logFormat := flag.String("log-format", "text", "log format: text|json")
	flag.Parse()

	logger := buildLogger(*logLevel, *logFormat)
	slog.SetDefault(logger)

	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	keyBytes, err := cfg.KeyBytes()
	if err != nil {
		log.Fatalf("key: %v", err)
	}
	sealer, err := crypto.NewSealer(keyBytes)
	if err != nil {
		log.Fatalf("sealer: %v", err)
	}

	engine := policy.NewEngine()
	if cfg.Policy.BaselineEnabled {
		engine.Replace(policy.Baseline())
	}
	var store policy.Store
	if cfg.Policy.StorePath != "" {
		fs, err := policy.OpenFileStore(cfg.Policy.StorePath)
		if err != nil {
			log.Fatalf("policy store: %v", err)
		}
		store = fs
		// Fold persisted rules over the baseline (operator overrides win).
		engine.Replace(append(engine.Rules(), fs.List()...))
	}

	dialer, err := upstream.NewNetDialer(10*time.Second, cfg.UpstreamProxy)
	if err != nil {
		log.Fatalf("upstream dialer: %v", err)
	}
	dialer.Safety = upstream.SafetyConfig{
		AllowPrivate:  cfg.Safety.AllowPrivate,
		AllowMetadata: cfg.Safety.AllowMetadata,
		ExtraBlocked:  cfg.Safety.ExtraBlocked,
	}
	if cfg.UpstreamProxy != "" {
		logger.Info("server.upstream_proxy", "url", cfg.UpstreamProxy)
	}
	srv := serverruntime.New(cfg.ServerID, sealer, dialer, policyEvaluator{engine: engine})
	srv.SetLogger(logger.With("service", "server", "server_id", cfg.ServerID))
	if cfg.Limits.MaxSessionsPerClient > 0 {
		srv.SetMaxSessionsPerClient(cfg.Limits.MaxSessionsPerClient)
	}
	if cfg.Limits.IdleSessionTimeoutSeconds > 0 {
		srv.SetIdleSessionTimeout(time.Duration(cfg.Limits.IdleSessionTimeoutSeconds) * time.Second)
	}

	tunnelPath := cfg.TunnelPath
	if tunnelPath == "" {
		tunnelPath = "/tunnel"
	}
	healthPath := cfg.HealthPath
	if healthPath == "" {
		healthPath = "/healthz"
	}
	mux := http.NewServeMux()
	mux.Handle(tunnelPath, srv.Tunnel())
	mux.Handle(healthPath, srv.Health())

	// M2: full timeouts. WriteTimeout must be >= the long-poll hold (25s) plus
	// time to encode/seal/write the response, otherwise legitimate long-polls
	// would be cut off.
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      40 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverLog := logger.With("service", "server", "server_id", cfg.ServerID)
	go func() {
		serverLog.Info("startup.listening",
			"addr", cfg.ListenAddr, "tunnel_path", tunnelPath, "health_path", healthPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverLog.Error("http.serve_failed", "error", err.Error())
		}
	}()

	var adminSrv *http.Server
	if cfg.Admin.Enabled {
		mode := admin.AuthLocalOnly
		if cfg.Admin.Token != "" {
			mode = admin.AuthBearerToken
		}
		api := admin.New(admin.AuthConfig{Mode: mode, Token: cfg.Admin.Token}, store, engine, srv)
		adminSrv = &http.Server{
			Addr:              cfg.Admin.ListenAddr,
			Handler:           api.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		go func() {
			serverLog.Info("admin_api.listening",
				"addr", cfg.Admin.ListenAddr, "mode", mode)
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverLog.Error("admin_api.failed", "error", err.Error())
			}
		}()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	serverLog.Info("shutting_down")
	shutCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = httpSrv.Shutdown(shutCtx)
	if adminSrv != nil {
		_ = adminSrv.Shutdown(shutCtx)
	}
	_ = srv.Close()
}
