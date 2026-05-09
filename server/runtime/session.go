package runtime

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

// serverSession holds the upstream-bound state for one client session.
type serverSession struct {
	id       string
	clientID string
	target   protocol.Target
	conn     net.Conn

	mu sync.Mutex

	// nextRecvSeq is the seq value expected on the next inbound DATA from
	// the client. The session lifecycle is documented in
	// docs/protocol.md §"Session Lifecycle"; this struct is the
	// server-side implementation.
	nextRecvSeq uint64
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

func (s *serverSession) markActivity() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
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
		conn.Close()
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
