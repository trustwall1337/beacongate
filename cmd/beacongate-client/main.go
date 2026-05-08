// Command beacongate-client is the BeaconGate client process. It loads a
// JSON config, brings up the configured transport (Google by default), and
// serves a local SOCKS5 listener that bridges traffic through the relay.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trustwall1337/beacongate/client/control"
	clientruntime "github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/client/socks"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/transport"
	"github.com/trustwall1337/beacongate/engine/transport/google"
)

func main() {
	cfgPath := flag.String("config", "client_config.json", "path to client config JSON")
	controlAddr := flag.String("control-addr", "", "local control HTTP listen address (e.g. 127.0.0.1:9091)")
	flag.Parse()

	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	tr, err := buildTransport(cfg)
	if err != nil {
		log.Fatalf("transport: %v", err)
	}
	rt, err := clientruntime.New(cfg, tr)
	if err != nil {
		log.Fatalf("runtime: %v", err)
	}
	defer rt.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	diagCtx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	report := rt.RunStartupDiagnostics(diagCtx)
	dcancel()
	log.Printf("startup diagnostics: transport.healthy=%v probe.ok=%v err=%q", report.Transport.Healthy, report.ProbeOK, report.ProbeErr)

	pump := clientruntime.NewPump(rt)
	pump.Start()
	defer pump.Close()

	srv := socks.NewServer(pump)
	if cfg.Socks.Username != "" {
		srv.SetAuth(socks.AuthConfig{Username: cfg.Socks.Username, Password: cfg.Socks.Password})
	}
	go func() {
		if err := srv.ListenAndServe(cfg.ListenAddr); err != nil {
			log.Printf("socks server: %v", err)
		}
	}()
	log.Printf("beacongate-client listening on %s", cfg.ListenAddr)

	if *controlAddr != "" {
		api := control.New(rt)
		go func() {
			log.Printf("control API listening on %s", *controlAddr)
			if err := http.ListenAndServe(*controlAddr, api.Handler()); err != nil {
				log.Printf("control api: %v", err)
			}
		}()
	}

	<-ctx.Done()
	log.Printf("shutting down")
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
