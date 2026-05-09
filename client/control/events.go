package control

import (
	"net/http"
	"sync"
	"time"
)

// Event is one structured runtime event surfaced by GET /api/events.
// The shape matches STEP-2 §"Event / Log Model": small, support-oriented,
// not a raw log dump.
type Event struct {
	Timestamp time.Time `json:"ts"`
	Level     string    `json:"level"`     // info|warn|error
	Component string    `json:"component"` // runtime|transport|socks|...
	Type      string    `json:"type"`      // connected|probe_failed|transport_unreachable|...
	Summary   string    `json:"summary"`
	Detail    string    `json:"detail,omitempty"`
}

// EventSink is a capped, concurrency-safe ring buffer of Events.
// Writes never block; the oldest entry is overwritten when full.
type EventSink struct {
	mu    sync.Mutex
	buf   []Event
	head  int
	count int
	cap   int
}

// NewEventSink returns a sink that holds at most cap events.
// cap < 1 silently coerces to 1.
func NewEventSink(cap int) *EventSink {
	if cap < 1 {
		cap = 1
	}
	return &EventSink{
		buf: make([]Event, cap),
		cap: cap,
	}
}

// Record appends an event. The Timestamp is set to time.Now() if zero.
func (s *EventSink) Record(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count < s.cap {
		s.buf[(s.head+s.count)%s.cap] = e
		s.count++
		return
	}
	// full: overwrite oldest
	s.buf[s.head] = e
	s.head = (s.head + 1) % s.cap
}

// Snapshot returns a copy of the buffer in chronological order.
func (s *EventSink) Snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, s.count)
	for i := 0; i < s.count; i++ {
		out[i] = s.buf[(s.head+i)%s.cap]
	}
	return out
}

func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.events == nil {
		writeJSON(w, http.StatusOK, []Event{})
		return
	}
	writeJSON(w, http.StatusOK, a.events.Snapshot())
}
