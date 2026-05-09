package control

import (
	"context"
	"net/http"
	"time"
)

// ValidateResult is the payload of POST /api/validate.
//
// The endpoint re-runs Validate() on the currently-loaded config plus
// a Probe() round-trip — the same checks `beacongate-client -validate-only`
// performs at boot, but at runtime, useful for support / verify.sh.
type ValidateResult struct {
	OK         bool   `json:"ok"`
	ConfigErr  string `json:"config_err,omitempty"`
	ProbeOK    bool   `json:"probe_ok"`
	ProbeErr   string `json:"probe_err,omitempty"`
	ProbeRTTMs int64  `json:"probe_rtt_ms,omitempty"`
}

func (a *API) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	res := ValidateResult{}
	cfg := a.rt.Config()
	if err := cfg.Validate(); err != nil {
		res.ConfigErr = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := a.rt.Probe(ctx); err != nil {
		res.ProbeErr = err.Error()
	} else {
		res.ProbeOK = true
		res.ProbeRTTMs = time.Since(start).Milliseconds()
	}
	res.OK = res.ConfigErr == "" && res.ProbeOK
	writeJSON(w, http.StatusOK, res)
}
