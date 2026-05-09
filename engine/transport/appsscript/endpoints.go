package appsscript

import (
	"sync"
	"time"
)

// relayEndpoint holds per-deployment state: the script.google.com URL, an
// optional account label for stats grouping, transient backoff state on
// failure, and the daily quota counters.
//
// Mutated under a single endpointMu mutex held briefly per access — quota
// reads/writes are off the request hot path (Workstream A9 invariant #4).
type relayEndpoint struct {
	url     string // full https://script.google.com/macros/s/{id}/exec URL
	account string // operator label, "" = unlabeled

	// blacklistedTill is the next time at which this endpoint is eligible
	// for selection again. Failure backoff is exponential between
	// endpointBlacklistBaseTTL and endpointBlacklistMaxTTL.
	blacklistedTill time.Time
	failCount       int

	// dailyCount is incremented per HTTP response received (regardless of
	// status), since every Apps Script doPost invocation consumes one
	// quota unit even when it returns 403 or an HTML error page.
	// Transport-level failures (no response reached Apps Script) do not
	// count.
	dailyCount   uint64
	dailyResetAt time.Time

	// scriptCount is the deployment's self-reported count from the hourly
	// doGet poll. zero scriptCountAt = never successfully fetched.
	scriptCount   uint64
	scriptCountAt time.Time

	// scriptStatsErrLogged suppresses repeat "needs redeploy" warnings
	// when the deployed Code.gs is the legacy version that doesn't
	// return JSON. Cleared on the first successful parse.
	scriptStatsErrLogged bool
}

const (
	// Failure backoff bounds for an endpoint after a non-quota error.
	// These are intentionally conservative so a flapping deployment
	// gets pulled out of rotation quickly and re-evaluated rarely.
	endpointBlacklistBaseTTL = 3 * time.Second
	endpointBlacklistMaxTTL  = 1 * time.Hour

	// quotaBlacklistTTL is applied when an endpoint returns a 403 or any
	// HTML body shape suggesting Apps Script quota exhaustion. The
	// endpoint stays out of rotation until the next quota reset window
	// (midnight Pacific) — exponential backoff is wrong here because
	// the underlying cause won't clear until the calendar advances.
	quotaBlacklistTTL = 30 * time.Minute
)

// endpointPool owns the slice of relayEndpoints plus the round-robin
// cursor.
//
// **Bucket awareness (v1.1.0):** endpoints sharing the same `account`
// label form a "bucket". Selection rotates BUCKETS first — pick from
// the next bucket before re-picking from the same one — so quota draw
// is spread evenly across the operator's Google accounts. Within a
// bucket, deployments round-robin and skip-on-blacklist as before.
//
// Same-bucket failover: when an attempt against deployment X in
// bucket A fails, pickFallback prefers a different deployment in
// bucket A (same quota cap, same per-account rate limit) before
// crossing into bucket B. Cross-bucket fallback only fires when
// bucket A has no other live deployment.
//
// What v1.1.0 does NOT do: spawn N parallel poll workers per bucket
// (Apps Script's per-account concurrency cap permits ~4 in-flight
// requests; matching that with parallel workers is a v1.2 follow-up).
// The carrier above this pool remains single-Roundtrip; bucket-aware
// selection improves quota distribution without changing the
// in-flight request count.
type endpointPool struct {
	mu  sync.Mutex
	eps []relayEndpoint

	// buckets is a list of endpoint-index slices, one slice per
	// distinct `account` label. Endpoints with empty account collapse
	// into a single "<unlabeled>" bucket. Bucket order is stable
	// across pool lifetime (insertion order from first occurrence of
	// each account).
	buckets [][]int

	// nextBucket rotates buckets at the outer level.
	nextBucket int
	// nextInBucket tracks the per-bucket round-robin cursor; one entry
	// per bucket, parallel to `buckets`.
	nextInBucket []int
}

// newEndpointPool constructs the pool from the operator's script_keys
// (and optional script_accounts in parallel). At least one entry is
// required.
func newEndpointPool(scriptKeys, scriptAccounts []string) *endpointPool {
	return newEndpointPoolWithURLs(scriptKeys, scriptAccounts, nil)
}

// newEndpointPoolWithURLs is the test-friendly constructor: when
// scriptURLs is non-nil and parallel to scriptKeys, the URLs are used
// verbatim instead of being built from the deployment IDs. Production
// callers pass nil for scriptURLs.
func newEndpointPoolWithURLs(scriptKeys, scriptAccounts, scriptURLs []string) *endpointPool {
	eps := make([]relayEndpoint, len(scriptKeys))
	// Group endpoints by account label, preserving insertion order.
	bucketIdx := make(map[string]int)
	var buckets [][]int
	for i, key := range scriptKeys {
		account := ""
		if i < len(scriptAccounts) {
			account = scriptAccounts[i]
		}
		var url string
		if i < len(scriptURLs) && scriptURLs[i] != "" {
			url = scriptURLs[i]
		} else {
			url = buildScriptURL(key)
		}
		eps[i] = relayEndpoint{
			url:     url,
			account: account,
		}
		bk := bucketKey(account)
		bi, ok := bucketIdx[bk]
		if !ok {
			bi = len(buckets)
			bucketIdx[bk] = bi
			buckets = append(buckets, nil)
		}
		buckets[bi] = append(buckets[bi], i)
	}
	return &endpointPool{
		eps:          eps,
		buckets:      buckets,
		nextInBucket: make([]int, len(buckets)),
	}
}

// bucketKey normalizes an account label for grouping. Empty/unlabeled
// endpoints all collapse into the same anonymous bucket.
func bucketKey(account string) string {
	if account == "" {
		return "<unlabeled>"
	}
	return account
}

// bucketOf returns the bucket index containing endpoint idx, or -1 if
// idx is out of range. Caller must hold p.mu.
func (p *endpointPool) bucketOfLocked(idx int) int {
	for bi, b := range p.buckets {
		for _, ei := range b {
			if ei == idx {
				return bi
			}
		}
	}
	return -1
}

// pick returns the next live endpoint by index, rotating buckets at
// the outer level so quota draw spreads evenly across operator
// Google accounts. Within a bucket, deployments round-robin and
// skip-on-blacklist.
//
// Returns -1 if every endpoint in every bucket is blacklisted
// (caller should still retry with an arbitrary one rather than hang
// the request — better an attempt than a stall).
//
// Caller must hold no other locks; pick is short and grabs only mu.
func (p *endpointPool) pick(now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.eps) == 0 || len(p.buckets) == 0 {
		return -1
	}
	// Try every bucket at most once, starting from nextBucket.
	for b := 0; b < len(p.buckets); b++ {
		bi := (p.nextBucket + b) % len(p.buckets)
		bucket := p.buckets[bi]
		if len(bucket) == 0 {
			continue
		}
		// Within this bucket, try every endpoint at most once.
		startIdx := p.nextInBucket[bi]
		for j := 0; j < len(bucket); j++ {
			pos := (startIdx + j) % len(bucket)
			idx := bucket[pos]
			if now.Before(p.eps[idx].blacklistedTill) {
				continue
			}
			// Advance both cursors so the next pick rotates onward.
			p.nextInBucket[bi] = (pos + 1) % len(bucket)
			p.nextBucket = (bi + 1) % len(p.buckets)
			return idx
		}
	}
	// All endpoints in all buckets blacklisted.
	return -1
}

// pickFallback returns an alternate endpoint after `excludeIdx` for
// failover (Workstream A9 invariant #3 caps to one failover per
// batch).
//
// **Bucket-aware:** prefers another endpoint in the SAME bucket as
// excludeIdx (same Google account → same quota cap, same per-account
// rate limits, so retrying in-bucket has the most chance of success).
// Falls through to other buckets only when the primary's bucket has
// no other live endpoint.
//
// Returns -1 if no other live endpoint exists fleet-wide.
func (p *endpointPool) pickFallback(excludeIdx int, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.eps) <= 1 {
		return -1
	}
	// 1) Same-bucket fallback first.
	if bi := p.bucketOfLocked(excludeIdx); bi >= 0 {
		bucket := p.buckets[bi]
		for _, idx := range bucket {
			if idx == excludeIdx {
				continue
			}
			if now.Before(p.eps[idx].blacklistedTill) {
				continue
			}
			return idx
		}
	}
	// 2) Cross-bucket fallback: try every other bucket in order.
	for b := 0; b < len(p.buckets); b++ {
		for _, idx := range p.buckets[b] {
			if idx == excludeIdx {
				continue
			}
			if now.Before(p.eps[idx].blacklistedTill) {
				continue
			}
			return idx
		}
	}
	return -1
}

// recordSuccess clears the consecutive-failure counter for an endpoint.
func (p *endpointPool) recordSuccess(idx int) {
	if idx < 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx >= len(p.eps) {
		return
	}
	p.eps[idx].failCount = 0
	p.eps[idx].blacklistedTill = time.Time{}
}

// recordFailure backs an endpoint off after a transport-level or HTTP-error
// response. quotaErr=true indicates the failure looked like Apps Script
// quota exhaustion and switches to the long-window TTL; otherwise the
// failure count drives an exponential schedule.
func (p *endpointPool) recordFailure(idx int, now time.Time, quotaErr bool) {
	if idx < 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx >= len(p.eps) {
		return
	}
	ep := &p.eps[idx]
	ep.failCount++
	if quotaErr {
		ep.blacklistedTill = now.Add(quotaBlacklistTTL)
		return
	}
	ttl := endpointBlacklistBaseTTL
	for i := 1; i < ep.failCount && ttl < endpointBlacklistMaxTTL; i++ {
		ttl *= 2
	}
	if ttl > endpointBlacklistMaxTTL {
		ttl = endpointBlacklistMaxTTL
	}
	ep.blacklistedTill = now.Add(ttl)
}

// urlAt returns the URL for the endpoint at idx, or "" if out of range.
// Used by the hot-path Roundtrip to avoid taking the mutex when it
// already has the index.
func (p *endpointPool) urlAt(idx int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.eps) {
		return ""
	}
	return p.eps[idx].url
}

// snapshot returns a defensive copy of every endpoint's read-only state
// for stats / diagnostics. Done off the request path.
func (p *endpointPool) snapshot() []relayEndpoint {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]relayEndpoint, len(p.eps))
	copy(out, p.eps)
	return out
}

// urls returns just the URL list, used by the quota loop to know what
// to GET.
func (p *endpointPool) urls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.eps))
	for i := range p.eps {
		out[i] = p.eps[i].url
	}
	return out
}

// buildScriptURL constructs the Apps Script web-app URL from a deployment
// ID. The plan locks this in: in appsscript mode, the operator does NOT
// set server.url — the URL is derived from script_keys here.
func buildScriptURL(deploymentID string) string {
	return "https://script.google.com/macros/s/" + deploymentID + "/exec"
}
