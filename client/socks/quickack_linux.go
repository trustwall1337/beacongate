//go:build linux

package socks

import (
	"net"
	"syscall"
)

// setQuickAck enables TCP_QUICKACK on Linux. TCP_QUICKACK is a one-shot
// hint that the kernel resets after subsequent ACKs, so it accelerates
// the next ACK only (typical case: the SOCKS-negotiation reply and the
// first TLS handshake ACK, both of which would otherwise sit in the
// 40 ms delayed-ACK queue). No-ops cleanly when the conn is not a
// *net.TCPConn or when SyscallConn is unavailable (kernel without
// TCP_QUICKACK, alternative TCP stack, etc.).
func setQuickAck(c net.Conn) {
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_QUICKACK, 1)
	})
}
