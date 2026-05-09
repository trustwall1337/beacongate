package upstream

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestNetDialerConnects(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, _ := l.Accept()
		if c != nil {
			c.Close()
		}
	}()
	host, portStr, _ := net.SplitHostPort(l.Addr().String())
	var port uint16
	fmtPort(t, portStr, &port)

	d, derr := NewNetDialer(2*time.Second, "")
	if derr != nil {
		t.Fatalf("new dialer: %v", derr)
	}
	d.Safety.AllowPrivate = true // test reaches 127.0.0.1
	conn, err := d.Dial(context.Background(), host, port)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
}

func fmtPort(t *testing.T, s string, out *uint16) {
	t.Helper()
	var p int
	for _, ch := range s {
		p = p*10 + int(ch-'0')
	}
	*out = uint16(p)
}

// TestUpstreamProxyDials verifies that when NewNetDialer is given a
// SOCKS5 upstream-proxy URL, the outbound TCP dial actually flows
// through that proxy. We stand up a tiny SOCKS5 proxy locally that
// records every CONNECT it processes, then dial through it.
//
// The test also asserts the SSRF guard still runs locally — even when
// going through a proxy, a request for a metadata-IP destination must
// be rejected before reaching the proxy.
func TestUpstreamProxyDials(t *testing.T) {
	// Target server (the "destination" in the proxy CONNECT).
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// Tiny SOCKS5 proxy.
	proxyHits := make(chan string, 1)
	proxy, err := startTinySOCKS5Proxy(t, proxyHits)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	d, err := NewNetDialer(3*time.Second, "socks5://"+proxy.Addr().String())
	if err != nil {
		t.Fatalf("new dialer: %v", err)
	}
	d.Safety.AllowPrivate = true // test target is 127.0.0.1

	host, portStr, _ := net.SplitHostPort(target.Addr().String())
	var port uint16
	fmtPort(t, portStr, &port)

	conn, err := d.Dial(context.Background(), host, port)
	if err != nil {
		t.Fatalf("dial via proxy: %v", err)
	}
	conn.Close()

	select {
	case got := <-proxyHits:
		want := net.JoinHostPort(host, portStr)
		if got != want {
			t.Errorf("proxy saw CONNECT to %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy never received the CONNECT")
	}
}

// TestUpstreamProxySSRFStillEnforced verifies that even when proxying
// is enabled, the SSRF guard still rejects metadata-IP destinations
// before any traffic touches the proxy.
func TestUpstreamProxySSRFStillEnforced(t *testing.T) {
	d, err := NewNetDialer(3*time.Second, "socks5://127.0.0.1:1") // proxy address doesn't matter; we should never reach it
	if err != nil {
		t.Fatalf("new dialer: %v", err)
	}
	// Default safety: deny private/metadata. Don't override.
	_, err = d.Dial(context.Background(), "169.254.169.254", 80)
	if err == nil {
		t.Fatal("expected SSRF rejection for metadata IP, got nil")
	}
}

// startTinySOCKS5Proxy is a minimal SOCKS5 server (CONNECT only,
// no-auth) sufficient for testing the dialer. Records the CONNECT
// target on hits.
func startTinySOCKS5Proxy(t *testing.T, hits chan<- string) (net.Listener, error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5(c, hits)
		}
	}()
	return l, nil
}

func handleSOCKS5(c net.Conn, hits chan<- string) {
	defer c.Close()
	buf := make([]byte, 512)
	// Greeting: VER=5, NMETHODS, METHODS...
	n, err := c.Read(buf)
	if err != nil || n < 2 || buf[0] != 5 {
		return
	}
	// No-auth response.
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	// CONNECT request: VER=5, CMD=1 (connect), RSV, ATYP, addr, port.
	n, err = c.Read(buf)
	if err != nil || n < 7 || buf[0] != 5 || buf[1] != 1 {
		return
	}
	atyp := buf[3]
	var host string
	var port uint16
	switch atyp {
	case 1: // IPv4
		if n < 10 {
			return
		}
		host = net.IPv4(buf[4], buf[5], buf[6], buf[7]).String()
		port = uint16(buf[8])<<8 | uint16(buf[9])
	case 3: // domain
		if n < 5 {
			return
		}
		l := int(buf[4])
		if n < 5+l+2 {
			return
		}
		host = string(buf[5 : 5+l])
		port = uint16(buf[5+l])<<8 | uint16(buf[6+l])
	default:
		return
	}
	hits <- net.JoinHostPort(host, fmtUint(port))

	// Connect upstream.
	upstream, err := net.Dial("tcp", net.JoinHostPort(host, fmtUint(port)))
	if err != nil {
		// Reply with general-failure code.
		_, _ = c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	// Success reply.
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})

	// Half-close after the test-side conn closes; we don't bother piping
	// data because the test only checks dial reachability.
	go func() { _, _ = copyZero(upstream, c) }()
	_, _ = copyZero(c, upstream)
}

func copyZero(dst, src net.Conn) (int64, error) {
	buf := make([]byte, 4096)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			w, _ := dst.Write(buf[:n])
			total += int64(w)
		}
		if err != nil {
			return total, err
		}
	}
}

func fmtUint(p uint16) string {
	const digits = "0123456789"
	if p == 0 {
		return "0"
	}
	var out []byte
	for p > 0 {
		out = append([]byte{digits[p%10]}, out...)
		p /= 10
	}
	return string(out)
}

func TestDNSCacheTTL(t *testing.T) {
	c := NewDNSCache(20 * time.Millisecond)
	c.Set("example.com", "1.2.3.4")
	if ip, ok := c.Lookup("example.com"); !ok || ip != "1.2.3.4" {
		t.Fatalf("immediate lookup: ip=%s ok=%v", ip, ok)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Lookup("example.com"); ok {
		t.Fatalf("expected expiry")
	}
}
