// Package transporttest provides in-process fakes for the transport
// abstraction. It is the equivalent of net/http/httptest for BeaconGate:
// production code never depends on it, but every test that needs to drive
// a transport without HTTP can use Fake.
package transporttest

import (
	"context"

	"github.com/trustwall1337/beacongate/engine/transport"
)

// Fake is a deterministic transport whose behaviour is supplied by the
// caller. It satisfies transport.ClientTransport.
type Fake struct {
	Handler func(ctx context.Context, batch []byte) ([]byte, error)
	closed  bool
}

func (f *Fake) Roundtrip(ctx context.Context, batch []byte) ([]byte, error) {
	if f.closed {
		return nil, transport.ErrClosed
	}
	if f.Handler == nil {
		return nil, transport.ErrInvalidResponse
	}
	return f.Handler(ctx, batch)
}

func (f *Fake) Diagnose(_ context.Context) (transport.Diagnostics, error) {
	if f.closed {
		return transport.Diagnostics{}, transport.ErrClosed
	}
	return transport.Diagnostics{Healthy: true, Detail: "fake"}, nil
}

func (f *Fake) Close() error {
	f.closed = true
	return nil
}

var _ transport.ClientTransport = (*Fake)(nil)
