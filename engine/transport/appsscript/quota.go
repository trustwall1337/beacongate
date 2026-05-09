package appsscript

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Quota tracking has two halves:
//
//   1. Per-response counter (bumpDailyCount): incremented on every HTTP
//      response received from any deployment. A single mutex acquisition
//      per response, no I/O. This satisfies Workstream A9 invariant #4
//      "no quota work on the request hot path" — bumping a counter is
//      not "quota work" in the polling/blocking sense. Polling and
//      stats fetching are what the invariant forbids on the hot path,
//      and those run in startQuotaLoop's dedicated goroutine.
//
//   2. Hourly-ish doGet poll (runQuotaPollLoop): fetches each
//      deployment's self-reported daily count and writes it to the
//      relayEndpoint snapshot. Surfaced via Diagnose().

const (
	// quotaPollInterval is how often we GET each deployment's /exec
	// to read its self-reported daily count. 30 minutes adds ~48
	// invocations/day per deployment — negligible against the ~20K/day
	// account budget.
	quotaPollInterval = 30 * time.Minute

	// quotaPollInitialDelay lets the transport warm up before the first
	// fetch so startup logs aren't interleaved with stats polls.
	quotaPollInitialDelay = 15 * time.Second

	// quotaPollRequestTimeout caps a single GET. doGet is a fast path
	// (one PropertiesService read in Apps Script).
	quotaPollRequestTimeout = 30 * time.Second

	// quotaPollMaxBody bounds the response read. The JSON payload is
	// ~50 bytes; 4 KB is a generous ceiling.
	quotaPollMaxBody = 4 * 1024
)

// quotaResetTZ is the Apps Script quota reset timezone. Google resets
// the per-account UrlFetch quota at midnight Pacific. Falls back to a
// fixed -08:00 zone if the system tzdata is unavailable.
var quotaResetTZ = func() *time.Location {
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return loc
	}
	return time.FixedZone("PST", -8*3600)
}()

// nextQuotaReset returns the next midnight in the Apps Script quota
// timezone strictly after now.
func nextQuotaReset(now time.Time) time.Time {
	local := now.In(quotaResetTZ)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, quotaResetTZ)
	if !midnight.After(now) {
		midnight = midnight.Add(24 * time.Hour)
	}
	return midnight
}

// scriptStatsResponse is the JSON the deployed Code.gs returns from
// doGet. Mirrors the shape produced in apps_script/Code.gs.
type scriptStatsResponse struct {
	OK       bool   `json:"ok"`
	Date     string `json:"date"`
	Count    int64  `json:"count"`
	Version  int    `json:"version"`
	Protocol int    `json:"protocol"`
}

// bumpDailyCount records one Apps Script invocation for an endpoint.
// Caller must NOT hold any lock related to the request hot path; this
// briefly takes endpointPool.mu and returns.
func (c *Client) bumpDailyCount(idx int) {
	if idx < 0 {
		return
	}
	now := time.Now()
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if idx >= len(c.pool.eps) {
		return
	}
	ep := &c.pool.eps[idx]
	c.touchDailyWindowLocked(ep, now)
	ep.dailyCount++
}

// touchDailyWindowLocked rolls over the daily counter when the previous
// window has elapsed. Caller must hold c.pool.mu.
func (c *Client) touchDailyWindowLocked(ep *relayEndpoint, now time.Time) {
	if ep.dailyResetAt.IsZero() {
		ep.dailyResetAt = nextQuotaReset(now)
		return
	}
	if now.Before(ep.dailyResetAt) {
		return
	}
	ep.dailyCount = 0
	ep.dailyResetAt = nextQuotaReset(now)
}

// startQuotaLoop fires the background quota poller. Called once from
// New(). Stops on Close().
func (c *Client) startQuotaLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	c.closedMu.Lock()
	c.quotaCancel = cancel
	c.closedMu.Unlock()
	go c.runQuotaPollLoop(ctx)
}

func (c *Client) runQuotaPollLoop(ctx context.Context) {
	defer close(c.quotaDone)

	select {
	case <-ctx.Done():
		return
	case <-time.After(quotaPollInitialDelay):
	}
	c.pollAllDeployments(ctx)

	t := time.NewTicker(quotaPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.pollAllDeployments(ctx)
		}
	}
}

func (c *Client) pollAllDeployments(ctx context.Context) {
	urls := c.pool.urls()
	for i, url := range urls {
		if ctx.Err() != nil {
			return
		}
		c.pollOneDeployment(ctx, i, url)
	}
}

func (c *Client) pollOneDeployment(ctx context.Context, idx int, url string) {
	reqCtx, cancel := context.WithTimeout(ctx, quotaPollRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", c.userAgent)

	httpClient := c.clients.pick()
	if httpClient == nil {
		return
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Transport error — request never reached Apps Script, so don't
		// bump the daily counter. Next interval will retry.
		return
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, quotaPollMaxBody))
	// doGet, like doPost, consumes one Apps Script invocation. Bump
	// the counter on any HTTP response we got back.
	c.bumpDailyCount(idx)
	if readErr != nil {
		return
	}
	if resp.StatusCode != http.StatusOK {
		return
	}
	c.recordScriptStatsLocked(idx, body)
}

// recordScriptStatsLocked parses a doGet response body and stores the
// reported count. Acquires c.pool.mu briefly.
func (c *Client) recordScriptStatsLocked(idx int, body []byte) {
	var stats scriptStatsResponse
	trimmed := bytes.TrimSpace(body)
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if idx < 0 || idx >= len(c.pool.eps) {
		return
	}
	ep := &c.pool.eps[idx]
	if err := json.Unmarshal(trimmed, &stats); err != nil || !stats.OK {
		// Likely the deployed Code.gs is the legacy/Goose version
		// that doesn't return JSON. Set the once-flag so we don't log
		// repeatedly. The transport stays healthy — operator just
		// loses the per-deployment stats line.
		ep.scriptStatsErrLogged = true
		return
	}
	if stats.Count >= 0 {
		ep.scriptCount = uint64(stats.Count)
	}
	ep.scriptCountAt = time.Now()
	ep.scriptStatsErrLogged = false
}
