// Command beacongate-server is the BeaconGate server process. It accepts
// encrypted batches over HTTP, enforces outbound policy, dials upstream
// destinations, and answers with batched responses.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	flag.Parse()

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

	dialer := upstream.NewNetDialer(10 * time.Second)
	dialer.Safety = upstream.SafetyConfig{
		AllowPrivate:  cfg.Safety.AllowPrivate,
		AllowMetadata: cfg.Safety.AllowMetadata,
		ExtraBlocked:  cfg.Safety.ExtraBlocked,
	}
	srv := serverruntime.New(cfg.ServerID, sealer, dialer, policyEvaluator{engine: engine})
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
	go func() {
		log.Printf("beacongate-server listening on %s (tunnel=%s, health=%s)", cfg.ListenAddr, tunnelPath, healthPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
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
			log.Printf("admin API listening on %s (mode=%v)", cfg.Admin.ListenAddr, mode)
			if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("admin: %v", err)
			}
		}()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	log.Printf("shutting down")
	shutCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	httpSrv.Shutdown(shutCtx)
	if adminSrv != nil {
		adminSrv.Shutdown(shutCtx)
	}
	srv.Close()
}

var _ = os.Args
