package runtime

import "time"

// startReaper kicks off a background goroutine that walks the live session
// table and terminates anything that has been idle for longer than
// idleSessionTimeout. A session's "activity" is updated whenever inbound
// DATA arrives or upstream bytes show up in the pending buffer.
func (s *Server) startReaper() {
	s.reaperWG.Add(1)
	go func() {
		defer s.reaperWG.Done()
		for {
			s.mu.Lock()
			interval := s.idleSessionTimeout / 4
			if interval < time.Second {
				interval = time.Second
			}
			if interval > time.Minute {
				interval = time.Minute
			}
			disabled := s.idleSessionTimeout == 0
			s.mu.Unlock()
			if disabled {
				select {
				case <-s.stopCh:
					return
				case <-time.After(time.Minute):
				}
				continue
			}
			select {
			case <-s.stopCh:
				return
			case <-time.After(interval):
			}
			s.reapOnce()
		}
	}()
}

func (s *Server) reapOnce() {
	s.mu.Lock()
	cutoff := time.Now().Add(-s.idleSessionTimeout)
	type victim struct {
		clientID, sessionID string
		ss                  *serverSession
	}
	var victims []victim
	for cid, clients := range s.byClient {
		for sid, ss := range clients {
			ss.mu.Lock()
			last := ss.lastActivity
			ss.mu.Unlock()
			if last.Before(cutoff) {
				victims = append(victims, victim{cid, sid, ss})
			}
		}
	}
	s.mu.Unlock()
	for _, v := range victims {
		v.ss.terminate(errReaped)
		s.unregister(v.clientID, v.sessionID)
		s.notify(v.clientID)
		s.log().Info("session.reaped",
			"client_id", v.clientID, "session_id", v.sessionID)
	}
}
