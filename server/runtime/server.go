// Package runtime is the BeaconGate server-side engine. It accepts encrypted
// batches from clients, processes the protocol envelope, drives upstream
// connections, and returns batches of responses on the same HTTP request.
package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/replay"
	"github.com/trustwall1337/beacongate/server/internal/limit"
	"github.com/trustwall1337/beacongate/server/upstream"
)

// discardLogger is the default logger used when the operator hasn't wired
// one in. Logs go nowhere; tests stay quiet.
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

const (
	defaultDrainWindow          = 25 * time.Millisecond
	// defaultLongPollWindow is the server-side hold time for an idle
	// long-poll. Matches client/runtime.defaultIdleHold so the two ends
	// agree on cadence: 8s gives a fast inbound-channel cycle (low pickup
	// latency for response data) at the cost of more Apps Script
	// invocations under idle. See docstring on defaultIdleHold for the
	// quota trade-off rationale.
	defaultLongPollWindow       = 8 * time.Second
	defaultMaxChunk             = 16 * 1024
	defaultMaxSessionsPerClient = 100
	defaultIdleSessionTimeout   = 10 * time.Minute

	// Per-IP rate cap on the /tunnel endpoint (plan D1). Conservative
	// default sized to comfortably cover a busy interactive client
	// (long-poll cadence ~25s + occasional bursts) while bounding the
	// cost a single peer with the shared key can impose.
	defaultTunnelRatePerSec = 50.0
	defaultTunnelBurst      = 100
)

// PolicyDecision is what the policy engine returns for a target. The server
// runtime treats Allowed=false as POLICY_DENIED.
type PolicyDecision struct {
	Allowed bool
	Reason  string
}

// PolicyEvaluator is the policy hook the server runtime calls before
// dialing. The full policy package satisfies this interface; tests can plug
// in a stub.
type PolicyEvaluator interface {
	Evaluate(target protocol.Target) PolicyDecision
}

// AllowAll is a default policy used when none has been wired up.
type AllowAll struct{}

func (AllowAll) Evaluate(protocol.Target) PolicyDecision {
	return PolicyDecision{Allowed: true}
}

// Server holds the per-process state shared across tunnel requests.
type Server struct {
	serverID string
	sealer   *crypto.Sealer
	dialer   upstream.Dialer
	policy   PolicyEvaluator

	// replayStore is the v1.1 replay dedup cache (plan B4+B5).
	// Per-client sharded; lock-bounded; off the request critical
	// path except for the lookup/insert which are O(1)-ish.
	replayStore *replay.Store

	// tunnelLimiter caps requests-per-second per remote IP on
	// /tunnel (plan D1). Defense in depth: even a peer with the
	// shared key cannot saturate a single server.
	tunnelLimiter *limit.TokenBucket

	drainWindow          time.Duration
	longPollWindow       time.Duration
	maxChunk             int
	maxSessionsPerClient int
	idleSessionTimeout   time.Duration

	mu       sync.Mutex
	byClient map[string]map[string]*serverSession // clientID -> sessionID -> session

	signalsMu sync.Mutex
	signals   map[string]chan struct{} // per-clientID buffered (cap 1) wakeup
	stopCh    chan struct{}
	stopOnce  sync.Once
	reaperWG  sync.WaitGroup

	// logger is read-mostly via atomic.Pointer so the hot path doesn't take
	// s.mu just to access it. Default is a no-op; SetLogger swaps in a real
	// one before traffic starts.
	logger atomic.Pointer[slog.Logger]
}

func New(serverID string, sealer *crypto.Sealer, dialer upstream.Dialer, policy PolicyEvaluator) *Server {
	if policy == nil {
		policy = AllowAll{}
	}
	srv := &Server{
		serverID:             serverID,
		sealer:               sealer,
		dialer:               dialer,
		policy:               policy,
		replayStore:          replay.New(replay.Defaults()),
		tunnelLimiter:        limit.New(defaultTunnelRatePerSec, defaultTunnelBurst),
		drainWindow:          defaultDrainWindow,
		longPollWindow:       defaultLongPollWindow,
		maxChunk:             defaultMaxChunk,
		maxSessionsPerClient: defaultMaxSessionsPerClient,
		idleSessionTimeout:   defaultIdleSessionTimeout,
		byClient:             map[string]map[string]*serverSession{},
		signals:              map[string]chan struct{}{},
		stopCh:               make(chan struct{}),
	}
	srv.logger.Store(discardLogger)
	srv.startReaper()
	return srv
}

// SetLogger installs a structured logger. Pass slog.Default() for stdlib
// behaviour, or a custom slog.Logger for JSON / file output. Passing nil
// silences the runtime.
func (s *Server) SetLogger(l *slog.Logger) {
	if l == nil {
		l = discardLogger
	}
	s.logger.Store(l)
}

// log returns the current logger, never nil.
func (s *Server) log() *slog.Logger { return s.logger.Load() }

// SetLongPollWindow overrides the default 25s server-hold time. Useful for
// tests that want a tighter bound on test runtime; production should leave
// the default.
func (s *Server) SetLongPollWindow(d time.Duration) {
	s.mu.Lock()
	s.longPollWindow = d
	s.mu.Unlock()
}

// SetMaxSessionsPerClient overrides the default per-client cap.
func (s *Server) SetMaxSessionsPerClient(n int) {
	s.mu.Lock()
	if n > 0 {
		s.maxSessionsPerClient = n
	}
	s.mu.Unlock()
}

// SetIdleSessionTimeout overrides the default reap interval. Set to 0 to
// disable idle-session reaping (not recommended in production).
func (s *Server) SetIdleSessionTimeout(d time.Duration) {
	s.mu.Lock()
	s.idleSessionTimeout = d
	s.mu.Unlock()
}

// SetPolicy swaps in a new policy evaluator atomically.
func (s *Server) SetPolicy(p PolicyEvaluator) {
	if p == nil {
		p = AllowAll{}
	}
	s.mu.Lock()
	s.policy = p
	s.mu.Unlock()
}

// SessionCount returns the number of live upstream sessions across clients.
func (s *Server) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.byClient {
		n += len(c)
	}
	return n
}

// Close terminates all live sessions, releases any in-flight long-poll
// requests, and stops the idle-session reaper.
func (s *Server) Close() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.reaperWG.Wait()
	s.mu.Lock()
	// Pre-size live to the total session count so Close doesn't grow
	// the slice when the server has many in-flight sessions at
	// shutdown.
	totalSessions := 0
	for _, byID := range s.byClient {
		totalSessions += len(byID)
	}
	live := make([]*serverSession, 0, totalSessions)
	for _, byID := range s.byClient {
		for _, sess := range byID {
			live = append(live, sess)
		}
	}
	s.byClient = map[string]map[string]*serverSession{}
	s.mu.Unlock()
	for _, ss := range live {
		ss.terminate(errors.New("server closing"))
	}
	// Drop any leftover wakeup channels for clients that have all gone away.
	s.signalsMu.Lock()
	s.signals = map[string]chan struct{}{}
	s.signalsMu.Unlock()
	return nil
}

// signal returns the per-client wakeup channel, allocating it on first use.
// Used by the tunnel handler to long-poll for upstream data without busy
// waiting.
func (s *Server) signal(clientID string) chan struct{} {
	s.signalsMu.Lock()
	defer s.signalsMu.Unlock()
	ch := s.signals[clientID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.signals[clientID] = ch
	}
	return ch
}

// notify wakes any long-poll request that is currently waiting on data for
// clientID. The buffered cap-1 channel collapses bursts: if a wakeup is
// already pending, additional notifies are dropped.
func (s *Server) notify(clientID string) {
	ch := s.signal(clientID)
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Tunnel returns an http.Handler that processes one encrypted batch per
// request and answers with one encrypted batch.
func (s *Server) Tunnel() http.Handler {
	return http.HandlerFunc(s.handleTunnel)
}

// Health returns an http.Handler for /healthz-style liveness probes.
func (s *Server) Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func (s *Server) lookup(clientID, sessionID string) *serverSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients, ok := s.byClient[clientID]
	if !ok {
		return nil
	}
	return clients[sessionID]
}

func (s *Server) register(ss *serverSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients, ok := s.byClient[ss.clientID]
	if !ok {
		clients = map[string]*serverSession{}
		s.byClient[ss.clientID] = clients
	}
	clients[ss.id] = ss
}

func (s *Server) unregister(clientID, sessionID string) {
	s.mu.Lock()
	clients, ok := s.byClient[clientID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(clients, sessionID)
	dropSignal := false
	if len(clients) == 0 {
		delete(s.byClient, clientID)
		dropSignal = true
	}
	s.mu.Unlock()
	if dropSignal {
		// M4: free the per-client wakeup channel so a churning fleet of
		// short-lived clients does not slowly leak memory.
		s.signalsMu.Lock()
		delete(s.signals, clientID)
		s.signalsMu.Unlock()
	}
}

func (s *Server) currentPolicy() PolicyEvaluator {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy
}

// dial wraps the configured dialer with a per-call context.
func (s *Server) dial(ctx context.Context, target protocol.Target) (net.Conn, error) {
	return s.dialer.Dial(ctx, target.Host, target.Port)
}
