package session

import (
	"testing"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

func makeSession() *Session {
	return newSession("client-a", "sess-1", protocol.Target{Network: "tcp", Host: "example.com", Port: 443})
}

func TestSessionStartsOpen(t *testing.T) {
	s := makeSession()
	if s.State() != StateOpen {
		t.Fatalf("expected open, got %s", s.State())
	}
	if s.Terminated() {
		t.Fatalf("should not be terminated")
	}
}

func TestRecvDataEnforcesSequence(t *testing.T) {
	s := makeSession()
	if _, err := s.RecvData(0, []byte("a")); err != nil {
		t.Fatalf("seq 0 should succeed: %v", err)
	}
	if _, err := s.RecvData(2, []byte("b")); err == nil || err.Code != ResetCodeBadSequence {
		t.Fatalf("expected BAD_SEQUENCE, got %v", err)
	}
}

func TestRecvDataAfterRecvCloseInvalid(t *testing.T) {
	s := makeSession()
	if _, err := s.RecvClose(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecvData(0, []byte("x")); err == nil || err.Code != ResetCodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %v", err)
	}
}

func TestSendCloseThenRecvCloseTerminates(t *testing.T) {
	s := makeSession()
	terminal, err := s.SendClose()
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		t.Fatalf("send-close alone should not terminate")
	}
	if s.State() != StateHalfClosedLocal {
		t.Fatalf("expected half-closed-local, got %s", s.State())
	}
	terminal, err = s.RecvClose()
	if err != nil {
		t.Fatal(err)
	}
	if !terminal {
		t.Fatalf("both-close should terminate")
	}
}

func TestRecvCloseThenSendCloseTerminates(t *testing.T) {
	s := makeSession()
	terminal, err := s.RecvClose()
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		t.Fatalf("recv-close alone should not terminate")
	}
	if s.State() != StateHalfClosedRemote {
		t.Fatalf("expected half-closed-remote, got %s", s.State())
	}
	terminal, err = s.SendClose()
	if err != nil {
		t.Fatal(err)
	}
	if !terminal {
		t.Fatalf("both-close should terminate")
	}
}

func TestSendDataAfterSendCloseInvalid(t *testing.T) {
	s := makeSession()
	if _, err := s.SendClose(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendData(); err == nil || err.Code != ResetCodeInvalidState {
		t.Fatalf("expected INVALID_STATE for send after close, got %v", err)
	}
}

func TestSendDataAssignsMonotonicSeq(t *testing.T) {
	s := makeSession()
	for i := uint64(0); i < 5; i++ {
		seq, err := s.SendData()
		if err != nil {
			t.Fatal(err)
		}
		if seq != i {
			t.Fatalf("expected %d got %d", i, seq)
		}
	}
}

func TestResetTerminates(t *testing.T) {
	s := makeSession()
	s.Reset()
	if !s.Terminated() {
		t.Fatalf("expected terminated")
	}
}
