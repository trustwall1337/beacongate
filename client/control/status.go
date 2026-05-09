package control

import (
	"context"
	"time"

	"github.com/trustwall1337/beacongate/client/runtime"
)

// StatusReport is the structured payload returned by GET /api/status.
// Field shape matches STEP-2 §"Required Status Model" and is the
// stable contract surface for any future tooling (support scripts,
// the bundled verify.sh, future desktop UI).
type StatusReport struct {
	State               string    `json:"state"` // stopped|starting|connected|degraded|error
	ClientID            string    `json:"client_id"`
	ActiveProfile       string    `json:"active_profile,omitempty"`
	ListenAddr          string    `json:"listen_addr"`
	TransportType       string    `json:"transport_type"`
	TransportHealthy    bool      `json:"transport_healthy"`
	ProbeOK             bool      `json:"probe_ok"`
	LastSuccessfulProbe time.Time `json:"last_successful_probe,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
}

// statusReport builds a StatusReport snapshot from the runtime. It runs
// a short transport diagnose to populate TransportHealthy without
// blocking on a full probe round-trip; LastSuccessfulProbe captures the
// last full probe instead.
func statusReport(rt *runtime.Runtime) StatusReport {
	cfg := rt.Config()
	report := StatusReport{
		State:         rt.State().String(),
		ClientID:      rt.ClientID(),
		ActiveProfile: rt.ActiveProfile(),
		ListenAddr:    cfg.ListenAddr,
		TransportType: cfg.Transport.Type,
	}
	report.LastError, report.LastSuccessfulProbe = rt.StatusSnapshot()
	report.ProbeOK = !report.LastSuccessfulProbe.IsZero() && report.LastError == ""

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if d, err := rt.Diagnose(ctx); err == nil {
		report.TransportHealthy = d.Healthy
	}
	return report
}
