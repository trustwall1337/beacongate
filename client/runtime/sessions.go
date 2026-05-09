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
	// 25s is below common HTTP intermediary idle timeouts (Cloudflare 100s,
	// nginx/Caddy 60s) so no proxy will sever the connection.
	defaultIdleHold = 25 * time.Second
	// defaultActiveTimeout caps a request that carries real outbound work.
	// It needs to be larger than defaultIdleHold so a server-side long-poll
	// has room to complete naturally.
	defaultActiveTimeout = 35 * time.Second
	defaultInboxCapacity = 64
	defaultMaxChunk      = 16 * 1024
)

// Pump drives a background goroutine that batches outbound session traffic
// and dispatches inbound messages to per-session inboxes. The HTTP transport
// is request/response, so the pump uses one in-flight request at a time:
// when there is outbound work, the request fires immediately; when idle,
// the request sends a single PROBE that the server holds open ("long-poll")
// until upstream data is ready or until the hold window expires. Newly
// enqueued outbound work cancels the in-flight long-poll so user traffic
// is never blocked behind keepalive.
type Pump struct {
	rt       *Runtime
	idleHold time.Duration
	inboxCap int

	mu       sync.Mutex
	sessions map[string]*ClientSession
	outbox   []protocol.Message

	flush   chan struct{}
	stop    chan struct{}
	stopped chan struct{}

	cancelMu       sync.Mutex
	cancelInFlight context.CancelFunc

	errMu   sync.Mutex
	lastErr error

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
		rt:       rt,
		idleHold: defaultIdleHold,
		inboxCap: defaultInboxCapacity,
		sessions: map[string]*ClientSession{},
		flush:    make(chan struct{}, 1),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
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

func (p *Pump) recordErr(err error) {
	p.errMu.Lock()
	p.lastErr = err
	p.errMu.Unlock()
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

// signalFlush wakes the pump loop, asking it to issue an HTTP request now.
// It also cancels any in-flight long-poll so newly enqueued outbound work
// is not blocked behind a keepalive request.
func (p *Pump) signalFlush() {
	select {
	case p.flush <- struct{}{}:
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
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		if err := p.tick(); err != nil {
			p.recordErr(err)
			// Avoid hot-looping on persistent transport errors.
			select {
			case <-p.stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

// tick issues exactly one HTTP request. If outbound work is queued the
// request carries it and uses the active timeout. If the queue is empty
// and there are live sessions, the request is a long-poll PROBE that lets
// the server hold open until upstream data is ready. If there are no
// outbound messages and no sessions, the pump parks on the flush/stop
// channels — no HTTP traffic is generated.
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
			// Truly idle: no live sessions, nothing to keep alive. Park.
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
		// idleHold + a small slack so the server-side long-poll always has
		// room to complete on its own before the client gives up.
		timeout = idleHold + 5*time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	// Register cancel so signalFlush can break us out of the long-poll when
	// new outbound work arrives. Only do this for long-polls; active
	// requests carry data and shouldn't be interrupted.
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
		// A long-poll cancelled by signalFlush is expected, not a fault.
		if longPoll && errors.Is(err, context.Canceled) {
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
