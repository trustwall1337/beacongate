package upstream

import (
	"sync"
	"time"
)

// DNSCache is a tiny TTL cache so repeated session opens for the same host
// don't hit the resolver. It is intentionally minimal and is not a substitute
// for a real DNS resolver.
type DNSCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]dnsEntry
}

type dnsEntry struct {
	ip     string
	expiry time.Time
}

func NewDNSCache(ttl time.Duration) *DNSCache {
	return &DNSCache{ttl: ttl, entries: map[string]dnsEntry{}}
}

func (c *DNSCache) Lookup(host string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[host]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiry) {
		delete(c.entries, host)
		return "", false
	}
	return e.ip, true
}

func (c *DNSCache) Set(host, ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[host] = dnsEntry{ip: ip, expiry: time.Now().Add(c.ttl)}
}
