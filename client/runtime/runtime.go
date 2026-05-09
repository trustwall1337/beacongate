// Package runtime is the BeaconGate client-side engine. It wires the
// protocol, crypto envelope, and transport layers together and exposes a
// small, testable API to higher-level adapters (e.g. the SOCKS server).
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport"
)

// State enumerates the coarse lifecycle states surfaced through the
// control API. The runtime moves Starting → Connected on the first
// successful probe; transient failures move Connected → Degraded;
// Close moves any state to Stopped.
type State int32

const (
	StateStarting State = iota
	StateConnected
	StateDegraded
	StateError
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateConnected:
		return "connected"
	case StateDegraded:
		return "degraded"
	case StateError:
		return "error"
	case StateStopped:
		return "stopped"
	}
	return "unknown"
}

var ErrClosed = errors.New("client runtime: closed")

// discardLogger is the silent default; SetLogger replaces it with a real one.
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// Runtime owns the per-process client state. It is safe for concurrent use.
type Runtime struct {
	cfg       *config.ClientConfig
	sealer    *crypto.Sealer
	transport transport.ClientTransport

	closed  atomic.Bool
	counter atomic.Uint64

	logger atomic.Pointer[slog.Logger]

	state         atomic.Int32 // State value; reads and writes go through helper methods
	activeProfile atomic.Pointer[string]

	statusMu            sync.Mutex
	lastError           string
	lastSuccessfulProbe time.Time

	eventMu  sync.Mutex
	eventRec EventRecorder
}

// New builds a Runtime from a validated client config and a constructed
// transport. The transport's lifetime is owned by the Runtime once passed in.
func New(cfg *config.ClientConfig, t transport.ClientTransport) (*Runtime, error) {
	if cfg == nil {
		return nil, errors.New("client runtime: cfg is required")
	}
	if t == nil {
		return nil, errors.New("client runtime: transport is required")
	}
	keyBytes, err := cfg.ServerKeyBytes()
	if err != nil {
		return nil, err
	}
	sealer, err := crypto.NewSealer(keyBytes)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{cfg: cfg, sealer: sealer, transport: t}
	rt.logger.Store(discardLogger)
	rt.state.Store(int32(StateStarting))
	return rt, nil
}

// State returns the current lifecycle state.
func (r *Runtime) State() State { return State(r.state.Load()) }

// SetState atomically updates the lifecycle state. Callers (the pump,
// startup diagnostics, the SOCKS server) move the state from observed
// signals — this is not driven by the control API.
func (r *Runtime) SetState(s State) { r.state.Store(int32(s)) }

// SetActiveProfile records the human-facing profile name (Phase 1: the
// config filename). Surfaced through the control API so an operator
// can confirm which config the running client is using.
func (r *Runtime) SetActiveProfile(name string) {
	if name == "" {
		return
	}
	cp := name
	r.activeProfile.Store(&cp)
}

// ActiveProfile returns the active profile name set by SetActiveProfile,
// or the empty string if none was set.
func (r *Runtime) ActiveProfile() string {
	if p := r.activeProfile.Load(); p != nil {
		return *p
	}
	return ""
}

// RecordError stores a human-readable error string for the control
// API's status surface. Pass empty string to clear.
func (r *Runtime) RecordError(msg string) {
	r.statusMu.Lock()
	r.lastError = msg
	r.statusMu.Unlock()
}

// RecordSuccessfulProbe stamps "now" as the last successful probe time
// and clears any prior recorded error.
func (r *Runtime) RecordSuccessfulProbe() {
	now := time.Now()
	r.statusMu.Lock()
	r.lastSuccessfulProbe = now
	r.lastError = ""
	r.statusMu.Unlock()
}

// StatusSnapshot returns the (lastError, lastSuccessfulProbe) tuple
// under a single lock acquisition. Used by the control API.
func (r *Runtime) StatusSnapshot() (lastError string, lastSuccessfulProbe time.Time) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.lastError, r.lastSuccessfulProbe
}

// Config returns the loaded client config. Read-only — do not mutate.
func (r *Runtime) Config() *config.ClientConfig { return r.cfg }

// Transport returns the underlying transport.ClientTransport.
// Control-API consumers can type-assert to a concrete transport type
// to surface transport-specific stats (e.g. *appsscript.Client.Stats()
// for per-account quota numbers).
func (r *Runtime) Transport() transport.ClientTransport { return r.transport }

// EventRecorder is the optional callback the control API installs
// so runtime-side state changes (degraded, reconnecting, reconnected,
// probe_failed) flow into the events ring buffer surfaced through
// GET /api/events. Callers without a control API leave the recorder
// nil; RecordEvent is then a no-op.
type EventRecorder func(level, component, eventType, summary, detail string)

// SetEventRecorder installs the callback. Pass nil to clear.
func (r *Runtime) SetEventRecorder(rec EventRecorder) {
	r.eventMu.Lock()
	r.eventRec = rec
	r.eventMu.Unlock()
}

// RecordEvent dispatches to the installed EventRecorder, or no-ops
// if none is set. Safe to call from any goroutine.
func (r *Runtime) RecordEvent(level, component, eventType, summary, detail string) {
	r.eventMu.Lock()
	rec := r.eventRec
	r.eventMu.Unlock()
	if rec != nil {
		rec(level, component, eventType, summary, detail)
	}
}

// SetLogger installs a structured logger. Pass nil to silence.
func (r *Runtime) SetLogger(l *slog.Logger) {
	if l == nil {
		l = discardLogger
	}
	r.logger.Store(l)
}

// Log returns the current logger. Always non-nil.
func (r *Runtime) Log() *slog.Logger { return r.logger.Load() }

// Exchange wraps a list of outbound messages in an envelope, encrypts and
// sends them, then decrypts and decodes the server's reply.
func (r *Runtime) Exchange(ctx context.Context, msgs []protocol.Message) ([]protocol.Message, error) {
	if r.closed.Load() {
		return nil, ErrClosed
	}
	if len(msgs) == 0 {
		return nil, errors.New("client runtime: refusing to send empty batch")
	}
	env := protocol.Envelope{
		Version:     protocol.Version{Major: protocol.ProtocolVersionMajor, Minor: protocol.ProtocolVersionMinor},
		ClientID:    r.cfg.ClientID,
		Transport:   r.cfg.Transport.Type,
		Compression: protocol.CompressionNone,
		Messages:    msgs,
	}
	plaintext, err := protocol.EncodeEnvelope(env)
	if err != nil {
		return nil, fmt.Errorf("client runtime: encode envelope: %w", err)
	}
	wire, err := r.sealer.Seal(r.cfg.ClientID, plaintext)
	if err != nil {
		return nil, fmt.Errorf("client runtime: seal: %w", err)
	}
	respWire, err := r.transport.Roundtrip(ctx, wire)
	if err != nil {
		return nil, err
	}
	// Open returns a SealedBatch with the server's client_id (typically
	// server_id) plus the inner timestamp/replay-id. The client doesn't
	// need to dedup server-originated batches today (the Pump
	// serializes one in-flight request at a time), so we ignore those
	// fields here. If a future change adds server-side replay defense
	// for the response leg, this is the spot to wire it.
	batch, err := r.sealer.Open(respWire)
	if err != nil {
		return nil, fmt.Errorf("client runtime: open response: %w", err)
	}
	respEnv, err := protocol.DecodeEnvelope(batch.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("client runtime: decode response: %w", err)
	}
	return respEnv.Messages, nil
}

// Diagnose proxies to the underlying transport. It is a no-op when the
// runtime has been closed.
func (r *Runtime) Diagnose(ctx context.Context) (transport.Diagnostics, error) {
	if r.closed.Load() {
		return transport.Diagnostics{}, ErrClosed
	}
	return r.transport.Diagnose(ctx)
}

// Probe sends a PROBE envelope to negotiate version compatibility.
func (r *Runtime) Probe(ctx context.Context) (*protocol.Message, error) {
	probeID, err := r.NewID("probe")
	if err != nil {
		return nil, err
	}
	resp, err := r.Exchange(ctx, []protocol.Message{{
		Type:              protocol.MessageTypeProbe,
		ProbeID:           probeID,
		SupportedVersions: []protocol.Version{{Major: protocol.ProtocolVersionMajor, Minor: protocol.ProtocolVersionMinor}},
	}})
	if err != nil {
		return nil, err
	}
	for i := range resp {
		if resp[i].Type == protocol.MessageTypeProbe && resp[i].ProbeID == probeID {
			return &resp[i], nil
		}
	}
	return nil, errors.New("client runtime: no matching probe response")
}

// NewID returns a fresh identifier with the given prefix. The combination of
// a 64-bit counter and 8 random bytes is safe to use for both probes and
// session ids without coordinating with peers.
func (r *Runtime) NewID(prefix string) (string, error) {
	n := r.counter.Add(1)
	rnd := make([]byte, 8)
	if _, err := rand.Read(rnd); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d-%s", prefix, n, hex.EncodeToString(rnd)), nil
}

// ClientID returns the configured client identifier.
func (r *Runtime) ClientID() string { return r.cfg.ClientID }

// Close releases the underlying transport. Subsequent operations return
// ErrClosed.
func (r *Runtime) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	r.SetState(StateStopped)
	return r.transport.Close()
}
