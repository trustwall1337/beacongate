// Package control exposes a local HTTP API on the client process for
// desktop, CLI, and automation consumers. It is loopback-only by design
// so the engine never depends on a network-facing trust decision.
//
// Phase 1 surface (per STEP-2-control-api §"Minimum Control Surface",
// scoped to the strict Phase-1 subset — no lifecycle or profile CRUD):
//
//   - GET  /api/status     full status model (state, transport, probe, last error)
//   - GET  /api/health     coarse transport health (used by external probes)
//   - GET  /api/diagnose   full startup diagnostics (transport + probe round-trip)
//   - GET  /api/events     recent structured events (ring buffer, capped)
//   - POST /api/validate   re-validate the currently loaded config + run a probe
//
// Lifecycle (connect/disconnect) and profile CRUD are deferred to a
// future desktop wrapper — Phase 1 process model is one-shot
// (`./beacongate-client` foreground or under systemd / Termux).
package control

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/trustwall1337/beacongate/client/runtime"
)

type API struct {
	rt     *runtime.Runtime
	events *EventSink
}

// New builds a control API with default options (256-entry event ring).
// The API installs itself as the runtime's event recorder so runtime-
// side state changes (degraded, reconnecting, reconnected) flow into
// the ring buffer exposed via GET /api/events.
func New(rt *runtime.Runtime) *API {
	a := &API{rt: rt, events: NewEventSink(256)}
	rt.SetEventRecorder(func(level, component, eventType, summary, detail string) {
		a.events.Record(Event{
			Level:     level,
			Component: component,
			Type:      eventType,
			Summary:   summary,
			Detail:    detail,
		})
	})
	return a
}

// Events returns the API's event sink. Runtime callers (the pump,
// startup diagnostics) record into this sink in parallel with their
// existing slog logging — events are a separate, structured surface,
// not a replacement for logs.
func (a *API) Events() *EventSink { return a.events }

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/health", a.handleHealth)
	mux.HandleFunc("/api/diagnose", a.handleDiagnose)
	mux.HandleFunc("/api/events", a.handleEvents)
	mux.HandleFunc("/api/validate", a.handleValidate)
	mux.HandleFunc("/api/quota", a.handleQuota)
	return loopbackOnly(mux)
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusReport(a.rt))
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	d, err := a.rt.Diagnose(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *API) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	report := a.rt.RunStartupDiagnostics(ctx)
	writeJSON(w, http.StatusOK, report)
}
