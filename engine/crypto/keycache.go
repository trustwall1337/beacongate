package crypto

import (
	"container/list"
	"crypto/cipher"
	"sync"
)

// perClientKeyCache is a small bounded LRU keyed by client_id, mapping
// to the cipher.AEAD constructed from the HKDF-derived per-client key.
// The cache is intentionally small: under realistic operator load
// every client_id reappears within the LRU window, and an attacker
// flooding distinct client_ids hits a hard upper bound on memory.
//
// Cache misses re-derive at the cost of one HKDF-SHA256 call (~µs)
// plus one AES key-schedule (~µs). Cache hits skip both.
//
// Workstream A9 invariant relevance: this is on the request hot path
// (called from every Seal and every Open) — the cache MUST be
// O(1)-ish and lock-bounded. Per-instance mutex; no global lock.
type perClientKeyCache struct {
	mu      sync.Mutex
	cap     int
	entries map[string]*list.Element
	order   *list.List
}

type cacheEntry struct {
	clientID string
	aead     cipher.AEAD
}

func newPerClientKeyCache(capacity int) *perClientKeyCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &perClientKeyCache{
		cap:     capacity,
		entries: make(map[string]*list.Element, capacity),
		order:   list.New(),
	}
}

// get returns the cached AEAD for clientID and bumps it to the
// MRU end of the LRU. Returns nil on cache miss.
func (c *perClientKeyCache) get(clientID string) cipher.AEAD {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[clientID]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*cacheEntry).aead
	}
	return nil
}

// put stores aead under clientID. If the cache is at capacity,
// the LRU entry is evicted. A subsequent put for an existing
// client_id refreshes the AEAD (e.g. after a key rotation, in
// future).
func (c *perClientKeyCache) put(clientID string, aead cipher.AEAD) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[clientID]; ok {
		elem.Value.(*cacheEntry).aead = aead
		c.order.MoveToFront(elem)
		return
	}
	for c.order.Len() >= c.cap {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*cacheEntry).clientID)
	}
	elem := c.order.PushFront(&cacheEntry{clientID: clientID, aead: aead})
	c.entries[clientID] = elem
}
