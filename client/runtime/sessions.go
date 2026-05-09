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
	// defaultCoalesceWindow batches outbound frames that arrive within
	// this window into a single POST. Curl's TLS Finished and the
	// HTTP request that follows are typically <1 ms apart; without
	// coalescing they hit two separate ticks → two POSTs → two Apps
	// Script round-trips of overhead. 5 ms is invisible to a human
	// (TCP/IP scheduling jitter is larger) and reliably catches
	// keystroke-spaced writes too. Operators can override via
	// coalesce_step_ms in the config file, including setting a
	// larger window for SSH-style quota-economy workloads.
	defaultCoalesceWindow = 5 * time.Millisecond
)

// Pump drives the client transport. It splits work between two
// goroutines:
//
//   - The outbound worker fires HTTP POSTs that carry session traffic
//     (OPEN/DATA/CLOSE/RESET). When the outbox is empty, it parks on
//     the flush channel — it does NOT issue idle long-polls, so it
//     can fire a new outbound POST the instant traffic arrives.
//   - The idle worker keeps a single PROBE long-poll standing at the
//     server whenever there are live sessions. It is the receive-side
//     pipe for upstream-originated bytes that arrive *between* outbound
//     POSTs (e.g. late TLS handshake responses, or any push from the
//     upstream while the outbound worker is parked).
//
// Why split: with one in-flight request, every TLS handshake leg
// serializes through one Apps Script round-trip (~2 s overhead each)
// and a non-responsive leg (e.g. TLS 1.3 client Finished) stalls the
// active-drain ceiling. Two concurrent POSTs let the outbound POST
// return as soon as its drain window expires — the upstream's eventual
// reply is picked up by the idle worker's standing long-poll without
// waiting for the next outbound. See server/runtime: defaultActive
// DrainWindow comment for the matching server-side rationale.
//
// Ordering: the server's drainAllForClient is atomic per-client, so
// each upstream chunk is delivered exactly once via exactly one POST.
// Apps Script per-call latency variance can still let two POSTs return
// out-of-order on the wire — handled by per-session receive-side seq
// reordering in deliverData.
type Pump struct {
	rt       *Runtime
	idleHold time.Duration
	inboxCap int

	mu       sync.Mutex
	sessions map[string]*ClientSession
	outbox   []protocol.Message

	// flush wakes the outbound worker on new enqueue. flushIdle wakes
	// the idle worker when the session set transitions empty → non-
	// empty (it self-perpetuates after that). With one cap-1 channel
	// per worker, signalFlush can wake both reliably; a single shared
	// channel would deliver to exactly one waiter and silently strand
	// the other parked.
	flush     chan struct{}
	flushIdle chan struct{}
	stop      chan struct{}
	stopped   chan struct{}

	// cancelInFlight gates the outbound worker's currently-blocked
	// long-poll. Today the outbound worker no longer issues long-polls
	// (those moved to the idle worker), but the cancel hook is kept so
	// that any future code path which has the outbound worker waiting
	// on something interruptible can be unblocked by signalFlush.
	cancelMu       sync.Mutex
	cancelInFlight context.CancelFunc

	// idleCancel cancels the idle worker's currently-in-flight long-
	// poll. Used on Close; not used on signalFlush — the idle worker
	// is supposed to keep polling regardless of outbound activity, so
	// kicking it on every flush is wasted Apps Script overhead.
	idleCancelMu sync.Mutex
	idleCancel   context.CancelFunc

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
	return &Pump{
		rt:             rt,
		idleHold:       defaultIdleHold,
		inboxCap:       defaultInboxCapacity,
		sessions:       map[string]*ClientSession{},
		flush:          make(chan struct{}, 1),
		flushIdle:      make(chan struct{}, 1),
		stop:           make(chan struct{}),
		stopped:        make(chan struct{}),
		coalesceWindow: defaultCoalesceWindow,
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
	// Cancel any in-flight idle long-poll so its goroutine doesn't
	// linger waiting for the server's full window.
	p.idleCancelMu.Lock()
	if c := p.idleCancel; c != nil {
		c()
	}
	p.idleCancelMu.Unlock()
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
//
// OPEN is NOT enqueued here. It is held on the session as openPending and
// emitted by the first session.Write or session.Close call (batched with
// that call's DATA / CLOSE message into a single outbound POST), or — in
// the degenerate "open and never use" case — by a 1 s safety timer.
//
// Why deferred: the outbound worker fires its POST as soon as the outbox
// is non-empty. If Dial appended OPEN immediately, the worker would ship
// OPEN alone and pay one full Apps Script round-trip (~2 s) before the
// caller's first byte even gets the chance to ride the same POST. On
// HTTPS, where curl's TLS init delays the first ClientHello by 50–500 ms,
// that's two sequential round-trips for a single logical handshake leg.
// Holding OPEN on the session collapses it into one round-trip per leg.
//
// The 1 s safety timer covers the degenerate path. The v1.0 attempt used
// a 50 ms timer and was reverted because curl-with-TLS often takes longer
// than 50 ms to generate its first ClientHello — the timer fired
// prematurely, shipped OPEN alone, and the upstream's TLS server closed
// its idle socket before DATA arrived. 1 s is well above curl's typical
// init time and well below the 10–15 s upstream idle timeout.
func (p *Pump) Dial(target protocol.Target) (*ClientSession, error) {
	id, err := p.rt.NewID("sess")
	if err != nil {
		return nil, err
	}
	s := &ClientSession{
		id:          id,
		pump:        p,
		target:      target,
		inbox:       make(chan []byte, p.inboxCap),
		closed:      make(chan struct{}),
		openPending: true,
	}
	p.mu.Lock()
	p.sessions[id] = s
	p.mu.Unlock()
	s.mu.Lock()
	s.openSafetyTimer = time.AfterFunc(time.Second, s.flushOpenIfPending)
	s.mu.Unlock()
	p.rt.Log().Info("session.open",
		"session_id", id,
		"target", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)))
	return s, nil
}

// signalFlush wakes the outbound worker so a freshly-enqueued frame
// fires its POST immediately. It also pokes the idle worker — only
// useful on the empty→non-empty session transition (Dial), but cheap
// enough to do unconditionally — and cancels any in-flight wait the
// outbound worker may be blocked on.
func (p *Pump) signalFlush() {
	select {
	case p.flush <- struct{}{}:
	default:
	}
	select {
	case p.flushIdle <- struct{}{}:
	default:
	}
	p.cancelMu.Lock()
	c := p.cancelInFlight
	p.cancelMu.Unlock()
	if c != nil {
		c()
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

func (p *Pump) loop() {
	defer close(p.stopped)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p.outboundLoop()
	}()
	go func() {
		defer wg.Done()
		p.idleLoop()
	}()
	wg.Wait()
}

// outboundLoop is the worker that fires HTTP POSTs carrying real
// session traffic. It parks on the flush/stop channels when the outbox
// is empty — never issues idle long-polls, so a freshly-enqueued frame
// fires its POST without waiting for any standing PROBE to return.
func (p *Pump) outboundLoop() {
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		if err := p.outboundTick(); err != nil {
			p.recordErr(err)
			wait := p.reconnectBackoff()
			select {
			case <-p.stop:
				return
			case <-time.After(wait):
			}
		} else {
			p.recordSuccess()
		}
	}
}

// outboundTick fires one POST when the outbox has work, or parks on
// flush/stop when the outbox is empty. Unlike the legacy single-pump
// tick, this does NOT fall back to issuing PROBE long-polls — that's
// the idle worker's job.
func (p *Pump) outboundTick() error {
	p.mu.Lock()
	batch := p.outbox
	p.outbox = nil
	p.mu.Unlock()

	if len(batch) == 0 {
		// Park until the next enqueue or shutdown. We don't care
		// whether there are live sessions: the idle worker handles
		// keepalive long-polls; the outbound worker only wakes for
		// real outbound work.
		select {
		case <-p.flush:
		case <-p.stop:
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultActiveTimeout)
	defer cancel()

	resp, err := p.rt.Exchange(ctx, batch)
	if err != nil {
		p.rt.Log().Warn("pump.exchange_failed",
			"worker", "outbound", "error", err.Error())
		return err
	}
	for _, m := range resp {
		p.dispatch(m)
	}
	return nil
}

// idleLoop is the standing-long-poll worker. While there is at least
// one live session it keeps a single PROBE in flight at the server,
// short-circuiting on upstream data so late responses (those whose
// active POST already returned with empty drain) reach the client
// without waiting for the next outbound POST. When there are no live
// sessions, it parks on flush/stop and emits no HTTP traffic.
func (p *Pump) idleLoop() {
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		if err := p.idleTick(); err != nil {
			p.recordErr(err)
			wait := p.reconnectBackoff()
			select {
			case <-p.stop:
				return
			case <-time.After(wait):
			}
		} else {
			p.recordSuccess()
		}
	}
}

// idleTick issues one PROBE long-poll if there are live sessions, or
// parks on flush/stop otherwise. It never carries outbound DATA — that
// stays the outbound worker's responsibility, so frame ordering inside
// a session is preserved.
func (p *Pump) idleTick() error {
	p.mu.Lock()
	hasSessions := len(p.sessions) > 0
	idleHold := p.idleHold
	p.mu.Unlock()

	if !hasSessions {
		// No live sessions → nothing to keep alive. Park until either
		// a session is created (signalFlush kicks flushIdle) or
		// shutdown.
		select {
		case <-p.flushIdle:
		case <-p.stop:
		}
		return nil
	}

	probeID, err := p.rt.NewID("kp")
	if err != nil {
		return err
	}
	batch := []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: probeID}}

	timeout := idleHold + 5*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	p.idleCancelMu.Lock()
	p.idleCancel = cancel
	p.idleCancelMu.Unlock()
	defer func() {
		p.idleCancelMu.Lock()
		p.idleCancel = nil
		p.idleCancelMu.Unlock()
		cancel()
	}()

	resp, err := p.rt.Exchange(ctx, batch)
	if err != nil {
		// context.Canceled / DeadlineExceeded are normal long-poll
		// completions, not transport failures. Surfacing them as
		// errors would trip the 3-strike degraded-state counter.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		p.rt.Log().Warn("pump.exchange_failed",
			"worker", "idle", "error", err.Error())
		return err
	}
	for _, m := range resp {
		p.dispatch(m)
	}
	return nil
}

// tick is preserved for the existing test surface (reconnect_test.go
// calls it directly). It runs exactly one POST: outbound if the outbox
// has work, otherwise an idle long-poll if there are live sessions,
// otherwise parks. The split worker loop above is what production uses;
// this single-shot variant keeps the legacy test contract working.
func (p *Pump) tick() error {
	p.mu.Lock()
	batch := p.outbox
	p.outbox = nil
	hasSessions := len(p.sessions) > 0
	idleHold := p.idleHold
	p.mu.Unlock()

	longPoll := false
	if len(batch) == 0 {
		if !hasSessions {
			select {
			case <-p.flush:
			case <-p.stop:
			}
			return nil
		}
		probeID, err := p.rt.NewID("kp")
		if err != nil {
			return err
		}
		batch = []protocol.Message{{Type: protocol.MessageTypeProbe, ProbeID: probeID}}
		longPoll = true
	}

	timeout := defaultActiveTimeout
	if longPoll {
		timeout = idleHold + 5*time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	if longPoll {
		p.cancelMu.Lock()
		p.cancelInFlight = cancel
		p.cancelMu.Unlock()
	}
	defer func() {
		if longPoll {
			p.cancelMu.Lock()
			p.cancelInFlight = nil
			p.cancelMu.Unlock()
		}
		cancel()
	}()

	resp, err := p.rt.Exchange(ctx, batch)
	if err != nil {
		if longPoll && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil
		}
		p.rt.Log().Warn("pump.exchange_failed",
			"long_poll", longPoll, "error", err.Error())
		return err
	}
	for _, m := range resp {
		p.dispatch(m)
	}
	return nil
}

func (p *Pump) dispatch(m protocol.Message) {
	switch m.Type {
	case protocol.MessageTypeData:
		p.deliverData(m.SessionID, m.Seq, m.Data, m.Compressed)
	case protocol.MessageTypeClose:
		p.recvClose(m.SessionID)
	case protocol.MessageTypeReset:
		p.recvReset(m.SessionID, m.Code, m.Reason)
	case protocol.MessageTypeProbe, protocol.MessageTypePing:
		// nothing to do; presence is the keepalive signal.
	}
}

// deliverData routes one DATA message from the wire into the session's
// inbox. With concurrent POSTs (outbound + idle worker) the same
// upstream byte stream can produce DATA messages that arrive on the
// HTTP wire in a different order than the server's send-seq order:
// Apps Script per-call latency variance can let a later POST return
// before an earlier one. Use seq to reassemble the original order
// before delivering to the inbox.
//
// Legacy server responses without seq populated bypass the reorder
// buffer (sessions in the legacy single-pump deployment never see
// out-of-order DATA, so no behaviour change for them).
func (p *Pump) deliverData(id string, seq *uint64, data []byte, compressed bool) {
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

	if seq == nil {
		// Pre-seq server response or non-data path. Deliver directly.
		s.deliverInOrder(data)
		return
	}

	s.recvMu.Lock()
	expected := s.nextRecvSeq
	switch {
	case *seq < expected:
		// Duplicate (idempotent retry replay surfaced as a re-delivery,
		// or a benign re-send across two POSTs). Drop.
		s.recvMu.Unlock()
		return
	case *seq > expected:
		// Out-of-order arrival: the missing seq is still in flight on
		// another POST. Buffer this one and wait for the gap to close.
		// recvBuffer is sized lazily so sessions that never see
		// out-of-order traffic pay nothing.
		if s.recvBuffer == nil {
			s.recvBuffer = make(map[uint64][]byte)
		}
		// Keep a defensive copy: the underlying slice may be reused
		// by the caller (decoder, decompressor) once we return.
		buf := append([]byte(nil), data...)
		s.recvBuffer[*seq] = buf
		s.recvMu.Unlock()
		return
	}

	// In-order: deliver this chunk plus any contiguous run already
	// buffered. Build the ordered list under recvMu, then drop the
	// lock before pushing into the inbox so a slow consumer cannot
	// stall a peer thread holding recvMu.
	ordered := [][]byte{data}
	s.nextRecvSeq++
	for {
		next, ok := s.recvBuffer[s.nextRecvSeq]
		if !ok {
			break
		}
		ordered = append(ordered, next)
		delete(s.recvBuffer, s.nextRecvSeq)
		s.nextRecvSeq++
	}
	s.recvMu.Unlock()

	for _, chunk := range ordered {
		s.deliverInOrder(chunk)
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
	target protocol.Target
	inbox  chan []byte
	closed chan struct{}

	mu           sync.Mutex
	sendSeq      uint64
	localClosed  bool
	remoteClosed bool
	err          error

	readBuf []byte

	// openPending is true between Dial and the first Write/Close (or
	// safety-timer fire). While set, the OPEN message has not yet been
	// enqueued — it is emitted atomically with the first Write/Close to
	// fuse the OPEN+first-DATA POST into a single Apps Script round-
	// trip. Protected by mu.
	openPending bool
	// openSafetyTimer fires after 1 s post-Dial if the user hasn't
	// written or closed. Held on the session so any code path
	// (Write, Close, terminate, the timer itself) can Stop it cleanly.
	openSafetyTimer *time.Timer

	// Receive-side reordering state. With concurrent POSTs (outbound
	// + idle worker), the server's send-seq order is preserved across
	// posts but the *HTTP wire return order* is not — Apps Script per-
	// call latency variance can let a later POST return earlier.
	// recvMu protects nextRecvSeq + recvBuffer; recvBuffer holds
	// out-of-order chunks until the missing seq arrives.
	recvMu      sync.Mutex
	nextRecvSeq uint64
	recvBuffer  map[uint64][]byte
}

// deliverInOrder pushes one chunk into the session's inbox. It is
// called only after the reorder buffer has produced the chunk in seq
// order, OR for legacy server responses that don't populate seq.
func (s *ClientSession) deliverInOrder(data []byte) {
	select {
	case s.inbox <- data:
	case <-s.closed:
	}
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

	// consumeOpenPending atomically clears openPending and stops the
	// safety timer; it returns the OPEN message that must lead the
	// first batch (or nil if already consumed).
	openMsg := s.consumeOpenPending()

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
		dataMsg := buildDataMessage(s.id, &seqVal, chunk)
		// First chunk after Dial: ship OPEN and DATA in the SAME
		// outbound batch so the pump fires one POST instead of two.
		if openMsg != nil {
			s.pump.enqueue(*openMsg, dataMsg)
			openMsg = nil
		} else {
			s.pump.enqueue(dataMsg)
		}
		written += len(chunk)
		b = b[len(chunk):]
	}
	return written, nil
}

// consumeOpenPending atomically clears the openPending flag and stops
// the safety timer. Returns the OPEN message the caller must lead the
// next outbound batch with, or nil if OPEN was already emitted (by an
// earlier Write/Close or by the timer itself).
func (s *ClientSession) consumeOpenPending() *protocol.Message {
	s.mu.Lock()
	if !s.openPending {
		s.mu.Unlock()
		return nil
	}
	s.openPending = false
	timer := s.openSafetyTimer
	s.openSafetyTimer = nil
	target := s.target
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	return &protocol.Message{
		Type:      protocol.MessageTypeOpen,
		SessionID: s.id,
		Target:    &target,
	}
}

// flushOpenIfPending is the safety-timer callback. If the user hasn't
// written or closed within the deadline, ship OPEN alone so the server
// gets to dial the upstream eventually. The timer is auto-cancelled by
// any earlier Write/Close via consumeOpenPending.
func (s *ClientSession) flushOpenIfPending() {
	openMsg := s.consumeOpenPending()
	if openMsg == nil {
		return
	}
	s.pump.enqueue(*openMsg)
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
	// Close-without-Write also satisfies the OPEN-fusion trigger: ship
	// [OPEN, CLOSE] in one POST so the server dials and immediately
	// tears down in a single Apps Script round-trip.
	openMsg := s.consumeOpenPending()
	closeMsg := protocol.Message{Type: protocol.MessageTypeClose, SessionID: s.id}
	if openMsg != nil {
		s.pump.enqueue(*openMsg, closeMsg)
	} else {
		s.pump.enqueue(closeMsg)
	}
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
	timer := s.openSafetyTimer
	s.openSafetyTimer = nil
	s.mu.Unlock()
	if timer != nil {
		// Stop is a no-op if it already fired; either way, drop the
		// reference so a churning fleet of short-lived sessions doesn't
		// keep a goroutine per session for up to 1 s after termination.
		timer.Stop()
	}
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
