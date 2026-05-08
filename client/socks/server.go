// Package socks implements a SOCKS5 listener that proxies CONNECT requests
// through a BeaconGate client runtime. UDP and BIND are not supported.
package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/engine/protocol"
)

const (
	socksVersion = 0x05

	cmdConnect      = 0x01
	cmdBind         = 0x02
	cmdUDPAssociate = 0x03

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSuccess        = 0x00
	repGeneralFailure = 0x01
	repNotAllowed     = 0x02
	repNetUnreachable = 0x03
	repHostUnreach    = 0x04
	repConnRefused    = 0x05
	repCmdNotSupport  = 0x07

	authNoAuth   = 0x00
	authUserPass = 0x02
	authNoAccept = 0xff
)

// AuthConfig optionally enables RFC1929 username/password authentication on
// the SOCKS5 listener. When Username is empty, the server advertises
// "no auth" (the default and most permissive mode); when set, it requires a
// matching credential pair.
type AuthConfig struct {
	Username string
	Password string
}

func (a AuthConfig) Enabled() bool { return a.Username != "" }

type Server struct {
	pump *runtime.Pump
	auth AuthConfig

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

func NewServer(pump *runtime.Pump) *Server {
	return &Server{pump: pump}
}

// SetAuth turns on RFC1929 username/password authentication. Empty username
// disables auth (the no-auth default).
func (s *Server) SetAuth(a AuthConfig) {
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
}

// ListenAndServe binds addr and serves until the listener returns an error
// or Close is called.
func (s *Server) ListenAndServe(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		l.Close()
		return errors.New("socks: server closed")
	}
	s.listener = l
	s.mu.Unlock()
	for {
		conn, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Addr returns the bound listener address; useful for tests using :0.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err == nil {
		defer conn.SetDeadline(time.Time{})
	}
	s.mu.Lock()
	auth := s.auth
	s.mu.Unlock()
	if err := negotiateAuth(conn, auth); err != nil {
		return
	}
	cmd, addr, port, err := readRequest(conn)
	if err != nil {
		return
	}
	switch cmd {
	case cmdBind, cmdUDPAssociate:
		writeReply(conn, repCmdNotSupport)
		return
	case cmdConnect:
		// allowed
	default:
		writeReply(conn, repCmdNotSupport)
		return
	}
	conn.SetDeadline(time.Time{})

	target := protocol.Target{Network: "tcp", Host: addr, Port: port}
	sess, err := s.pump.Dial(target)
	if err != nil {
		writeReply(conn, repGeneralFailure)
		return
	}
	defer sess.Close()
	if err := writeReply(conn, repSuccess); err != nil {
		return
	}
	bridge(conn, sess)
}

func negotiateAuth(conn net.Conn, auth AuthConfig) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	if hdr[0] != socksVersion {
		return fmt.Errorf("socks: bad version %d", hdr[0])
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	// Pick the strongest method we accept and the client offered.
	want := byte(authNoAuth)
	if auth.Enabled() {
		want = authUserPass
	}
	offered := false
	for _, m := range methods {
		if m == want {
			offered = true
			break
		}
	}
	if !offered {
		conn.Write([]byte{socksVersion, authNoAccept})
		return errors.New("socks: no acceptable auth method")
	}
	if _, err := conn.Write([]byte{socksVersion, want}); err != nil {
		return err
	}
	if want == authUserPass {
		return verifyUserPass(conn, auth)
	}
	return nil
}

// verifyUserPass implements the RFC1929 sub-negotiation: VER ULEN UNAME
// PLEN PASSWD, then VER STATUS where STATUS=0 is success.
func verifyUserPass(conn net.Conn, auth AuthConfig) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	if hdr[0] != 0x01 {
		return fmt.Errorf("socks: bad userpass version %d", hdr[0])
	}
	uname := make([]byte, hdr[1])
	if _, err := io.ReadFull(conn, uname); err != nil {
		return err
	}
	plenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, plenBuf); err != nil {
		return err
	}
	pwd := make([]byte, plenBuf[0])
	if _, err := io.ReadFull(conn, pwd); err != nil {
		return err
	}
	ok := constantTimeMatch(uname, []byte(auth.Username)) &&
		constantTimeMatch(pwd, []byte(auth.Password))
	status := byte(0x00)
	if !ok {
		status = 0x01
	}
	conn.Write([]byte{0x01, status})
	if !ok {
		return errors.New("socks: bad credentials")
	}
	return nil
}

// constantTimeMatch is byte-equality without an early exit on mismatch, so
// timing leakage doesn't help an attacker shorten a password attack.
func constantTimeMatch(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func readRequest(conn net.Conn) (cmd byte, addr string, port uint16, err error) {
	hdr := make([]byte, 4)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return
	}
	if hdr[0] != socksVersion {
		err = fmt.Errorf("socks: bad version %d", hdr[0])
		return
	}
	cmd = hdr[1]
	atyp := hdr[3]
	switch atyp {
	case atypIPv4:
		buf := make([]byte, 4)
		if _, err = io.ReadFull(conn, buf); err != nil {
			return
		}
		addr = net.IP(buf).String()
	case atypIPv6:
		buf := make([]byte, 16)
		if _, err = io.ReadFull(conn, buf); err != nil {
			return
		}
		addr = net.IP(buf).String()
	case atypDomain:
		var l [1]byte
		if _, err = io.ReadFull(conn, l[:]); err != nil {
			return
		}
		buf := make([]byte, l[0])
		if _, err = io.ReadFull(conn, buf); err != nil {
			return
		}
		addr = string(buf)
	default:
		err = fmt.Errorf("socks: unknown atyp %d", atyp)
		return
	}
	pbuf := make([]byte, 2)
	if _, err = io.ReadFull(conn, pbuf); err != nil {
		return
	}
	port = binary.BigEndian.Uint16(pbuf)
	return
}

func writeReply(conn net.Conn, rep byte) error {
	// We always answer with 0.0.0.0:0 for BND fields; clients ignore them.
	resp := []byte{socksVersion, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(resp)
	return err
}

func bridge(conn net.Conn, sess *runtime.ClientSession) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(sess, conn)
		sess.Close()
		cancel()
	}()
	go func() {
		defer wg.Done()
		io.Copy(conn, sess)
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		cancel()
	}()
	// m1: when ctx is canceled (either copier returned), force-close both
	// ends so the *other* io.Copy doesn't block forever waiting on a half
	// that will never produce more bytes.
	<-ctx.Done()
	conn.Close()
	sess.Close()
	wg.Wait()
}

// FormatHostPort joins addr and port for diagnostics.
func FormatHostPort(addr string, port uint16) string {
	return net.JoinHostPort(addr, strconv.Itoa(int(port)))
}
