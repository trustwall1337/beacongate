// Package integration ties every layer together and runs against an
// in-process echo upstream and a real HTTP server. Failures here are
// the loudest signal that a refactor broke a cross-package contract.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	clientruntime "github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/client/socks"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport/google"
	"github.com/trustwall1337/beacongate/server/policy"
	serverruntime "github.com/trustwall1337/beacongate/server/runtime"
	"github.com/trustwall1337/beacongate/server/upstream"
)

// itPolicyEvaluator is a thin adapter from policy.Engine to the
// runtime.PolicyEvaluator interface. The integration test owns the wiring
// just like the server binary does, keeping policy and runtime decoupled.
type itPolicyEvaluator struct{ engine *policy.Engine }

func (p *itPolicyEvaluator) Evaluate(t protocol.Target) serverruntime.PolicyDecision {
	d := p.engine.Evaluate(t.Host, t.Port)
	return serverruntime.PolicyDecision{Allowed: d.Allowed, Reason: d.Reason}
}

type harness struct {
	tunnelURL  string
	clientCfg  *config.ClientConfig
	clientRT   *clientruntime.Runtime
	socksAddr  net.Addr
	socksSrv   *socks.Server
	pump       *clientruntime.Pump
	echoStop   func()
	tunnelStop func()
	serverObj  *serverruntime.Server
	engine     *policy.Engine
}

func (h *harness) cleanup() {
	if h.socksSrv != nil {
		h.socksSrv.Close()
	}
	if h.pump != nil {
		h.pump.Close()
	}
	if h.clientRT != nil {
		h.clientRT.Close()
	}
	if h.serverObj != nil {
		h.serverObj.Close()
	}
	if h.tunnelStop != nil {
		h.tunnelStop()
	}
	if h.echoStop != nil {
		h.echoStop()
	}
}

func startEcho(t *testing.T) (string, uint16, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()
	host, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	return host, uint16(port), func() { l.Close() }
}

func setup(t *testing.T) *harness {
	t.Helper()
	echoHost, echoPort, echoStop := startEcho(t)
	_ = echoHost
	_ = echoPort

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}

	engine := policy.NewEngine()
	dialer := upstream.NewNetDialer(2 * time.Second)
	dialer.Safety.AllowPrivate = true // integration test uses loopback echo
	srv := serverruntime.New("server-it", sealer, dialer, &itPolicyEvaluator{engine: engine})
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv.Tunnel())
	mux.Handle("/healthz", srv.Health())
	ts := httptest.NewServer(mux)

	cfg := &config.ClientConfig{
		ClientID:   "client-it",
		ListenAddr: "127.0.0.1:0",
		Server:     config.ClientServerConfig{URL: ts.URL + "/tunnel", Key: config.EncodeKey(key)},
		Transport:  config.ClientTransportConfig{Type: "google"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tr, err := google.New(google.Config{URL: cfg.Server.URL, HealthURL: ts.URL + "/healthz"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := clientruntime.New(cfg, tr)
	if err != nil {
		t.Fatal(err)
	}
	pump := clientruntime.NewPump(rt)
	pump.Start()

	socksSrv := socks.NewServer(pump)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go socksSrv.Serve(l)

	return &harness{
		tunnelURL: ts.URL + "/tunnel",
		clientCfg: cfg,
		clientRT:  rt,
		socksAddr: l.Addr(),
		socksSrv:  socksSrv,
		pump:      pump,
		echoStop: func() {
			echoStop()
		},
		tunnelStop: ts.Close,
		serverObj:  srv,
		engine:     engine,
	}
}

func socksConnect(t *testing.T, addr string, host string, port uint16) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(c, greeting); err != nil {
		t.Fatal(err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	pb := []byte{byte(port >> 8), byte(port & 0xff)}
	req = append(req, pb...)
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		t.Fatal(err)
	}
	tail := make([]byte, 6)
	if _, err := io.ReadFull(c, tail); err != nil {
		t.Fatal(err)
	}
	if hdr[1] != 0 {
		c.Close()
		t.Fatalf("socks reply rep=%d", hdr[1])
	}
	return c
}

func TestEndToEndEcho(t *testing.T) {
	h := setup(t)
	defer h.cleanup()
	echoHost, echoPort, _ := startEcho(t) // dedicated echo for this test
	defer func() {}()

	conn := socksConnect(t, h.socksAddr.String(), echoHost, echoPort)
	defer conn.Close()

	want := []byte("end-to-end roundtrip")
	if _, err := conn.Write(want); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEndToEndPolicyDeny(t *testing.T) {
	h := setup(t)
	defer h.cleanup()

	// Block 127.0.0.1 entirely so any echo attempt fails fast.
	h.engine.Replace([]policy.Rule{{
		ID: "block-loopback", Enabled: true, Action: policy.ActionBlock,
		Match: policy.MatchCIDR, Pattern: "127.0.0.0/8",
		Reason: "test deny",
	}})

	echoHost, echoPort, stop := startEcho(t)
	defer stop()

	conn, err := net.Dial("tcp", h.socksAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte{0x05, 0x01, 0x00})
	io.ReadFull(conn, make([]byte, 2))
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(echoHost))}
	req = append(req, []byte(echoHost)...)
	req = append(req, byte(echoPort>>8), byte(echoPort&0xff))
	conn.Write(req)
	hdr := make([]byte, 10)
	io.ReadFull(conn, hdr)
	// SOCKS replies success because the client doesn't await server policy
	// before answering the local app. Subsequent reads should observe EOF
	// (RESET propagates to the session and closes inbox).
	if hdr[1] != 0 {
		t.Fatalf("expected SOCKS success, got %d", hdr[1])
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		t.Fatalf("expected EOF after policy deny, got %d bytes %q", n, buf[:n])
	}
}

func TestEndToEndProbe(t *testing.T) {
	h := setup(t)
	defer h.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := h.clientRT.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status %q", resp.Status)
	}
}

// helper to ensure imports are used regardless of test selection
var _ = fmt.Sprintf
