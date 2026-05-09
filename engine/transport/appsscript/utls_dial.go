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
// fingerprint, completes the handshake, and returns a net.Conn that
// http.Transport can detect as a TLS conn (so HTTP/2 ALPN routing
// still works).
//
// sessionCache is required and must be persistent across calls so
// resumption works on subsequent connections to the same SNI host.
//
// alpnProtocols controls the ALPN list advertised in the ClientHello.
// For HTTP traffic, pass {"h2", "http/1.1"}.
func dialUTLS(ctx context.Context, googleIP, sniHost string, sessionCache uTLSSessionCache, alpnProtocols []string) (net.Conn, error) {
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

// utlsConnWrapper bridges *utls.UConn to net/http's TLS detection.
//
// Go's http.Transport detects "this conn is a TLS conn that negotiated
// ALPN h2" by type-asserting the connection to:
//
//	interface { ConnectionState() tls.ConnectionState }
//
// where tls is *crypto/tls* (stdlib). uTLS's UConn has a
// ConnectionState method too, but it returns *utls.ConnectionState*
// (uTLS's own type), which does NOT satisfy that implicit interface.
//
// Without this wrapper, http.Transport sees the conn as plain TCP
// (no TLS state), refuses to use HTTP/2, and falls back to HTTP/1.1.
// With this wrapper, http.Transport sees a real tls.ConnectionState,
// reads NegotiatedProtocol="h2", and routes through http2.Transport.
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
