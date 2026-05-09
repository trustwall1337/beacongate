package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/trustwall1337/beacongate/client/control"
	"github.com/trustwall1337/beacongate/engine/transport/appsscript"
)

// runStatus connects to a running beacongate-client's local control
// API and prints a one-shot human-readable summary: lifecycle state,
// transport health, probe state, and per-account Apps Script quota.
//
// Live-refresh TUI is deferred to v1.1.1; this one-shot mode is the
// minimum the friend on the phone needs to answer "is the tunnel up?"
// and "how much quota do I have left?" without reading raw JSON.
func runStatus(controlAddr string) int {
	if controlAddr == "" {
		fmt.Fprintln(os.Stderr, "-status requires -control-addr to be set")
		return 1
	}
	base := "http://" + strings.TrimPrefix(controlAddr, "http://")

	var st control.StatusReport
	if err := getJSON(base+"/api/status", &st); err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 1
	}
	var q control.QuotaResponse
	if err := getJSON(base+"/api/quota", &q); err != nil {
		fmt.Fprintf(os.Stderr, "quota: %v\n", err)
		return 1
	}

	prettyPrintStatus(os.Stdout, st, q)
	return 0
}

// getJSON GETs a URL and decodes its JSON body into out. The url is
// constructed from operator-controlled flag values that point at the
// local-loopback control API, so gosec G107 (HTTP request with
// variable URL) is not a realistic exposure here — but to make the
// linter happy we go through http.NewRequest + http.DefaultClient.Do
// rather than the plain http.Get(string) form.
func getJSON(url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

func prettyPrintStatus(w io.Writer, st control.StatusReport, q control.QuotaResponse) {
	_, _ = fmt.Fprintf(w, "BeaconGate  •  profile: %s (%s)\n", or(st.ActiveProfile, "(unset)"), st.TransportType)
	_, _ = fmt.Fprintf(w, "  state:           %s\n", st.State)
	_, _ = fmt.Fprintf(w, "  client_id:       %s\n", st.ClientID)
	_, _ = fmt.Fprintf(w, "  listen_addr:     %s\n", st.ListenAddr)
	_, _ = fmt.Fprintf(w, "  transport:       healthy=%v probe_ok=%v\n", st.TransportHealthy, st.ProbeOK)
	if !st.LastSuccessfulProbe.IsZero() {
		_, _ = fmt.Fprintf(w, "  last_good_probe: %s (%s ago)\n",
			st.LastSuccessfulProbe.Format(time.RFC3339),
			time.Since(st.LastSuccessfulProbe).Round(time.Second))
	}
	if st.LastError != "" {
		_, _ = fmt.Fprintf(w, "  last_error:      %s\n", st.LastError)
	}

	if q.AppsScript == nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "(quota tracking only available for the appsscript transport)")
		return
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Quota (Apps Script, ~20K calls/day per Google account):")
	for _, a := range q.AppsScript.Accounts {
		printAccountBar(w, a)
	}
	if !q.AppsScript.NextResetAt.IsZero() {
		_, _ = fmt.Fprintf(w, "  next reset:    %s (%s from now)\n",
			q.AppsScript.NextResetAt.Format(time.RFC3339),
			time.Until(q.AppsScript.NextResetAt).Round(time.Minute))
	}
}

func printAccountBar(w io.Writer, a appsscript.AccountStats) {
	const dailyCap = 20000
	usage := a.DailyCount
	pct := int(usage * 100 / dailyCap)
	if pct > 100 {
		pct = 100
	}
	const barWidth = 30
	filled := pct * barWidth / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	_, _ = fmt.Fprintf(w, "  %-12s  %5d / %d  (%3d%%)  [%s]  deployments=%d (%d healthy)\n",
		a.Label, usage, dailyCap, pct, bar, a.DeploymentCount, a.HealthyDeployments)
	if a.ScriptCount > 0 && a.ScriptCount != a.DailyCount {
		_, _ = fmt.Fprintf(w, "                 (script-side count: %d)\n", a.ScriptCount)
	}
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
