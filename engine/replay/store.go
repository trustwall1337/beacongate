// Package replay implements the BeaconGate v1.1 server-side replay
// dedup store. It pairs with engine/crypto's per-batch replay-id and
// timestamp (plan B1) to make the wire idempotent under benign retry
// while rejecting actual on-wire replays.
//
// Two-tier design (plan B4 + B5):
//
//   - **Response cache** (per client, byte-budget LRU, 60s TTL):
//     holds the full response bytes for the most recent N batches.
//     A re-arrival of the same replay-id within 60s returns the
//     cached response without re-processing — this is what makes
//     the appsscript transport's per-batch failover idempotent.
//
//   - **Dedup ring buffer** (per client, fixed cap, 10min TTL):
//     remembers which replay-ids have been seen within the last
//     10 minutes. A re-arrival outside the response-cache window
//     but inside the dedup window is rejected as REPLAYED. The
//     ring is sized so a legitimate client at the per-IP rate cap
//     cannot fill it before TTL; if it ever did, eviction would
//     create a silent replay-protection gap, so the store
//     instead returns RATE_PRESSURE when a new entry would force
//     pre-TTL eviction.
//
// Workstream A9 invariant relevance: hot path. Operations on the
// store must be O(1)-ish and bounded under per-client mutex (no
// global lock that all tunnel workers contend on).
package replay

import (
	"container/list"
	"errors"
	"sync"
	"time"
)

// Decision is what Check returns. The server tunnel handler dispatches
// on it.
type Decision int

const (
	// Accept means this is a fresh batch — process it as new. The
	// caller MUST eventually call RecordResponse so a subsequent
	// retry with the same replay-id returns the cached bytes
	// instead of double-processing.
	Accept Decision = iota
	// DuplicateProcessed means a replay-id we have a cached
	// response for arrived again. The caller returns Check's
	// returned response bytes verbatim — no reprocessing, no
	// state mutation.
	DuplicateProcessed
	// Replayed means a replay-id we previously saw is back, but the
	// response cache window expired before this retry arrived. We
	// can't return the original response and we won't re-process,
	// so the caller MUST reject the request (HTTP 400 with code
	// REPLAYED).
	Replayed
	// StaleTimestamp means the inner timestamp is outside the
	// configured ±skew window. Likely either a replay past the
	// dedup window or a misconfigured client clock.
	StaleTimestamp
	// RatePressure means the dedup ring would have to evict an
	// un-expired entry to admit this one. Treated as a violation
	// of the per-IP rate cap that the caller (the tunnel handler)
	// also enforces; the store fails closed rather than silently
	// shrinking the replay window.
	RatePressure
)

func (d Decision) String() string {
	switch d {
	case Accept:
		return "ACCEPT"
	case DuplicateProcessed:
		return "DUPLICATE_PROCESSED"
	case Replayed:
		return "REPLAYED"
	case StaleTimestamp:
		return "STALE_TIMESTAMP"
	case RatePressure:
		return "RATE_PRESSURE"
	}
	return "UNKNOWN"
}

// Config controls the store's per-client sizing.
//
// The defaults are sized for plan A7 #11 (50 req/s/IP rate cap):
//
//	dedup_cap = window × rate × headroom = 600 × 50 × 2 = 60_000
//
// The response cache is sized in bytes rather than entries because
// response bodies vary widely (small ACKs vs full long-poll drains).
type Config struct {
	// SkewMax is the absolute clock-skew tolerance for the inner
	// timestamp. ±5min by default.
	SkewMax time.Duration
	// ResponseTTL is how long a replay-id's response stays in the
	// response cache. 60s by default — long enough for benign
	// transport-level failover (plan A2), short enough to keep
	// memory bounded.
	ResponseTTL time.Duration
	// DedupTTL is how long a replay-id stays in the dedup ring
	// after its response cache expires. 10min by default.
	DedupTTL time.Duration
	// DedupCapPerClient is the fixed ring-buffer size per client.
	// 60_000 by default; eviction-pressure errors RATE_PRESSURE.
	DedupCapPerClient int
	// ResponseBudgetBytesPerClient bounds the response cache by
	// total byte volume. 32 MiB by default.
	ResponseBudgetBytesPerClient int
}

// Defaults returns the production-sized config.
func Defaults() Config {
	return Config{
		SkewMax:                      5 * time.Minute,
		ResponseTTL:                  60 * time.Second,
		DedupTTL:                     10 * time.Minute,
		DedupCapPerClient:            60_000,
		ResponseBudgetBytesPerClient: 32 * 1024 * 1024,
	}
}

// Store holds per-client replay state. Safe for concurrent use.
type Store struct {
	cfg Config

	mu     sync.Mutex
	shards map[string]*clientShard
}

// New constructs a store with the given config. Zero-valued fields
// in cfg are filled from Defaults().
func New(cfg Config) *Store {
	def := Defaults()
	if cfg.SkewMax == 0 {
		cfg.SkewMax = def.SkewMax
	}
	if cfg.ResponseTTL == 0 {
		cfg.ResponseTTL = def.ResponseTTL
	}
	if cfg.DedupTTL == 0 {
		cfg.DedupTTL = def.DedupTTL
	}
	if cfg.DedupCapPerClient == 0 {
		cfg.DedupCapPerClient = def.DedupCapPerClient
	}
	if cfg.ResponseBudgetBytesPerClient == 0 {
		cfg.ResponseBudgetBytesPerClient = def.ResponseBudgetBytesPerClient
	}
	return &Store{
		cfg:    cfg,
		shards: map[string]*clientShard{},
	}
}

// Check decides what to do with an inbound replay-id. now is the
// current wall clock; ts is the inner timestamp recovered from the
// AEAD plaintext. Pass an empty cachedResponse when Decision is not
// DuplicateProcessed.
func (s *Store) Check(clientID string, replayID [16]byte, ts, now time.Time) (Decision, []byte) {
	if skew := now.Sub(ts); skew > s.cfg.SkewMax || skew < -s.cfg.SkewMax {
		return StaleTimestamp, nil
	}
	shard := s.shardFor(clientID)
	return shard.check(replayID, ts, now, s.cfg)
}

// RecordResponse stores response bytes for replayID so a subsequent
// retry within the response-cache window returns them verbatim.
// MUST be called by every Accept path. Calling it for a replay-id
// the store does not currently treat as Accept'd is a no-op.
func (s *Store) RecordResponse(clientID string, replayID [16]byte, response []byte, now time.Time) {
	shard := s.shardFor(clientID)
	shard.recordResponse(replayID, response, now, s.cfg)
}

// Forget drops all state for a client (useful when a client_id is
// known to have been retired). Safe to call concurrently.
func (s *Store) Forget(clientID string) {
	s.mu.Lock()
	delete(s.shards, clientID)
	s.mu.Unlock()
}

func (s *Store) shardFor(clientID string) *clientShard {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.shards[clientID]; ok {
		return sh
	}
	sh := newClientShard(s.cfg.DedupCapPerClient)
	s.shards[clientID] = sh
	return sh
}

// --- per-client shard ---

type ringEntry struct {
	replayID [16]byte
	addedAt  time.Time
}

type responseEntry struct {
	bytes    []byte
	expires  time.Time
	lruElem  *list.Element
	replayID [16]byte
}

type clientShard struct {
	mu sync.Mutex

	dedupRing []ringEntry
	dedupIdx  map[[16]byte]int // replay-id → ring index for O(1) lookup
	ringHead  int              // next slot to overwrite
	ringSize  int              // entries currently in ring

	responseCache map[[16]byte]*responseEntry
	responseLRU   *list.List
	responseBytes int
}

func newClientShard(cap int) *clientShard {
	return &clientShard{
		dedupRing:     make([]ringEntry, cap),
		dedupIdx:      make(map[[16]byte]int, cap),
		responseCache: make(map[[16]byte]*responseEntry),
		responseLRU:   list.New(),
	}
}

func (c *clientShard) check(replayID [16]byte, _, now time.Time, cfg Config) (Decision, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.evictExpiredResponseLocked(now, cfg)

	// 1) Already in response cache? Return cached bytes (idempotent retry).
	if entry, ok := c.responseCache[replayID]; ok {
		// Bump LRU position so a chatty retrier keeps the entry warm.
		c.responseLRU.MoveToFront(entry.lruElem)
		return DuplicateProcessed, entry.bytes
	}

	// 2) In dedup ring (response cache expired) → REPLAYED.
	if idx, ok := c.dedupIdx[replayID]; ok {
		entry := c.dedupRing[idx]
		// Verify the ring slot still belongs to this replay-id (the
		// ring may have been overwritten if we ever ran past cap;
		// this is defense-in-depth).
		if entry.replayID == replayID && now.Sub(entry.addedAt) <= cfg.DedupTTL {
			return Replayed, nil
		}
		delete(c.dedupIdx, replayID)
	}

	// 3) Not seen — accept. Insert into ring.
	if !c.insertRingLocked(replayID, now, cfg) {
		return RatePressure, nil
	}
	return Accept, nil
}

// insertRingLocked appends replayID to the dedup ring. Returns false
// if doing so would overwrite an entry whose TTL has not yet expired
// (cache pressure; caller must reject).
func (c *clientShard) insertRingLocked(replayID [16]byte, now time.Time, cfg Config) bool {
	cap := len(c.dedupRing)
	if c.ringSize < cap {
		// Ring not full yet — append at the head slot.
		c.dedupRing[c.ringHead] = ringEntry{replayID: replayID, addedAt: now}
		c.dedupIdx[replayID] = c.ringHead
		c.ringHead = (c.ringHead + 1) % cap
		c.ringSize++
		return true
	}
	// Ring full — overwriting the oldest entry. If that entry is
	// still within TTL, this is a rate-pressure violation: the
	// client has produced more replay-ids in less than DedupTTL
	// than the ring can hold.
	victim := c.dedupRing[c.ringHead]
	if !victim.addedAt.IsZero() && now.Sub(victim.addedAt) <= cfg.DedupTTL {
		return false
	}
	delete(c.dedupIdx, victim.replayID)
	c.dedupRing[c.ringHead] = ringEntry{replayID: replayID, addedAt: now}
	c.dedupIdx[replayID] = c.ringHead
	c.ringHead = (c.ringHead + 1) % cap
	return true
}

func (c *clientShard) recordResponse(replayID [16]byte, response []byte, now time.Time, cfg Config) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.evictExpiredResponseLocked(now, cfg)

	// Defensive copy: the caller's buffer may be reused.
	cp := make([]byte, len(response))
	copy(cp, response)

	// Evict by byte budget if needed.
	for c.responseBytes+len(cp) > cfg.ResponseBudgetBytesPerClient && c.responseLRU.Len() > 0 {
		oldest := c.responseLRU.Back()
		if oldest == nil {
			break
		}
		old := oldest.Value.(*responseEntry)
		c.responseBytes -= len(old.bytes)
		delete(c.responseCache, old.replayID)
		c.responseLRU.Remove(oldest)
	}

	entry := &responseEntry{
		bytes:    cp,
		expires:  now.Add(cfg.ResponseTTL),
		replayID: replayID,
	}
	entry.lruElem = c.responseLRU.PushFront(entry)
	c.responseCache[replayID] = entry
	c.responseBytes += len(cp)

	// Note: the dedup ring entry was added by Check on the Accept
	// path. We don't insert here so a misbehaving caller that calls
	// RecordResponse without a prior Check won't pollute the ring.
}

func (c *clientShard) evictExpiredResponseLocked(now time.Time, _ Config) {
	for c.responseLRU.Len() > 0 {
		back := c.responseLRU.Back()
		if back == nil {
			return
		}
		entry := back.Value.(*responseEntry)
		if entry.expires.After(now) {
			return
		}
		c.responseBytes -= len(entry.bytes)
		delete(c.responseCache, entry.replayID)
		c.responseLRU.Remove(back)
	}
}

// --- public errors for callers that prefer error sentinels over Decision values ---

var (
	// ErrReplayed is the sentinel for the Replayed Decision.
	ErrReplayed = errors.New("replay: batch already processed and response window expired")
	// ErrStaleTimestamp is the sentinel for the StaleTimestamp Decision.
	ErrStaleTimestamp = errors.New("replay: timestamp outside skew window")
	// ErrRatePressure is the sentinel for the RatePressure Decision.
	ErrRatePressure = errors.New("replay: dedup cache pressure (request rate exceeds configured cap)")
)
