package appsscript

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/trustwall1337/beacongate/engine/transport"
)

const (
	diagnoseRequestTimeout = 10 * time.Second
	diagnoseMaxBody        = 4 * 1024
)

// diagnoseAll runs a doGet probe against every deployment in parallel
// and reports an aggregate result. Healthy = at least one deployment
// returned the expected JSON shape; latency = median across successes.
func (c *Client) diagnoseAll(ctx context.Context) (transport.Diagnostics, error) {
	urls := c.pool.urls()
	if len(urls) == 0 {
		return transport.Diagnostics{}, fmt.Errorf("appsscript transport: no deployments configured")
	}

	type probeResult struct {
		idx     int
		ok      bool
		latency time.Duration
		detail  string
	}
	results := make([]probeResult, len(urls))
	var wg sync.WaitGroup
	for i, url := range urls {
		wg.Add(1)
		go func(idx int, deploymentURL string) {
			defer wg.Done()
			results[idx] = c.probeOne(ctx, idx, deploymentURL)
		}(i, url)
	}
	wg.Wait()

	successes := make([]time.Duration, 0, len(results))
	failures := make([]string, 0, len(results))
	for _, r := range results {
		if r.ok {
			successes = append(successes, r.latency)
		} else {
			failures = append(failures, fmt.Sprintf("[%d] %s", r.idx, r.detail))
		}
	}

	if len(successes) == 0 {
		detail := "all deployments failed probe"
		if len(failures) > 0 {
			detail = failures[0]
		}
		return transport.Diagnostics{
			Healthy: false,
			Detail:  detail,
		}, nil
	}

	sort.Slice(successes, func(i, j int) bool { return successes[i] < successes[j] })
	median := successes[len(successes)/2]
	return transport.Diagnostics{
		Healthy: true,
		Latency: median,
		Detail:  fmt.Sprintf("%d/%d deployments healthy, median %s", len(successes), len(results), median.Round(time.Millisecond)),
	}, nil
}

func (c *Client) probeOne(ctx context.Context, idx int, url string) (r struct {
	idx     int
	ok      bool
	latency time.Duration
	detail  string
}) {
	r.idx = idx
	reqCtx, cancel := context.WithTimeout(ctx, diagnoseRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		r.detail = "build req: " + err.Error()
		return
	}
	req.Header.Set("User-Agent", c.userAgent)
	httpClient := c.clients.pick()
	if httpClient == nil {
		r.detail = "no http client"
		return
	}
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		r.detail = err.Error()
		return
	}
	defer resp.Body.Close()
	r.latency = time.Since(start)
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, diagnoseMaxBody))
	c.bumpDailyCount(idx) // doGet still consumes one quota unit
	if readErr != nil {
		r.detail = "read: " + readErr.Error()
		return
	}
	if resp.StatusCode != http.StatusOK {
		r.detail = fmt.Sprintf("status %d", resp.StatusCode)
		return
	}
	var stats scriptStatsResponse
	if err := json.Unmarshal(body, &stats); err != nil || !stats.OK {
		// Legacy Code.gs that doesn't return JSON: we still consider
		// the deployment "reachable" for liveness purposes (the HTTP
		// path works), just with degraded stats.
		r.ok = true
		r.detail = "ok (no JSON stats — redeploy Code.gs to enable)"
		return
	}
	r.ok = true
	r.detail = fmt.Sprintf("ok (script count=%d on %s)", stats.Count, stats.Date)
	return
}
