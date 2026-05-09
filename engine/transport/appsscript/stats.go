package appsscript

import (
	"sort"
	"time"
)

// Stats is the aggregated quota and deployment-health snapshot the
// appsscript transport surfaces to control-API consumers (and to the
// `beacongate-client -status` CLI). Aggregated per-account; each
// AccountStats covers all deployments under one Google-account label.
type Stats struct {
	Accounts []AccountStats `json:"accounts"`
	// NextResetAt is the soonest upcoming Apps Script daily-quota
	// reset across any tracked deployment. Zero when no quota has
	// ever been counted (e.g. fresh start, no requests yet).
	NextResetAt time.Time `json:"next_reset_at,omitempty"`
}

// AccountStats covers all deployments labeled with one Google-account
// `account` field in `script_keys`. Per-account aggregation is the
// useful unit for operators since Apps Script enforces quota and
// concurrency caps PER ACCOUNT, not per deployment.
type AccountStats struct {
	Label           string `json:"label"`
	DeploymentCount int    `json:"deployment_count"`
	// DailyCount is the sum of per-deployment client-side counters
	// (every HTTP response received bumps this, even for 403/HTML —
	// because Apps Script charges quota for every invocation).
	DailyCount uint64 `json:"daily_count"`
	// ScriptCount is the sum of per-deployment counters reported BY
	// Apps Script via the doGet endpoint. Polled hourly. May lag
	// DailyCount by an hour; large divergences indicate clock skew
	// or that someone else is hitting the deployment URL.
	ScriptCount uint64 `json:"script_count"`
	// HealthyDeployments is the count of deployments whose blacklist
	// has expired (i.e. eligible for selection right now).
	HealthyDeployments int `json:"healthy_deployments"`
}

// Stats returns a snapshot of per-account quota usage and deployment
// health. Safe to call concurrently with Roundtrip; the snapshot is
// taken under the same mutex used by the request hot path so the
// numbers are point-in-time consistent within a single call.
func (c *Client) Stats() Stats {
	now := time.Now()
	snap := c.pool.snapshot()
	byAccount := make(map[string]*AccountStats)
	var nextReset time.Time
	for i := range snap {
		ep := &snap[i]
		label := ep.account
		if label == "" {
			label = "<unlabeled>"
		}
		a, ok := byAccount[label]
		if !ok {
			a = &AccountStats{Label: label}
			byAccount[label] = a
		}
		a.DeploymentCount++
		a.DailyCount += ep.dailyCount
		a.ScriptCount += ep.scriptCount
		if !now.Before(ep.blacklistedTill) {
			a.HealthyDeployments++
		}
		if !ep.dailyResetAt.IsZero() {
			if nextReset.IsZero() || ep.dailyResetAt.Before(nextReset) {
				nextReset = ep.dailyResetAt
			}
		}
	}
	out := Stats{NextResetAt: nextReset}
	out.Accounts = make([]AccountStats, 0, len(byAccount))
	for _, a := range byAccount {
		out.Accounts = append(out.Accounts, *a)
	}
	sort.Slice(out.Accounts, func(i, j int) bool {
		return out.Accounts[i].Label < out.Accounts[j].Label
	})
	return out
}
