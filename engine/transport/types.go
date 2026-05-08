// Package transport defines the abstraction the BeaconGate client uses to
// hand opaque encrypted batches off to a remote server. Concrete transports
// (e.g. the Google-facing HTTP transport) live in subpackages and must not
// leak provider-specific details into engine code.
package transport

import (
	"context"
	"time"
)

// ClientTransport carries an opaque batch from the client runtime to the
// server runtime and returns whatever bytes the server hands back. The
// transport never inspects the batch contents.
type ClientTransport interface {
	// Roundtrip sends batch and waits for the server's reply. Implementations
	// MUST honour ctx cancellation.
	Roundtrip(ctx context.Context, batch []byte) ([]byte, error)
	// Diagnose performs a transport-level health check. The returned report
	// is plain enough to surface in a UI or operator log.
	Diagnose(ctx context.Context) (Diagnostics, error)
	// Close releases any underlying resources (connections, pools, etc.).
	Close() error
}

type Diagnostics struct {
	Healthy bool
	Latency time.Duration
	Detail  string
}
