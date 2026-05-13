package runtime

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

// maxInboundReorderBuffer caps how many out-of-order DATA frames per
// session the server will hold while waiting for the missing seq to
// arrive. With the client running multiple outbound POST workers,
// frames can land out of order on the wire (Apps Script latency
// variance); buffering up to this many fills the gap without
// rejecting traffic. Beyond this we terminate the session — something
// is structurally wrong (lost frame, client bug) and indefinite
// buffering would leak memory.
const maxInboundReorderBuffer = 32

// serverSession holds the upstream-bound state for one client session.
type serverSession struct {
	id       string
	clientID string
	target   protocol.Target
	conn     net.Conn

	// writeMu serializes the entire inbound-DATA write path for this
	// session. Acquired BEFORE mu in handleData so that two concurrent
	// inbound-DATA calls can't claim contiguous seq ranges under mu,
	// release mu, and then race conn.Write — which would put bytes on
	// the upstream socket out of order. Lock ordering: writeMu → mu.
	// Other code paths (readUpstream, drain, terminate) only touch mu.
	writeMu sync.Mutex

	mu sync.Mutex

	// nextRecvSeq is the seq value expected on the next inbound DATA from
	// the client. The session lifecycle is documented in
	// docs/protocol.md §"Session Lifecycle"; this struct is the
	// server-side implementation.
	nextRecvSeq uint64
	// recvBuffer holds out-of-order inbound DATA frames keyed by seq.
	// Populated when a frame's seq > nextRecvSeq (a gap); drained when
	// the missing seq arrives. Capped at maxInboundReorderBuffer; an
	// overflow terminates the session as BAD_SEQUENCE.
	recvBuffer map[uint64][]byte
	// nextSendSeq is the seq value to assign to the next outbound DATA we
	// send back to the client.
	nextSendSeq uint64

	localClosed  bool // we sent CLOSE to the client
	remoteClosed bool // client sent CLOSE to us
	terminated   bool

	// pending is upstream-read data waiting to be batched into DATA messages.
	pending []byte
	upErr   error

	// lastActivity is updated on every read or write across the upstream.
	// Used by the idle-session reaper to drop dead sessions.
	lastActivity time.Time
}

// errReaped is the terminal error stamped on a session the reaper closed.
var errReaped = errors.New("server: idle session reaped")

func (s *serverSession) writeUpstream(b []byte) error {
	s.mu.Lock()
	if s.upErr != nil {
		err := s.upErr
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	_, err := s.conn.Write(b)
	s.mu.Lock()
	s.lastActivity = time.Now()
	if err != nil && s.upErr == nil {
		s.upErr = err
	}
	s.mu.Unlock()
	return err
}

func (s *serverSession) drain(maxChunk int) ([][]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil, s.upErr != nil || s.terminated, s.upErr
	}
	var out [][]byte
	buf := s.pending
	s.pending = nil
	for len(buf) > 0 {
		n := len(buf)
		if n > maxChunk {
			n = maxChunk
		}
		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		out = append(out, chunk)
		buf = buf[n:]
	}
	return out, s.upErr != nil || s.terminated, s.upErr
}

func (s *serverSession) terminate(err error) {
	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		return
	}
	s.terminated = true
	if err != nil && s.upErr == nil {
		s.upErr = err
	}
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *serverSession) closeWriteUpstream() {
	s.mu.Lock()
	s.remoteClosed = true
	conn := s.conn
	s.mu.Unlock()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}
