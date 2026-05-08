package runtime

import (
	"context"
	"time"

	"github.com/trustwall1337/beacongate/engine/transport"
)

// StartupDiagnostics is a coarse readiness check intended for CLI startup.
// It runs a transport diagnose and an end-to-end PROBE so a missing crypto
// key, wrong URL, or version mismatch all surface before serving traffic.
type StartupDiagnostics struct {
	Transport transport.Diagnostics
	ProbeOK   bool
	ProbeErr  string
	Elapsed   time.Duration
}

func (r *Runtime) RunStartupDiagnostics(ctx context.Context) StartupDiagnostics {
	start := time.Now()
	diag := StartupDiagnostics{}
	tDiag, err := r.transport.Diagnose(ctx)
	diag.Transport = tDiag
	if err != nil {
		diag.ProbeErr = err.Error()
		diag.Elapsed = time.Since(start)
		return diag
	}
	if _, err := r.Probe(ctx); err != nil {
		diag.ProbeErr = err.Error()
	} else {
		diag.ProbeOK = true
	}
	diag.Elapsed = time.Since(start)
	return diag
}
