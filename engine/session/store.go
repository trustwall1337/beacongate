package session

import (
	"sync"

	"github.com/trustwall1337/beacongate/engine/protocol"
)

// Store keeps live sessions partitioned by client_id so that one client cannot
// see, address, or collide with another client's sessions.
type Store struct {
	mu       sync.Mutex
	byClient map[string]map[string]*Session
}

func NewStore() *Store {
	return &Store{byClient: map[string]map[string]*Session{}}
}

// Open registers a new session for clientID. SESSION_EXISTS is returned if the
// session id is already live for that client.
func (s *Store) Open(clientID, sessionID string, target protocol.Target) (*Session, *SessionError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients, ok := s.byClient[clientID]
	if !ok {
		clients = map[string]*Session{}
		s.byClient[clientID] = clients
	}
	if _, exists := clients[sessionID]; exists {
		return nil, sessErr(ResetCodeSessionExists, "session "+sessionID+" already open")
	}
	sess := newSession(clientID, sessionID, target)
	clients[sessionID] = sess
	return sess, nil
}

// Get returns the live session for the (clientID, sessionID) pair if one
// exists. Sessions are scoped to clientID so cross-client lookups always miss.
func (s *Store) Get(clientID, sessionID string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients, ok := s.byClient[clientID]
	if !ok {
		return nil, false
	}
	sess, ok := clients[sessionID]
	return sess, ok
}

// Remove deletes the session entry. It is safe to call on an already-removed
// session.
func (s *Store) Remove(clientID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients, ok := s.byClient[clientID]
	if !ok {
		return
	}
	delete(clients, sessionID)
	if len(clients) == 0 {
		delete(s.byClient, clientID)
	}
}

// Count returns the number of live sessions across all clients. Useful for
// diagnostics and tests.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.byClient {
		n += len(c)
	}
	return n
}

// CountForClient returns the number of live sessions for one client.
func (s *Store) CountForClient(clientID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byClient[clientID])
}
