// Package session implements the BeaconGate session state machine described
// in docs/protocol.md. It is transport- and crypto-free: callers feed it
// already-decoded protocol events and observe the resulting state changes.
package session

import (
	"errors"
	"fmt"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

type State int

const (
	StateIdle State = iota
	StateOpen
	StateHalfClosedLocal
	StateHalfClosedRemote
	StateTerminated
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateOpen:
		return "open"
	case StateHalfClosedLocal:
		return "half-closed-local"
	case StateHalfClosedRemote:
		return "half-closed-remote"
	case StateTerminated:
		return "terminated"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// Reset codes recommended by docs/protocol.md.
const (
	ResetCodeInvalidState  = "INVALID_STATE"
	ResetCodeSessionExists = "SESSION_EXISTS"
	ResetCodeBadSequence   = "BAD_SEQUENCE"
	ResetCodePolicyDenied  = "POLICY_DENIED"
	ResetCodeDialFailed    = "DIAL_FAILED"
	ResetCodePeerError     = "PEER_ERROR"
)

// SessionError carries a protocol-shaped fault with a recommended RESET code.
// Returning a SessionError is the caller's signal to emit RESET on the wire.
type SessionError struct {
	Code   string
	Reason string
}

func (e *SessionError) Error() string {
	if e.Reason == "" {
		return e.Code
	}
	return e.Code + ": " + e.Reason
}

func sessErr(code, reason string) *SessionError {
	return &SessionError{Code: code, Reason: reason}
}

var ErrAlreadyTerminated = errors.New("session: already terminated")

type Session struct {
	id       string
	clientID string
	target   protocol.Target

	state State

	// nextInSeq is the seq value expected on the next inbound DATA.
	nextInSeq uint64
	// nextOutSeq is the seq value to assign to the next outbound DATA.
	nextOutSeq uint64
}

func newSession(clientID, sessionID string, target protocol.Target) *Session {
	return &Session{
		id:       sessionID,
		clientID: clientID,
		target:   target,
		state:    StateOpen,
	}
}

func (s *Session) ID() string              { return s.id }
func (s *Session) ClientID() string        { return s.clientID }
func (s *Session) Target() protocol.Target { return s.target }
func (s *Session) State() State            { return s.state }
func (s *Session) Terminated() bool        { return s.state == StateTerminated }

// RecvData validates the inbound sequence and returns the payload to deliver.
// After CLOSE has been received, further DATA is INVALID_STATE.
func (s *Session) RecvData(seq uint64, data []byte) ([]byte, *SessionError) {
	switch s.state {
	case StateOpen, StateHalfClosedLocal:
		// allowed
	case StateHalfClosedRemote, StateTerminated, StateIdle:
		return nil, sessErr(ResetCodeInvalidState, "DATA in state "+s.state.String())
	}
	if seq != s.nextInSeq {
		return nil, sessErr(ResetCodeBadSequence,
			fmt.Sprintf("expected seq %d, got %d", s.nextInSeq, seq))
	}
	s.nextInSeq++
	return data, nil
}

// RecvClose half-closes the remote write side. Returns true when both sides
// have closed and the caller must remove the session.
func (s *Session) RecvClose() (terminal bool, err *SessionError) {
	switch s.state {
	case StateOpen:
		s.state = StateHalfClosedRemote
		return false, nil
	case StateHalfClosedLocal:
		s.state = StateTerminated
		return true, nil
	case StateHalfClosedRemote:
		return false, sessErr(ResetCodeInvalidState, "duplicate CLOSE from peer")
	default:
		return false, sessErr(ResetCodeInvalidState, "CLOSE in state "+s.state.String())
	}
}

// SendClose half-closes the local write side. Returns true when both sides
// have closed.
func (s *Session) SendClose() (terminal bool, err *SessionError) {
	switch s.state {
	case StateOpen:
		s.state = StateHalfClosedLocal
		return false, nil
	case StateHalfClosedRemote:
		s.state = StateTerminated
		return true, nil
	case StateHalfClosedLocal:
		return false, sessErr(ResetCodeInvalidState, "local already closed")
	default:
		return false, sessErr(ResetCodeInvalidState, "CLOSE in state "+s.state.String())
	}
}

// SendData allocates and returns the next outbound sequence number. It is
// invalid to call SendData after the local side has been closed.
func (s *Session) SendData() (uint64, *SessionError) {
	switch s.state {
	case StateOpen, StateHalfClosedRemote:
		seq := s.nextOutSeq
		s.nextOutSeq++
		return seq, nil
	default:
		return 0, sessErr(ResetCodeInvalidState, "send DATA in state "+s.state.String())
	}
}

// Reset transitions the session to the terminal reset state.
func (s *Session) Reset() {
	s.state = StateTerminated
}
