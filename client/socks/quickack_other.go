//go:build !linux

package socks

import "net"

// setQuickAck is a no-op on non-Linux platforms. TCP_QUICKACK is a
// Linux-only socket option; on macOS, Windows, and BSDs the delayed-ACK
// behaviour is either tunable kernel-wide or simply not exposed per
// socket. SetNoDelay still applies cross-platform.
func setQuickAck(_ net.Conn) {}
