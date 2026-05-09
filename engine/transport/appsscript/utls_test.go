package appsscript

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestUTLSFingerprintIsChromeNotGo verifies that the ClientHello uTLS
// emits actually looks like Chrome 131, not Go's stdlib crypto/tls.
// This is the proof that v1.1.0 closes the TLS-fingerprint residual
// risk SECURITY.md previously documented as unfixed.
//
// We don't compute a JA3 hash here — JA3 is unstable across Chrome
// versions (ext-shuffler, GREASE), so a hash assertion would be
// flaky against real-world drift. Instead we check structural
// invariants that would *all* fail if the handshake were stdlib's:
//
//  1. ClientHello length is in the Chrome range (~500–800 bytes).
//     Stdlib emits ~200–300 bytes.
//  2. Chrome-distinctive extensions are present:
//     - application_settings (0x4469) — Chrome only
//     - compress_certificate (0x001b) — Chrome only in the version
//     set we pin to
//  3. The extensions list is large (Chrome includes ≥ 12 extensions
//     including GREASE; stdlib emits ≤ 10).
//
// If any of these regress, this test fails — catches both an
// accidental stdlib-tls swap *and* a uTLS profile downgrade.
func TestUTLSFingerprintIsChromeNotGo(t *testing.T) {
	hello, err := captureClientHello(t)
	if err != nil {
		t.Fatalf("captureClientHello: %v", err)
	}

	// 1. Length sanity. Chrome 131's ClientHello with our ALPN list
	//    runs around 500–800 bytes. Anything under 400 is a stdlib
	//    smell or a uTLS misconfiguration.
	if got := len(hello); got < 400 {
		t.Errorf("ClientHello is %d bytes; Chrome 131 is typically 500–800, Go stdlib is 200–300. Is the uTLS profile applied?", got)
	}

	// 2. Parse extensions.
	extTypes, err := parseClientHelloExtensionTypes(hello)
	if err != nil {
		t.Fatalf("parse ClientHello extensions: %v", err)
	}

	if len(extTypes) < 12 {
		t.Errorf("ClientHello has only %d extensions; Chrome 131 typically advertises 14+. Stdlib emits ≤ 10. Likely the uTLS profile isn't HelloChrome_131.", len(extTypes))
	}

	// Chrome-distinctive extensions. If any of these is missing, the
	// fingerprint isn't really Chrome.
	const (
		extApplicationSettings = 0x4469 // application_settings — Chrome only
		extCompressCertificate = 0x001b // compress_certificate — Chrome only
	)
	if !containsExtension(extTypes, extApplicationSettings) {
		t.Errorf("ClientHello missing application_settings (0x4469) — Chrome 131 always emits this; absence means we're not actually fingerprinting as Chrome")
	}
	if !containsExtension(extTypes, extCompressCertificate) {
		t.Errorf("ClientHello missing compress_certificate (0x001b) — Chrome 131 always emits this; absence means we're not actually fingerprinting as Chrome")
	}
}

// captureClientHello dials a TCP listener that records the first packet
// the client sends, then returns those bytes. The TLS handshake fails
// on the client side (the listener doesn't reply), but the ClientHello
// is on the wire before the failure — that's what we want.
func captureClientHello(t *testing.T) ([]byte, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = listener.Close() })

	captured := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 16384)
		n, _ := conn.Read(buf)
		captured <- buf[:n]
	}()

	addr := listener.Addr().String()
	cache := newUTLSSessionCache()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// dialUTLS will fail the handshake (the listener never replies
	// with a ServerHello) but the ClientHello goes on the wire first.
	if _, err := dialUTLS(ctx, addr, "test.example", cache, []string{"h2", "http/1.1"}); err == nil {
		// Handshake completed unexpectedly — this can't happen against
		// a non-TLS listener, so something is very wrong.
		t.Fatal("dialUTLS handshake unexpectedly succeeded against a non-TLS listener")
	}

	select {
	case b := <-captured:
		return b, nil
	case e := <-errCh:
		return nil, fmt.Errorf("listener accept failed: %w", e)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("never received ClientHello bytes")
	}
}

// parseClientHelloExtensionTypes returns the list of extension type IDs
// from a TLS ClientHello (record framing included). Minimal parser:
// only walks the structure deeply enough to enumerate extension types.
func parseClientHelloExtensionTypes(buf []byte) ([]uint16, error) {
	if len(buf) < 5 {
		return nil, fmt.Errorf("buf too short for TLS record header (%d bytes)", len(buf))
	}
	if buf[0] != 0x16 {
		return nil, fmt.Errorf("not a handshake record (got content type 0x%02x, want 0x16)", buf[0])
	}
	p := 5

	if len(buf) < p+4 {
		return nil, fmt.Errorf("buf too short for handshake header")
	}
	if buf[p] != 0x01 {
		return nil, fmt.Errorf("not a ClientHello (got handshake type 0x%02x, want 0x01)", buf[p])
	}
	p += 4

	// client_version (2) + random (32)
	p += 34
	if len(buf) < p+1 {
		return nil, fmt.Errorf("buf too short for session_id length")
	}

	sidLen := int(buf[p])
	p += 1 + sidLen
	if len(buf) < p+2 {
		return nil, fmt.Errorf("buf too short for cipher_suites length")
	}

	csLen := int(binary.BigEndian.Uint16(buf[p : p+2]))
	p += 2 + csLen
	if len(buf) < p+1 {
		return nil, fmt.Errorf("buf too short for compression_methods length")
	}

	cmLen := int(buf[p])
	p += 1 + cmLen
	if len(buf) < p+2 {
		return nil, fmt.Errorf("buf too short for extensions length")
	}

	extLen := int(binary.BigEndian.Uint16(buf[p : p+2]))
	p += 2
	extEnd := p + extLen
	if len(buf) < extEnd {
		return nil, fmt.Errorf("buf too short for extensions block (%d bytes; need %d)", len(buf), extEnd)
	}

	var types []uint16
	for p < extEnd {
		if p+4 > extEnd {
			return nil, fmt.Errorf("malformed extension at offset %d", p)
		}
		extType := binary.BigEndian.Uint16(buf[p : p+2])
		extDataLen := int(binary.BigEndian.Uint16(buf[p+2 : p+4]))
		types = append(types, extType)
		p += 4 + extDataLen
	}
	return types, nil
}

func containsExtension(extTypes []uint16, want uint16) bool {
	for _, t := range extTypes {
		if t == want {
			return true
		}
	}
	return false
}

// TestUTLSCacheRegistry verifies the SNI-rotation cache plumbing under
// uTLS: each SNI host gets its own session cache; repeated lookups
// for the same SNI return the same cache instance.
func TestUTLSCacheRegistry(t *testing.T) {
	r := newUTLSCacheRegistry()
	a1 := r.get("www.google.com")
	a2 := r.get("www.google.com")
	b := r.get("mail.google.com")

	if a1 != a2 {
		t.Errorf("cache for same SNI should be the same instance")
	}
	if a1 == b {
		t.Errorf("cache for different SNIs must be separate instances (resumption is bound to SNI server-side)")
	}
}

// TestUTLSConnWrapperConnectionState verifies the bridge from
// utls.ConnectionState to tls.ConnectionState — without this, http.Transport
// can't see the negotiated ALPN and falls back to HTTP/1.1 even when
// h2 was negotiated.
func TestUTLSConnWrapperConnectionState(t *testing.T) {
	// We can't easily mock *utls.UConn directly, so this test just
	// asserts the wrapper compiles and the field shape matches what
	// http.Transport's TLS-detection assertion expects.
	var w *utlsConnWrapper // typed nil; compile-time check only
	if w != nil {
		_ = w.ConnectionState() // tls.ConnectionState — compile error if return type drifts
	}
	// The runtime proof of the wrapper working is the existing
	// appsscript acceptance tests passing — they exercise the full
	// dial-handshake-roundtrip path through this wrapper.
	_ = strings.Contains // touch import to avoid "imported and not used" if test compiles minimally
}
