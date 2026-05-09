package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

const (
	// defaultIdleHold is how long a probe-only request is allowed to hang at
	// the server. The server returns early as soon as it has data to ship,
	// so this is an upper bound on quiet-period wakeups, not a typical wait.
	// 8s gives a tight inbound-channel cycle: when no data is queued, the
	// long-poll completes in 8s and the client immediately starts the next
	// poll, keeping the inbound path "fresh" with low pickup latency for
	// any newly-arriving response data. Trade-off: more Apps Script
	// invocations per minute under idle (~3× vs 25s), but well within the
	// 20K/day per-account quota for typical end-user load. Still below
	// common HTTP intermediary idle timeouts (Cloudflare 100s, nginx/Caddy
	// 60s) so no proxy will sever the connection.
	defaultIdleHold = 8 * time.Second
	// defaultActiveTimeout caps a request that carries real outbound work.
	// It needs to be larger than defaultIdleHold so a server-side long-poll
	// has room to complete naturally.
	defaultActiveTimeout = 35 * time.Second
	defaultInboxCapacity = 64
	defaultMaxChunk      = 16 * 1024

	// numWorkers is the size of the pump's worker pool. Each worker runs
	// an independent loop calling tick(); concurrent ticks let multiple
	// outbound POSTs and long-polls run in parallel over the underlying
	// HTTP/2 connection, which is the structural latency win compared to
	// a single-flight pump. Pattern adopted from GooseRelayVPN's
	// internal/carrier (workersPerEndpoint=4 in that codebase).
	numWorkers = 4

	// idleSlotsCap is the maximum number of worker goroutines that may
	// be in an idle long-poll at the same time. The remaining workers
	// are either sending outbound POSTs or backed-off-and-sleeping,
	// ready to wake when signalFlush broadcasts. idleSlotsCap MUST be
	// less than numWorkers — otherwise no worker would be available to
	// send freshly-enqueued outbound work without first waiting on some
	// in-flight long-poll to complete.
	idleSlotsCap = 2

	// outboundConcurrency is the max number of outbound POSTs (i.e.
	// POSTs carrying real session messages, not just keepalive probes)
	// that can be in flight at the same time. Pinned to 1 because the
	// wire protocol requires per-session message ordering: if OPEN and
	// DATA for the same session are split across two POSTs and arrive
	// at the server out of order, the server sees DATA for an unknown
	// session and resets it ("no such session" RESET). Long-polls run
	// in parallel — they don't carry session messages, only probes —
	// which is what gives us the inbound-latency win without breaking
	// outbound ordering. A future per-session in-flight tracker could
	// raise this cap.
	outboundConcurrency = 1
)

// waker is a broadcast notifier: Broadcast() wakes ALL goroutines
// currently blocked on C() simultaneously, unlike a buffered chan which
// only wakes one. Used to wake all idle workers when new outbound work
// arrives (signalFlush). Pattern adopted from GooseRelayVPN's carrier.
type waker struct {
	mu sync.Mutex
	ch chan struct{}
}

func newWaker() *waker { return &waker{ch: make(chan struct{})} }

// C returns the current channel to select on. Must be captured BEFORE
// entering select so a concurrent Broadcast() between drain and wait
// cannot be missed.
func (w *waker) C() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

// Broadcast unblocks all goroutines currently waiting on C().
func (w *waker) Broadcast() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.ch)
	w.ch = make(chan struct{})
}

// Pump drives a pool of background goroutines that batches outbound
// session traffic and dispatches inbound messages to per-session
// inboxes. numWorkers concurrent goroutines call tick() independently,
// so multiple outbound POSTs and idle long-polls can be in flight at
// the same time over the underlying HTTP/2 connection — that
// concurrency is what keeps end-user latency low through the
// Apps Script transport's per-call overhead. idleSlotsCap caps the
// number of workers that may be in idle long-polls simultaneously, so
// at least (numWorkers - idleSlotsCap) workers are always available to
// pick up freshly-enqueued outbound work without waiting for a
// long-poll to complete.
type Pump struct {
	rt       *Runtime
	idleHold time.Duration
	inboxCap int

	mu       sync.Mutex
	sessions map[string]*ClientSession
	outbox   []protocol.Message

	stop    chan struct{}
	stopped chan struct{}

	// parentCtx is the root context for all in-flight tick exchanges.
	// Cancelled in Close() so long-polls return promptly on shutdown.
	parentCtx    context.Context
	parentCancel context.CancelFunc

	// wake is broadcast by signalFlush. Workers in idle backoff select
	// against wake.C() so freshly-enqueued outbound work wakes them
	// immediately (instead of waiting for backoff to elapse).
	wake *waker

	// idleSlotMu / idleSlotsInFlight bound how many workers can be in
	// an idle long-poll concurrently. Workers that try to start a
	// long-poll when the cap is reached return false from tick() and
	// back off, leaving them ready to send fresh outbound work.
	idleSlotMu        sync.Mutex
	idleSlotsInFlight int

	// outboundSlot is a 1-cap semaphore that serializes outbound POSTs
	// (POSTs carrying real session messages). See outboundConcurrency
	// docstring for why per-session ordering requires this.
	outboundSlot chan struct{}

	errMu            sync.Mutex
	lastErr          error
	consecutiveFails int  // reset on every success
	reconnecting     bool // true while in exponential backoff

	// Coalescing (Workstream G): when coalesceWindow > 0, an enqueue
	// arms a timer that delays the wake-flush by coalesceWindow.
	// Subsequent enqueues within the window reset the timer, building
	// up a larger batch — fewer HTTP requests, real Apps Script quota
	// savings for interactive workloads (SSH typing, REST polling).
	//
	// safetyCap = 5 × coalesceWindow caps how long the timer can be
	// repeatedly extended; a steady stream of fast-arriving frames
	// must still flush within safetyCap so user-visible latency stays
	// bounded.
	coalesceMu        sync.Mutex
	coalesceWindow    time.Duration
	coalesceTimer     *time.Timer
	coalesceFirstKick time.Time
}

func NewPump(rt *Runtime) *Pump {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pump{
		rt:           rt,
		idleHold:     defaultIdleHold,
		inboxCap:     defaultInboxCapacity,
		sessions:     map[string]*ClientSession{},
		stop:         make(chan struct{}),
		stopped:      make(chan struct{}),
		parentCtx:    ctx,
		parentCancel: cancel,
		wake:         newWaker(),
		outboundSlot: make(chan struct{}, outboundConcurrency),
	}
}

// SetCoalesceWindow enables adaptive uplink coalescing. When d > 0,
// enqueues defer the wake-flush by d, building up larger batches.
// d == 0 disables coalescing (immediate flush, current behavior).
// Caller is responsible for clamping d to a sane range; the loader
// rejects values outside [0, 200ms] at config-load time.
func (p *Pump) SetCoalesceWindow(d time.Duration) {
	if d < 0 {
		d = 0
	}
	p.coalesceMu.Lock()
	p.coalesceWindow = d
	// If a timer was running with the old window, leave it alone —
	// it'll fire on the old schedule, then subsequent enqueues use
	// the new window. Keeps SetCoalesceWindow lock-free at the cost
	// of one stale-timer tick during reconfiguration, which is fine.
	p.coalesceMu.Unlock()
}

// SetIdleHold overrides the long-poll hold time. Tests use a tight value to
// keep their runtime small; production should keep the default.
func (p *Pump) SetIdleHold(d time.Duration) {
	p.mu.Lock()
	p.idleHold = d
	p.mu.Unlock()
}

// Log returns the logger this pump shares with its Runtime. Convenience
// for adapters layered on top (e.g. the SOCKS server).
func (p *Pump) Log() *slog.Logger { return p.rt.Log() }

func (p *Pump) Start() {
	go p.loop()
}

func (p *Pump) Close() error {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	// Cancel parent context so any in-flight tick exchange returns
	// promptly (long-polls don't have to wait their full deadline).
	p.parentCancel()
	// Also broadcast wake so any worker sleeping in idleBackoff returns
	// to its top-of-loop and observes p.stop.
	p.wake.Broadcast()
	<-p.stopped
	p.mu.Lock()
	live := make([]*ClientSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		live = append(live, s)
	}
	p.sessions = map[string]*ClientSession{}
	p.mu.Unlock()
	for _, s := range live {
		s.terminate(errors.New("pump closed"))
	}
	return nil
}

// LastError returns the last non-nil error seen by the pump goroutine.
func (p *Pump) LastError() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.lastErr
}

// Reconnect-state thresholds. Tuned conservatively so transient
// blips (a single timeout, one 5xx) don't surface as a state change
// to the operator/UI.
const (
	// degradedAfterFails: consecutive failures before state moves
	// from "connected" to "degraded".
	degradedAfterFails = 3
	// reconnectingAfterFails: consecutive failures before state
	// moves from "degraded" to "reconnecting" and the loop slows
	// to exponential backoff.
	reconnectingAfterFails = 5
	// reconnectBaseBackoff: first reconnect attempt waits this long;
	// each subsequent attempt doubles up to reconnectMaxBackoff.
	reconnectBaseBackoff = 3 * time.Second
	reconnectMaxBackoff  = 30 * time.Second
)

func (p *Pump) recordErr(err error) {
	p.errMu.Lock()
	p.lastErr = err
	p.consecutiveFails++
	fails := p.consecutiveFails
	p.errMu.Unlock()
	p.rt.RecordError(err.Error())
	switch fails {
	case degradedAfterFails:
		p.rt.SetState(StateDegraded)
		p.rt.Log().Warn("pump.degraded", "consecutive_failures", fails)
		p.rt.RecordEvent("warn", "runtime", "degraded",
			"3 consecutive transport failures",
			err.Error())
	case reconnectingAfterFails: //nolint:exhaustive // only the two threshold values are interesting
		p.rt.SetState(StateError) // visible as "error" externally; internal flag below drives backoff
		p.errMu.Lock()
		p.reconnecting = true
		p.errMu.Unlock()
		p.rt.Log().Warn("pump.reconnecting", "consecutive_failures", fails,
			"backoff_seconds", reconnectBaseBackoff.Seconds())
		p.rt.RecordEvent("warn", "runtime", "reconnecting",
			"5+ consecutive transport failures; entering exponential backoff",
			err.Error())
	}
}

// recordSuccess clears the failure counters and, if we were in
// degraded/reconnecting state, transitions back to connected and
// emits a reconnected event so support tooling can see the recovery.
func (p *Pump) recordSuccess() {
	p.errMu.Lock()
	wasReconnecting := p.reconnecting
	hadFails := p.consecutiveFails > 0
	p.consecutiveFails = 0
	p.reconnecting = false
	p.lastErr = nil
	p.errMu.Unlock()
	if wasReconnecting || hadFails {
		// Only flap state on actual recovery, not on every tick.
		p.rt.SetState(StateConnected)
		p.rt.RecordSuccessfulProbe()
		if wasReconnecting {
			p.rt.Log().Info("pump.reconnected")
			p.rt.RecordEvent("info", "runtime", "reconnected",
				"transport recovered after backoff", "")
		}
	}
}

// reconnectBackoff returns how long the loop should wait before the
// next tick when in reconnecting state. Exponential up to the max,
// reset by recordSuccess via consecutive_failures=0.
func (p *Pump) reconnectBackoff() time.Duration {
	p.errMu.Lock()
	fails := p.consecutiveFails
	p.errMu.Unlock()
	if fails < reconnectingAfterFails {
		// Not yet in reconnecting state; use the existing fast retry.
		return 200 * time.Millisecond
	}
	// fails=5 → 3s; 6 → 6s; 7 → 12s; 8 → 24s; 9+ → 30s cap.
	d := reconnectBaseBackoff
	for i := reconnectingAfterFails; i < fails && d < reconnectMaxBackoff; i++ {
		d *= 2
	}
	if d > reconnectMaxBackoff {
		d = reconnectMaxBackoff
	}
	return d
}

// Dial opens a new session against target. The returned ClientSession can be
// used as a bidirectional byte stream until Close.
func (p *Pump) Dial(target protocol.Target) (*ClientSession, error) {
	id, err := p.rt.NewID("sess")
	if err != nil {
		return nil, err
	}
	s := &ClientSession{
		id:     id,
		pump:   p,
		inbox:  make(chan []byte, p.inboxCap),
		closed: make(chan struct{}),
	}
	p.mu.Lock()
	p.sessions[id] = s
	p.outbox = append(p.outbox, protocol.Message{
		Type:      protocol.MessageTypeOpen,
		SessionID: id,
		Target:    &target,
	})
	p.mu.Unlock()
	p.signalFlush()
	p.rt.Log().Info("session.open",
		"session_id", id,
		"target", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)))
	return s, nil
}

// signalFlush wakes any worker goroutines that are sleeping in idle
// backoff so they immediately tick() and pick up newly-enqueued
// outbound work. With the worker-pool model we don't need to forcibly
// cancel in-flight long-polls — at least (numWorkers - idleSlotsCap)
// workers are guaranteed to be available (sending or backed-off) and
// signalFlush wakes them all simultaneously via the broadcast waker.
func (p *Pump) signalFlush() {
	p.wake.Broadcast()
}

// acquireIdleSlot tries to reserve one of the idleSlotsCap concurrent
// idle-long-poll slots. Returns false if all slots are taken; the
// caller should treat that as "no work this tick, back off".
func (p *Pump) acquireIdleSlot() bool {
	p.idleSlotMu.Lock()
	defer p.idleSlotMu.Unlock()
	if p.idleSlotsInFlight >= idleSlotsCap {
		return false
	}
	p.idleSlotsInFlight++
	return true
}

// releaseIdleSlot is the deferred counterpart to acquireIdleSlot.
func (p *Pump) releaseIdleSlot() {
	p.idleSlotMu.Lock()
	defer p.idleSlotMu.Unlock()
	p.idleSlotsInFlight--
}

// idleBackoff returns the sleep duration for a worker that found no
// work in n consecutive ticks. Short bumps for transient idle, longer
// bumps when the tunnel is genuinely quiet — the wake channel races
// against this timer so a kick() from new outbound work cancels the
// sleep immediately, preserving low end-user latency. Pattern from
// GooseRelayVPN's idleBackoff.
func idleBackoff(n int) time.Duration {
	switch {
	case n < 3:
		return 10 * time.Millisecond
	case n < 10:
		return 50 * time.Millisecond
	case n < 30:
		return 250 * time.Millisecond
	default:
		return time.Second
	}
}

func (p *Pump) enqueue(msgs ...protocol.Message) {
	p.mu.Lock()
	p.outbox = append(p.outbox, msgs...)
	p.mu.Unlock()
	p.scheduleFlush()
}

// scheduleFlush wakes the pump loop, but adaptively: when
// coalesceWindow is 0, fires immediately (preserves current
// behavior). When > 0, arms (or resets) a timer so multiple
// enqueues within the window collapse into one HTTP request —
// real Apps Script quota economy for interactive bursts.
//
// Safety cap: the timer can be reset for up to 5×coalesceWindow
// from the first kick of a coalesce period. Past that, the next
// reset call fires the flush directly so latency stays bounded.
func (p *Pump) scheduleFlush() {
	p.coalesceMu.Lock()
	window := p.coalesceWindow
	if window <= 0 {
		p.coalesceMu.Unlock()
		p.signalFlush()
		return
	}
	safetyCap := 5 * window
	if p.coalesceTimer == nil {
		// Open a new coalesce period.
		p.coalesceFirstKick = time.Now()
		p.coalesceTimer = time.AfterFunc(window, p.coalesceFire)
		p.coalesceMu.Unlock()
		return
	}
	// Existing period — reset the timer adaptively, but not past
	// the safety cap.
	elapsed := time.Since(p.coalesceFirstKick)
	if elapsed >= safetyCap {
		// Cap exceeded: fire now, end period.
		_ = p.coalesceTimer.Stop()
		p.coalesceTimer = nil
		p.coalesceMu.Unlock()
		p.signalFlush()
		return
	}
	remaining := safetyCap - elapsed
	next := window
	if next > remaining {
		next = remaining
	}
	_ = p.coalesceTimer.Reset(next)
	p.coalesceMu.Unlock()
}

// coalesceFire is the timer callback that ends a coalesce period and
// wakes the pump loop.
func (p *Pump) coalesceFire() {
	p.coalesceMu.Lock()
	p.coalesceTimer = nil
	p.coalesceMu.Unlock()
	p.signalFlush()
}

func (p *Pump) removeSession(id string) {
	p.mu.Lock()
	delete(p.sessions, id)
	p.mu.Unlock()
}

// loop spawns numWorkers concurrent worker goroutines, each running
// workerLoop. Workers compete for the outbox under p.mu, so multiple
// outbound POSTs and long-polls can be in flight at the same time over
// the underlying HTTP/2 connection — the structural concurrency is
// what keeps end-user latency below the per-call Apps Script overhead.
func (p *Pump) loop() {
	defer close(p.stopped)
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.workerLoop()
		}()
	}
	wg.Wait()
}

// workerLoop is one worker goroutine. It calls tick() in a loop;
// when a tick reports "no work this round" the worker sleeps with
// adaptive backoff, racing the wake channel so any signalFlush from
// newly-enqueued outbound work cancels the sleep immediately.
func (p *Pump) workerLoop() {
	consecutiveIdle := 0
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		didWork, err := p.tick()
		switch {
		case err != nil:
			p.recordErr(err)
			consecutiveIdle = 0
			// Reconnect backoff is shared across workers via the
			// recordErr-driven state machine; honor it so all
			// workers slow down together when the transport is in
			// reconnecting state.
			wait := p.reconnectBackoff()
			select {
			case <-p.stop:
				return
			case <-time.After(wait):
			}
		case didWork:
			p.recordSuccess()
			consecutiveIdle = 0
			// Loop back immediately — there might be more work.
		default:
			// Idle: no outbound, idle slot taken, or no sessions.
			// Capture wake channel BEFORE sleeping so a Broadcast()
			// fired between the work-check and the sleep is not lost.
			consecutiveIdle++
			wakeCh := p.wake.C()
			select {
			case <-p.stop:
				return
			case <-wakeCh:
				consecutiveIdle = 0
			case <-time.After(idleBackoff(consecutiveIdle)):
			}
		}
	}
}

// tick issues at most one HTTP request from one worker. If outbound
// work is queued the request carries it. If the queue is empty and
// there are live sessions, tick may try to start an idle long-poll —
// gated by acquireIdleSlot so at most idleSlotsCap workers are in idle
// long-polls concurrently. Returns:
//
//   - (true, nil)   work was sent or response received successfully
//   - (false, nil)  no work this round (no outbound, no idle slot
//                   available, or no live sessions); caller backs off
//   - (true, nil)   long-poll completed via expected wake (signalFlush
//                   cancelled or idleHold deadline fired naturally)
//   - (false, err)  real transport failure (caller calls recordErr)
func (p *Pump) tick() (didWork bool, err error) {
	p.mu.Lock()
	batch := p.outbox
	p.outbox = nil
	hasSessions := len(p.sessions) > 0
	idleHold := p.idleHold
	p.mu.Unlock()

	longPoll := false
	if len(batch) == 0 {
		if !hasSessions {
			// No sessions and no outbound — nothing to do this round.
			// Worker will back off and wait on wake channel.
			return false, nil
		}
		// Limit concurrent idle long-polls. If all idleSlotsCap slots
		// are taken, this worker backs off so it remains available to
		// fire freshly-queued outbound work without waiting for some
		// long-poll to complete first.
		if !p.acquireIdleSlot() {
			return false, nil
		}
		defer p.releaseIdleSlot()
		probeID, perr := p.rt.NewID("kp")
		if perr != nil {
			return false, perr
		}
		batch = []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: probeID}}
		longPoll = true
	}

	timeout := defaultActiveTimeout
	if longPoll {
		// idleHold + a small slack so the server-side long-poll always has
		// room to complete on its own before the client gives up.
		timeout = idleHold + 5*time.Second
	}
	// Derive from parentCtx so Pump.Close() (parentCancel) cancels all
	// in-flight ticks promptly, including idle long-polls.
	ctx, cancel := context.WithTimeout(p.parentCtx, timeout)
	defer cancel()

	// Outbound POSTs serialize through the 1-cap outboundSlot semaphore
	// to preserve per-session message ordering on the wire. Without
	// this gate, two workers could race to POST OPEN and DATA for the
	// same session — the network may reorder them, and the server
	// would see DATA-without-OPEN and reset with INVALID_STATE
	// "no such session". Long-polls (probe-only) skip the gate so
	// inbound-channel parallelism is preserved.
	if !longPoll {
		select {
		case p.outboundSlot <- struct{}{}:
		case <-ctx.Done():
			// Re-queue the batch so the next worker that wins the slot
			// picks it up. Otherwise the batch would be dropped on
			// shutdown / context cancellation.
			p.mu.Lock()
			p.outbox = append(batch, p.outbox...)
			p.mu.Unlock()
			return false, ctx.Err()
		case <-p.stop:
			p.mu.Lock()
			p.outbox = append(batch, p.outbox...)
			p.mu.Unlock()
			return false, nil
		}
		defer func() { <-p.outboundSlot }()
	}

	resp, err := p.rt.Exchange(ctx, batch)
	if err != nil {
		// Both endings are expected for long-polls and should not be
		// classified as failures by recordErr (whose 3-strike counter
		// trips state→degraded and starves the data path):
		//   - context.Canceled:        parent context (Pump.Close) or
		//                              an upstream cancellation. Treat
		//                              as completed-not-faulted for
		//                              long-polls so shutdown is quiet.
		//   - context.DeadlineExceeded: the long-poll's own
		//                              idleHold+5s deadline fired with
		//                              no upstream data — normal idle
		//                              roll-over to the next long-poll.
		if longPoll && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return true, nil
		}
		p.rt.Log().Warn("pump.exchange_failed",
			"long_poll", longPoll, "error", err.Error())
		return false, err
	}
	for _, m := range resp {
		p.dispatch(m)
	}
	return true, nil
}

func (p *Pump) dispatch(m protocol.Message) {
	switch m.Type {
	case protocol.MessageTypeData:
		p.deliverData(m.SessionID, m.Data, m.Compressed)
	case protocol.MessageTypeClose:
		p.recvClose(m.SessionID)
	case protocol.MessageTypeReset:
		p.recvReset(m.SessionID, m.Code, m.Reason)
	case protocol.MessageTypeProbe, protocol.MessageTypePing:
		// nothing to do; presence is the keepalive signal.
	}
}

func (p *Pump) deliverData(id string, data []byte, compressed bool) {
	p.mu.Lock()
	s := p.sessions[id]
	p.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.remoteClosed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if compressed {
		raw, err := protocol.DecompressData(data)
		if err != nil {
			s.terminate(err)
			return
		}
		data = raw
	}
	select {
	case s.inbox <- data:
	case <-s.closed:
	}
}

func (p *Pump) recvClose(id string) {
	p.mu.Lock()
	s := p.sessions[id]
	p.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.remoteClosed {
		s.mu.Unlock()
		return
	}
	s.remoteClosed = true
	terminate := s.localClosed
	s.mu.Unlock()
	close(s.inbox)
	if terminate {
		s.terminate(nil)
	}
}

func (p *Pump) recvReset(id, code, reason string) {
	p.mu.Lock()
	s := p.sessions[id]
	delete(p.sessions, id)
	p.mu.Unlock()
	if s == nil {
		return
	}
	p.rt.Log().Warn("session.reset",
		"session_id", id, "code", code, "reason", reason)
	s.terminate(fmt.Errorf("session reset: %s %s", code, reason))
}

// ClientSession is a bidirectional stream backed by one BeaconGate session.
type ClientSession struct {
	id     string
	pump   *Pump
	inbox  chan []byte
	closed chan struct{}

	mu           sync.Mutex
	sendSeq      uint64
	localClosed  bool
	remoteClosed bool
	err          error

	readBuf []byte
}

func (s *ClientSession) ID() string { return s.id }

func (s *ClientSession) Read(b []byte) (int, error) {
	if len(s.readBuf) > 0 {
		n := copy(b, s.readBuf)
		s.readBuf = s.readBuf[n:]
		return n, nil
	}
	select {
	case data, ok := <-s.inbox:
		if !ok {
			return 0, io.EOF
		}
		n := copy(b, data)
		if n < len(data) {
			s.readBuf = data[n:]
		}
		return n, nil
	case <-s.closed:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
}

func (s *ClientSession) Write(b []byte) (int, error) {
	s.mu.Lock()
	if s.localClosed {
		s.mu.Unlock()
		return 0, errors.New("session: write after close")
	}
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()

	written := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > defaultMaxChunk {
			chunk = chunk[:defaultMaxChunk]
		}
		s.mu.Lock()
		seq := s.sendSeq
		s.sendSeq++
		s.mu.Unlock()
		seqVal := seq
		s.pump.enqueue(buildDataMessage(s.id, &seqVal, chunk))
		written += len(chunk)
		b = b[len(chunk):]
	}
	return written, nil
}

// buildDataMessage emits a DATA message, gzip-compressing payloads that are
// large enough for compression to pay off (CompressThreshold). Smaller
// payloads stay raw because the gzip header would add bytes.
func buildDataMessage(sessID string, seq *uint64, chunk []byte) protocol.Message {
	if len(chunk) >= protocol.CompressThreshold {
		if compressed, err := protocol.CompressData(chunk); err == nil && len(compressed) < len(chunk) {
			return protocol.Message{
				Type:       protocol.MessageTypeData,
				SessionID:  sessID,
				Seq:        seq,
				Data:       compressed,
				Compressed: true,
			}
		}
	}
	return protocol.Message{
		Type:      protocol.MessageTypeData,
		SessionID: sessID,
		Seq:       seq,
		Data:      append([]byte(nil), chunk...),
	}
}

func (s *ClientSession) Close() error {
	s.mu.Lock()
	if s.localClosed {
		s.mu.Unlock()
		return nil
	}
	s.localClosed = true
	terminate := s.remoteClosed
	s.mu.Unlock()
	s.pump.enqueue(protocol.Message{Type: protocol.MessageTypeClose, SessionID: s.id})
	if terminate {
		s.terminate(nil)
	}
	return nil
}

func (s *ClientSession) terminate(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	closeInbox := !s.remoteClosed
	s.remoteClosed = true
	s.mu.Unlock()
	if closeInbox {
		close(s.inbox)
	}
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	s.pump.removeSession(s.id)
}
