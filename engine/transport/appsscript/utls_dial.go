package appsscript

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

// pinnedProfile is the uTLS ClientHello fingerprint BeaconGate presents
// when speaking to Google Apps Script. We pin to a specific Chrome
// version (not utls.HelloChrome_Auto) so the fingerprint is
// deterministic and only changes when we explicitly bump it.
//
// Bump cadence: one Chrome major per BeaconGate minor release. See
// docs/uTLS-fingerprint-cadence.md for the rationale and bump procedure.
//
// As of v1.1.0 we present Chrome 131 (stable Nov–Dec 2025). The
// real-world Chrome population still has plenty of 131 installs due to
// auto-update lag; bleeding-edge Chrome (133+) blends less well.
var pinnedProfile = utls.HelloChrome_131

// uTLSSessionCache is a thin shim adapting utls.ClientSessionCache to
// its own equivalent so callers can hold a stdlib-shaped reference.
// uTLS forks crypto/tls and defines its own ClientSessionCache; using
// utls.NewLRUClientSessionCache directly avoids any compatibility risk.
type uTLSSessionCache = utls.ClientSessionCache

// newUTLSSessionCache returns a new in-memory session ticket cache for
// uTLS resumption. Cache size of 8 mirrors the stdlib code path.
func newUTLSSessionCache() uTLSSessionCache {
	return utls.NewLRUClientSessionCache(8)
}

// buildUTLSConfig assembles the uTLS Config the dialer uses. Extracted
// so acceptance tests can verify the disguise invariants (SNI, MinVersion,
// ALPN) without driving a full handshake.
//
// **Invariants enforced here are part of the censorship-evasion property:**
//   - ServerName == sniHost (TLS ClientHello SNI must match the
//     configured rotation entry).
//   - MinVersion == TLS 1.3 (TLS 1.2 ClientHello looks different on
//     the wire and weakens "looks like modern Google" disguise).
//   - NextProtos is the ALPN list — pinned so resumed sessions match
//     the prewarm dial's negotiated protocol.
func buildUTLSConfig(sniHost string, sessionCache uTLSSessionCache, alpnProtocols []string) *utls.Config {
	return &utls.Config{
		ServerName:         sniHost,
		MinVersion:         tls.VersionTLS13,
		ClientSessionCache: sessionCache,
		NextProtos:         alpnProtocols,
	}
}

// dialUTLS opens a TCP connection to googleIP (or sniHost:443 if
// googleIP is empty), wraps it with uTLS using the pinned Chrome
// fingerprint, completes the handshake, and returns a *utlsConnWrapper.
//
// The return type is the wrapper (not bare net.Conn) so callers can
// reach `.UConn.ConnectionState().NegotiatedProtocol` after the
// handshake — http2.Transport's DialTLSContext callback does this to
// verify h2 ALPN was selected before handing the conn off.
//
// sessionCache is required and must be persistent across calls so
// resumption works on subsequent connections to the same SNI host.
//
// alpnProtocols controls the ALPN list advertised in the ClientHello.
// For HTTP traffic, pass {"h2", "http/1.1"}.
func dialUTLS(ctx context.Context, googleIP, sniHost string, sessionCache uTLSSessionCache, alpnProtocols []string) (*utlsConnWrapper, error) {
	addr := googleIP
	if addr == "" {
		addr = net.JoinHostPort(sniHost, "443")
	}
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("appsscript: utls dial %s: %w", addr, err)
	}

	uConn := utls.UClient(rawConn, buildUTLSConfig(sniHost, sessionCache, alpnProtocols), pinnedProfile)
	if err := uConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("appsscript: utls handshake to %s (sni=%s): %w", addr, sniHost, err)
	}
	return &utlsConnWrapper{UConn: uConn}, nil
}

// utlsConnWrapper wraps *utls.UConn and exposes a stdlib-shaped
// `ConnectionState() tls.ConnectionState` for any caller that wants
// it. http2.Transport doesn't actually need this method (we drive h2
// dispatch from the dialer's ALPN check rather than via type-assertion
// in the transport), but we keep the wrapper because:
//
//  1. It documents the close mapping between uTLS and stdlib state for
//     anyone reading this code.
//  2. Tests can call `.UConn.ConnectionState().NegotiatedProtocol`
//     without re-implementing the field-by-field copy.
//
// **Historical note:** in earlier versions of Go (≤ 1.20) net/http
// detected HTTP/2 via an `interface { ConnectionState() tls.ConnectionState }`
// type assertion, and this wrapper made our conn satisfy it. Go 1.21+
// replaced that with a concrete `*tls.Conn` assertion (transport.go:1763)
// which our wrapper cannot satisfy without inheriting from `*tls.Conn`
// (which uTLS forks from). The fix in v1.1.0 is to bypass net/http's
// transport entirely and use http2.Transport directly — see
// fronting.go's `newFrontedClient`.
type utlsConnWrapper struct {
	*utls.UConn
}

// ConnectionState translates uTLS's connection state to the stdlib type
// http.Transport expects. Field-by-field copy because uTLS forked
// crypto/tls; the field set is identical in v1.8.2.
func (w *utlsConnWrapper) ConnectionState() tls.ConnectionState {
	uState := w.UConn.ConnectionState()
	return tls.ConnectionState{
		Version:                     uState.Version,
		HandshakeComplete:           uState.HandshakeComplete,
		DidResume:                   uState.DidResume,
		CipherSuite:                 uState.CipherSuite,
		NegotiatedProtocol:          uState.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  true, // uTLS doesn't expose this; safe default since we only advertise our own list
		ServerName:                  uState.ServerName,
		PeerCertificates:            uState.PeerCertificates,
		VerifiedChains:              uState.VerifiedChains,
		SignedCertificateTimestamps: uState.SignedCertificateTimestamps,
		OCSPResponse:                uState.OCSPResponse,
		TLSUnique:                   uState.TLSUnique,
	}
}

// utlsCacheRegistry holds one uTLS session cache per SNI host. Used to
// keep cache lifetime tied to the http.Client lifetime: stdlib
// tls.ClientSessionCache and utls.ClientSessionCache are different
// types and can't share storage, so the appsscript transport keeps
// uTLS-specific caches in a small registry parallel to the existing
// stdlib `caches` field on httpClientPool.
type utlsCacheRegistry struct {
	mu     sync.Mutex
	byHost map[string]uTLSSessionCache
}

func newUTLSCacheRegistry() *utlsCacheRegistry {
	return &utlsCacheRegistry{byHost: make(map[string]uTLSSessionCache)}
}

// get returns (creating if absent) the cache for sniHost.
func (r *utlsCacheRegistry) get(sniHost string) uTLSSessionCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.byHost[sniHost]; ok {
		return c
	}
	c := newUTLSSessionCache()
	r.byHost[sniHost] = c
	return c
}
