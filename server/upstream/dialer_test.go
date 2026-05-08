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

	d := NewNetDialer(2 * time.Second)
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
