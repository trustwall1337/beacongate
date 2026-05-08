package socks

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport/transporttest"
)

func u64(v uint64) *uint64 { return &v }

// fakeServer is a minimal in-process BeaconGate "server" that drives an
// echo upstream for any session. It plugs in as a FakeTransport handler on
// the client side.
type fakeServer struct {
	sealer *crypto.Sealer

	mu       sync.Mutex
	pending  map[string][][]byte // per-session inbound bytes waiting to be DATA-returned
	openSeq  map[string]uint64
	closeSes map[string]bool
}

func newFakeServer(sealer *crypto.Sealer) *fakeServer {
	return &fakeServer{
		sealer:   sealer,
		pending:  map[string][][]byte{},
		openSeq:  map[string]uint64{},
		closeSes: map[string]bool{},
	}
}

func (fs *fakeServer) handle(_ context.Context, ct []byte) ([]byte, error) {
	plain, err := fs.sealer.Open(ct)
	if err != nil {
		return nil, err
	}
	env, err := protocol.DecodeEnvelope(plain)
	if err != nil {
		return nil, err
	}
	out := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 0},
		ClientID:    "fake-server",
		Compression: protocol.CompressionNone,
	}
	for _, m := range env.Messages {
		switch m.Type {
		case protocol.MessageTypeOpen:
			fs.mu.Lock()
			fs.openSeq[m.SessionID] = 0
			fs.mu.Unlock()
		case protocol.MessageTypeData:
			// echo back the bytes as a DATA from the server
			fs.mu.Lock()
			seq := fs.openSeq[m.SessionID]
			fs.openSeq[m.SessionID] = seq + 1
			fs.mu.Unlock()
			out.Messages = append(out.Messages, protocol.Message{
				Type:      protocol.MessageTypeData,
				SessionID: m.SessionID,
				Seq:       u64(seq),
				Data:      append([]byte(nil), m.Data...),
			})
		case protocol.MessageTypeClose:
			fs.mu.Lock()
			fs.closeSes[m.SessionID] = true
			fs.mu.Unlock()
			out.Messages = append(out.Messages, protocol.Message{
				Type: protocol.MessageTypeClose, SessionID: m.SessionID,
			})
		case protocol.MessageTypeProbe:
			out.Messages = append(out.Messages, protocol.Message{
				Type: protocol.MessageTypeProbe, ProbeID: m.ProbeID,
				Status:            "ok",
				SupportedVersions: []protocol.Version{{Major: 1, Minor: 0}},
				SelectedVersion:   &protocol.Version{Major: 1, Minor: 0},
			})
		}
	}
	if len(out.Messages) == 0 {
		out.Messages = append(out.Messages, protocol.Message{
			Type: protocol.MessageTypeProbe, ProbeID: "noop", Status: "ok",
			SupportedVersions: []protocol.Version{{Major: 1, Minor: 0}},
		})
	}
	raw, err := protocol.EncodeEnvelope(out)
	if err != nil {
		return nil, err
	}
	return fs.sealer.Seal(raw)
}

func setupSocks(t *testing.T) (*Server, net.Addr, func()) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.ClientConfig{
		ClientID:   "client-test",
		ListenAddr: "127.0.0.1:0",
		Server:     config.ClientServerConfig{URL: "http://x", Key: config.EncodeKey(key)},
		Transport:  config.ClientTransportConfig{Type: "fake"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	fs := newFakeServer(sealer)
	ft := &transporttest.Fake{Handler: fs.handle}
	rt, err := runtime.New(cfg, ft)
	if err != nil {
		t.Fatal(err)
	}
	pump := runtime.NewPump(rt)
	pump.Start()

	srv := NewServer(pump)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	cleanup := func() {
		srv.Close()
		pump.Close()
		rt.Close()
	}
	return srv, l.Addr(), cleanup
}

func socksHandshake(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("greeting reply unexpected: %v", resp)
	}
}

func socksConnect(t *testing.T, conn net.Conn, host string, port uint16) []byte {
	t.Helper()
	req := []byte{0x05, cmdConnect, 0x00, atypDomain, byte(len(host))}
	req = append(req, []byte(host)...)
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, port)
	req = append(req, pb...)
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		t.Fatal(err)
	}
	tail := make([]byte, 6) // ipv4 + port
	if _, err := io.ReadFull(conn, tail); err != nil {
		t.Fatal(err)
	}
	return append(hdr, tail...)
}

func TestSocksRejectsUDPAssociate(t *testing.T) {
	_, addr, cleanup := setupSocks(t)
	defer cleanup()
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	socksHandshake(t, conn)
	req := []byte{0x05, cmdUDPAssociate, 0x00, atypIPv4, 127, 0, 0, 1, 0, 0}
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != repCmdNotSupport {
		t.Fatalf("expected cmd-not-supported, got %d", resp[1])
	}
}

func TestSocksRejectsBind(t *testing.T) {
	_, addr, cleanup := setupSocks(t)
	defer cleanup()
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	socksHandshake(t, conn)
	req := []byte{0x05, cmdBind, 0x00, atypIPv4, 127, 0, 0, 1, 0, 0}
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != repCmdNotSupport {
		t.Fatalf("expected cmd-not-supported, got %d", resp[1])
	}
}

func TestSocksConnectEcho(t *testing.T) {
	_, addr, cleanup := setupSocks(t)
	defer cleanup()
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	socksHandshake(t, conn)
	resp := socksConnect(t, conn, "echo.example.com", 80)
	if resp[1] != repSuccess {
		t.Fatalf("expected success, got %d", resp[1])
	}
	// Send some bytes; the fake server echoes them back.
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, []byte("hello")) {
		t.Fatalf("echo mismatch: %q", buf)
	}
}
