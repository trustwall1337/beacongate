package socks

import "net"

// noDelayListener wraps a net.Listener so each accepted *net.TCPConn has
// SetNoDelay(true) and (on Linux) TCP_QUICKACK applied. This eliminates
// the kernel's 40 ms Nagle delay on small SOCKS write payloads from the
// local application and the 40 ms delayed-ACK on small reply records —
// together they cover both directions of every interactive request/reply
// pair (DNS-over-HTTPS, REST GETs, TLS handshake records).
//
// Applied at Serve so every code path that hands a listener in benefits
// (ListenAndServe and operator-supplied listeners both flow through the
// same wrap).
type noDelayListener struct {
	net.Listener
}

func (l *noDelayListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	setQuickAck(c)
	return c, nil
}
