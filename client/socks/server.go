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
	"strings"
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

	// enableUDPAssociate allows SOCKS5 UDP ASSOCIATE handling.
	// Disabled by default because BeaconGate's core tunnel path is TCP-first.
	// Mobile explicitly enables this for DNS UDP relay compatibility.
	enableUDPAssociate bool
	maxUDPAssoc        int
	activeUDPAssoc     int

	dnsCache    map[string]dnsCacheEntry
	dnsInflight map[string]chan struct{}
	failures    failureWindow
	cooldownTil time.Time
}

type dnsCacheEntry struct {
	payload []byte
	expires time.Time
}

type failureWindow struct {
	start time.Time
	count int
}

func NewServer(pump *runtime.Pump) *Server {
	return &Server{
		pump:        pump,
		maxUDPAssoc: 32,
		dnsCache:    map[string]dnsCacheEntry{},
		dnsInflight: map[string]chan struct{}{},
	}
}

// SetAuth turns on RFC1929 username/password authentication. Empty username
// disables auth (the no-auth default).
func (s *Server) SetAuth(a AuthConfig) {
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
}

// EnableUDPAssociate toggles SOCKS5 UDP ASSOCIATE support.
func (s *Server) EnableUDPAssociate(v bool) {
	s.mu.Lock()
	s.enableUDPAssociate = v
	s.mu.Unlock()
}

// SetUDPAssociateLimit sets the max number of concurrent UDP ASSOCIATE
// sessions. Values < 1 are ignored.
func (s *Server) SetUDPAssociateLimit(n int) {
	if n < 1 {
		return
	}
	s.mu.Lock()
	s.maxUDPAssoc = n
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
	// Wrap every accepted conn with NoDelay + QUICKACK so small TLS
	// handshake records and SOCKS-negotiation replies don't pay the
	// kernel's 40 ms Nagle / delayed-ACK tax on the local browser→tunnel
	// hop. The wrap is idempotent — re-Serving an already-wrapped
	// listener is harmless, the inner conns just get the option set twice.
	l = &noDelayListener{Listener: l}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
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
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err == nil {
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
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
	s.mu.Lock()
	udpAssocEnabled := s.enableUDPAssociate
	s.mu.Unlock()
	switch cmd {
	case cmdBind:
		_ = writeReply(conn, repCmdNotSupport)
		return
	case cmdUDPAssociate:
		if !udpAssocEnabled {
			_ = writeReply(conn, repCmdNotSupport)
			return
		}
		// Mobile path only needs DNS-over-UDP. Reject non-DNS UDP ASSOCIATE
		// early so QUIC/other UDP flows do not consume association slots.
		if reqPort := port; reqPort != 53 && reqPort != 0 {
			_ = writeReply(conn, repNotAllowed)
			return
		}
		s.mu.Lock()
		if s.activeUDPAssoc >= s.maxUDPAssoc {
			s.mu.Unlock()
			_ = writeReply(conn, repGeneralFailure)
			return
		}
		s.activeUDPAssoc++
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			if s.activeUDPAssoc > 0 {
				s.activeUDPAssoc--
			}
			s.mu.Unlock()
		}()
		s.handleUDPAssociate(conn, addr, port)
		return
	case cmdConnect:
		// allowed
	default:
		_ = writeReply(conn, repCmdNotSupport)
		return
	}
	_ = conn.SetDeadline(time.Time{})

	target := protocol.Target{Network: "tcp", Host: addr, Port: port}
	sess, err := s.pump.Dial(target)
	if err != nil {
		s.pump.Log().Warn("socks.dial_failed",
			"target", FormatHostPort(addr, port), "error", err.Error())
		_ = writeReply(conn, repGeneralFailure)
		return
	}
	defer func() { _ = sess.Close() }()
	if err := writeReply(conn, repSuccess); err != nil {
		return
	}
	s.pump.Log().Info("socks.connect",
		"session_id", sess.ID(),
		"target", FormatHostPort(addr, port),
		"local_app", conn.RemoteAddr().String())
	bridge(conn, sess)
}

func negotiateAuth(conn net.Conn, auth AuthConfig) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	// gosec G602 false positives below: io.ReadFull returns nil only
	// when the full 2 bytes were read, so hdr[0] and hdr[1] are
	// guaranteed in-bounds. The linter can't follow ReadFull's
	// post-condition.
	if hdr[0] != socksVersion { //nolint:gosec // G602: hdr length proven by io.ReadFull above
		return fmt.Errorf("socks: bad version %d", hdr[0]) //nolint:gosec // G602: same
	}
	methods := make([]byte, hdr[1]) //nolint:gosec // G602: same reasoning
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
		_, _ = conn.Write([]byte{socksVersion, authNoAccept})
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
	_, _ = conn.Write([]byte{0x01, status})
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
	// gosec G602 false positives below: io.ReadFull returns nil only
	// when the full 4 bytes were read, so hdr[0..3] are guaranteed
	// in-bounds. The linter can't follow ReadFull's post-condition.
	if hdr[0] != socksVersion { //nolint:gosec // G602: hdr length proven by io.ReadFull above
		err = fmt.Errorf("socks: bad version %d", hdr[0]) //nolint:gosec // G602: same
		return
	}
	cmd = hdr[1]   //nolint:gosec // G602: same reasoning
	atyp := hdr[3] //nolint:gosec // G602: same reasoning
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

func writeReplyWithBind(conn net.Conn, rep byte, bindIP net.IP, bindPort uint16) error {
	ip4 := bindIP.To4()
	if ip4 == nil {
		ip4 = net.IPv4zero
	}
	resp := []byte{socksVersion, rep, 0x00, atypIPv4, ip4[0], ip4[1], ip4[2], ip4[3], 0, 0}
	binary.BigEndian.PutUint16(resp[8:], bindPort)
	_, err := conn.Write(resp)
	return err
}

func (s *Server) handleUDPAssociate(conn net.Conn, reqAddr string, reqPort uint16) {
	if s.inCooldown() {
		_ = writeReply(conn, repGeneralFailure)
		return
	}
	relay, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		s.recordFailure()
		_ = writeReply(conn, repGeneralFailure)
		return
	}
	defer func() { _ = relay.Close() }()

	udpAddr, ok := relay.LocalAddr().(*net.UDPAddr)
	if !ok {
		s.recordFailure()
		_ = writeReply(conn, repGeneralFailure)
		return
	}
	// Tell client which UDP bind to use for SOCKS-encapsulated datagrams.
	// 0.0.0.0 would be legal but some clients are stricter; use the bound
	// interface IP from the TCP side when available.
	bindIP := net.IPv4(127, 0, 0, 1)
	if laddr, ok := conn.LocalAddr().(*net.TCPAddr); ok && laddr.IP != nil {
		bindIP = laddr.IP
	}
	if err := writeReplyWithBind(conn, repSuccess, bindIP, uint16(udpAddr.Port)); err != nil {
		s.recordFailure()
		return
	}

	// Association lifetime is bound to the TCP control connection.
	tcpClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(tcpClosed)
	}()

	buf := make([]byte, 64*1024)
	lastActivity := time.Now()
	// DNS responses come back within ~500ms over TCP DNS upstream
	// (or instantly from cache). Any idle time beyond 1.5s means
	// no more packets are coming for this flow — release the slot
	// so a new query can take it. Earlier value (8s) held zombie
	// associations long enough that Chrome's DNS-prefetch burst
	// blew past maxUDPAssoc and triggered a cascade of
	// "general SOCKS server failure" responses.
	const udpAssocIdleTTL = 1500 * time.Millisecond
	for {
		_ = relay.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, srcAddr, err := relay.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-tcpClosed:
					return
				default:
					// tun2socks may leave control TCP sockets around for dead UDP
					// flows; reclaim descriptors proactively on idle associations.
					if time.Since(lastActivity) > udpAssocIdleTTL {
						return
					}
					continue
				}
			}
			return
		}

		srcUDP, ok := srcAddr.(*net.UDPAddr)
		if !ok {
			continue
		}

		host, dstPort, payload, frag, err := decodeUDPAssociatePacket(buf[:n])
		if err != nil || frag != 0 {
			continue
		}
		lastActivity = time.Now()
		// Security/scope guard: only relay DNS UDP. This unblocks mobile
		// name resolution while avoiding broad UDP egress leakage. QUIC
		// (UDP/443) and any other non-DNS UDP is dropped — browsers
		// fall back to TCP HTTP/2 cleanly. This is the v1 mobile policy:
		// the BeaconGate transport is HTTP-shaped and per-packet UDP
		// relay through it doesn't scale to Chrome's QUIC traffic shape.
		if dstPort != 53 {
			continue
		}
		// DNS over TCP + per-name cache/singleflight: avoid UDP-association
		// churn and reduce repeated lookups under page-load bursts.
		answer, err := s.resolveDNSCached(payload)
		if err != nil {
			s.recordFailure()
			continue
		}
		reply, err := encodeUDPAssociatePacket(host, dstPort, answer)
		if err != nil {
			continue
		}
		_, _ = relay.WriteTo(reply, srcUDP)
	}
}

// dnsCacheCap bounds the resolver cache so a long browsing session
// can't grow it without limit. ~1k entries comfortably covers a
// realistic browsing graph (a phone visits hundreds of unique
// hostnames per hour; 1024 leaves room for sub-domain variants and
// CDN shards before evicting). At cap, insert evicts one random
// entry — cheaper than LRU bookkeeping and good enough at this size.
const dnsCacheCap = 1024

// dnsCacheTTL is the fixed-duration TTL we apply to every cached
// response. Doing per-record TTL parsing would be more correct but
// requires walking the answer RRs; for v1 a flat 45 s is the sweet
// spot — long enough to absorb the "20 prefetches per Chrome tab"
// burst, short enough that stale records don't break page loads
// after CDN failover.
const dnsCacheTTL = 45 * time.Second

// resolveDNSCached forwards a UDP DNS query to an upstream TCP DNS
// resolver and caches the response. The cache key deliberately
// EXCLUDES the DNS transaction ID (first two bytes of the query),
// keying instead by (qname, qtype, qclass) so two queries for the
// same record share a cache slot — without that, Chrome's random
// per-query tx IDs would make every query a cache miss.
//
// On cache hit, the stored response is copied and its tx ID
// rewritten to match the current query's tx ID; otherwise the
// requesting app would discard the response as "tx ID mismatch"
// and retry, defeating the cache entirely.
//
// On parse failure (malformed query / multi-question), we bypass
// the cache and pass the raw query straight upstream. Better to be
// slow than wrong on the rare edge case.
func (s *Server) resolveDNSCached(query []byte) ([]byte, error) {
	key, txID, ok := dnsCacheKey(query)
	if !ok {
		// Unparseable / unusual query — go direct, don't cache.
		return s.resolveDNSDirect(query)
	}

	now := time.Now()

	s.mu.Lock()
	if ent, hit := s.dnsCache[key]; hit && ent.expires.After(now) {
		out := append([]byte(nil), ent.payload...)
		s.mu.Unlock()
		spliceTxID(out, txID)
		return out, nil
	}
	if ch, inflight := s.dnsInflight[key]; inflight {
		s.mu.Unlock()
		<-ch
		s.mu.Lock()
		if ent, hit := s.dnsCache[key]; hit && ent.expires.After(time.Now()) {
			out := append([]byte(nil), ent.payload...)
			s.mu.Unlock()
			spliceTxID(out, txID)
			return out, nil
		}
		s.mu.Unlock()
		return nil, errors.New("dns inflight failed")
	}
	ch := make(chan struct{})
	s.dnsInflight[key] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.dnsInflight, key)
		close(ch)
		s.mu.Unlock()
	}()

	ans, err := s.resolveDNSDirect(query)
	if err != nil {
		return nil, err
	}

	// Store with tx ID zeroed so cache hits don't return a stale
	// ID that would mismatch the next requester's expectation.
	stored := append([]byte(nil), ans...)
	if len(stored) >= 2 {
		stored[0] = 0
		stored[1] = 0
	}
	s.mu.Lock()
	if len(s.dnsCache) >= dnsCacheCap {
		// Drop one random entry to make room. Go's map iteration
		// is randomized, so the `range break` produces a uniform-
		// ish eviction without LRU machinery.
		for k := range s.dnsCache {
			delete(s.dnsCache, k)
			break
		}
	}
	s.dnsCache[key] = dnsCacheEntry{
		payload: stored,
		expires: time.Now().Add(dnsCacheTTL),
	}
	s.mu.Unlock()
	// `ans` already has the correct tx ID echoed from upstream,
	// so the cache-miss path returns it directly.
	return ans, nil
}

// resolveDNSDirect hits the upstream TCP DNS resolvers without
// touching the cache. Primary: Cloudflare; fallback: Google. The
// 2 s timeout is the per-server budget; total worst case is ~4 s
// before we surrender and let tun2socks retry on the next packet.
func (s *Server) resolveDNSDirect(query []byte) ([]byte, error) {
	ans, err := resolveDNSOverTCP("1.1.1.1:53", query, 2*time.Second)
	if err != nil {
		ans, err = resolveDNSOverTCP("8.8.8.8:53", query, 2*time.Second)
		if err != nil {
			return nil, err
		}
	}
	return ans, nil
}

// spliceTxID overwrites the first two bytes of buf with the given
// DNS transaction ID. Caller has already copied the buffer so this
// is safe to mutate.
func spliceTxID(buf []byte, txID uint16) {
	if len(buf) < 2 {
		return
	}
	binary.BigEndian.PutUint16(buf[:2], txID)
}

// dnsCacheKey parses a DNS query and returns a cache key composed
// of `qname|qtype|qclass` (lowercased qname, no trailing dot), plus
// the query's transaction ID for splicing back into responses.
//
// Returns ok=false for any query the parser can't handle cleanly
// (truncated header, multi-question, compressed names in the
// question section — RFC 1035 §4.1.4 forbids the latter but it's
// safer to assume nothing). The caller bypasses the cache on
// ok=false and forwards the query straight upstream.
//
// Wire format (RFC 1035 §4.1):
//
//	Header: ID(2) Flags(2) QDCOUNT(2) ANCOUNT(2) NSCOUNT(2) ARCOUNT(2)
//	Question: Name(var) TYPE(2) CLASS(2)
//	Name: <len><label>...<0>     no length byte ≥ 0xC0 in queries
func dnsCacheKey(query []byte) (key string, txID uint16, ok bool) {
	if len(query) < 12 {
		return "", 0, false
	}
	txID = binary.BigEndian.Uint16(query[0:2])
	qdcount := binary.BigEndian.Uint16(query[4:6])
	if qdcount != 1 {
		return "", txID, false
	}

	var name strings.Builder
	pos := 12
	for pos < len(query) {
		l := int(query[pos])
		pos++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 {
			// Compressed pointer in a question section — malformed.
			return "", txID, false
		}
		if pos+l > len(query) {
			return "", txID, false
		}
		if name.Len() > 0 {
			name.WriteByte('.')
		}
		for i := 0; i < l; i++ {
			b := query[pos+i]
			if b >= 'A' && b <= 'Z' {
				b += 32 // lowercase ASCII
			}
			name.WriteByte(b)
		}
		pos += l
	}
	if pos+4 > len(query) {
		return "", txID, false
	}
	qtype := binary.BigEndian.Uint16(query[pos : pos+2])
	qclass := binary.BigEndian.Uint16(query[pos+2 : pos+4])
	return fmt.Sprintf("%s|%d|%d", name.String(), qtype, qclass), txID, true
}

func resolveDNSOverTCP(server string, query []byte, timeout time.Duration) ([]byte, error) {
	c, err := net.DialTimeout("tcp", server, timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(timeout))
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(query)))
	if _, err := c.Write(lb[:]); err != nil {
		return nil, err
	}
	if _, err := c.Write(query); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(c, lb[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(lb[:]))
	if n <= 0 || n > 64*1024 {
		return nil, errors.New("invalid dns tcp length")
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(c, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.failures.start.IsZero() || now.Sub(s.failures.start) > 2*time.Second {
		s.failures.start = now
		s.failures.count = 1
		return
	}
	s.failures.count++
	if s.failures.count >= 20 {
		s.cooldownTil = now.Add(1500 * time.Millisecond)
		s.failures.start = now
		s.failures.count = 0
	}
}

func (s *Server) inCooldown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.cooldownTil)
}

func decodeUDPAssociatePacket(pkt []byte) (host string, port uint16, payload []byte, frag byte, err error) {
	// RFC1928 UDP request header:
	// RSV(2) FRAG(1) ATYP(1) DST.ADDR(var) DST.PORT(2) DATA(var)
	if len(pkt) < 10 {
		err = errors.New("socks: udp packet too short")
		return
	}
	if pkt[0] != 0x00 || pkt[1] != 0x00 {
		err = errors.New("socks: udp rsv non-zero")
		return
	}
	frag = pkt[2]
	atyp := pkt[3]
	pos := 4
	switch atyp {
	case atypIPv4:
		if len(pkt) < pos+4+2 {
			err = errors.New("socks: udp ipv4 packet too short")
			return
		}
		host = net.IP(pkt[pos : pos+4]).String()
		pos += 4
	case atypIPv6:
		if len(pkt) < pos+16+2 {
			err = errors.New("socks: udp ipv6 packet too short")
			return
		}
		host = net.IP(pkt[pos : pos+16]).String()
		pos += 16
	case atypDomain:
		if len(pkt) < pos+1 {
			err = errors.New("socks: udp domain packet too short")
			return
		}
		l := int(pkt[pos])
		pos++
		if len(pkt) < pos+l+2 {
			err = errors.New("socks: udp domain body too short")
			return
		}
		host = string(pkt[pos : pos+l])
		pos += l
	default:
		err = fmt.Errorf("socks: udp atyp %d unsupported", atyp)
		return
	}
	port = binary.BigEndian.Uint16(pkt[pos : pos+2])
	pos += 2
	payload = pkt[pos:]
	return
}

func encodeUDPAssociatePacket(host string, port uint16, payload []byte) ([]byte, error) {
	ip := net.ParseIP(host)
	var hdr []byte
	switch {
	case ip.To4() != nil:
		ip4 := ip.To4()
		hdr = make([]byte, 0, 4+len(ip4)+2+len(payload))
		hdr = append(hdr, 0x00, 0x00, 0x00, atypIPv4)
		hdr = append(hdr, ip4...)
	case ip.To16() != nil:
		ip6 := ip.To16()
		hdr = make([]byte, 0, 4+len(ip6)+2+len(payload))
		hdr = append(hdr, 0x00, 0x00, 0x00, atypIPv6)
		hdr = append(hdr, ip6...)
	default:
		if len(host) > 255 {
			return nil, errors.New("socks: domain too long")
		}
		hdr = make([]byte, 0, 5+len(host)+2+len(payload))
		hdr = append(hdr, 0x00, 0x00, 0x00, atypDomain, byte(len(host)))
		hdr = append(hdr, []byte(host)...)
	}
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, port)
	hdr = append(hdr, pb...)
	hdr = append(hdr, payload...)
	return hdr, nil
}

func bridge(conn net.Conn, sess *runtime.ClientSession) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(sess, conn)
		_ = sess.Close()
		cancel()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, sess)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		cancel()
	}()
	// m1: when ctx is canceled (either copier returned), force-close both
	// ends so the *other* io.Copy doesn't block forever waiting on a half
	// that will never produce more bytes.
	<-ctx.Done()
	_ = conn.Close()
	_ = sess.Close()
	wg.Wait()
}

// FormatHostPort joins addr and port for diagnostics.
func FormatHostPort(addr string, port uint16) string {
	return net.JoinHostPort(addr, strconv.Itoa(int(port)))
}
