package session

import (
	"testing"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

func target() protocol.Target {
	return protocol.Target{Network: "tcp", Host: "example.com", Port: 443}
}

func TestStoreOpenAndGet(t *testing.T) {
	s := NewStore()
	sess, err := s.Open("client-a", "sess-1", target())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.ID() != "sess-1" {
		t.Fatalf("id mismatch")
	}
	got, ok := s.Get("client-a", "sess-1")
	if !ok || got != sess {
		t.Fatalf("get failed: ok=%v got=%v", ok, got)
	}
}

func TestStoreRejectsDuplicate(t *testing.T) {
	s := NewStore()
	if _, err := s.Open("client-a", "sess-1", target()); err != nil {
		t.Fatal(err)
	}
	_, err := s.Open("client-a", "sess-1", target())
	if err == nil || err.Code != ResetCodeSessionExists {
		t.Fatalf("expected SESSION_EXISTS, got %v", err)
	}
}

func TestStoreClientIsolation(t *testing.T) {
	s := NewStore()
	if _, err := s.Open("client-a", "sess-1", target()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open("client-b", "sess-1", target()); err != nil {
		t.Fatalf("same session id under different client must be allowed: %v", err)
	}
	if _, ok := s.Get("client-c", "sess-1"); ok {
		t.Fatalf("unrelated client must not see session")
	}
	if s.CountForClient("client-a") != 1 {
		t.Fatalf("client-a should have 1 session")
	}
	if s.CountForClient("client-b") != 1 {
		t.Fatalf("client-b should have 1 session")
	}
	if s.Count() != 2 {
		t.Fatalf("total should be 2, got %d", s.Count())
	}
}

func TestStoreRemove(t *testing.T) {
	s := NewStore()
	if _, err := s.Open("client-a", "sess-1", target()); err != nil {
		t.Fatal(err)
	}
	s.Remove("client-a", "sess-1")
	if _, ok := s.Get("client-a", "sess-1"); ok {
		t.Fatalf("session should be gone")
	}
	if s.Count() != 0 {
		t.Fatalf("count should be 0")
	}
	// Removing again is a no-op.
	s.Remove("client-a", "sess-1")
	s.Remove("missing", "missing")
}
