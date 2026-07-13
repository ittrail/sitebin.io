package auth

import (
	"sync"
	"time"
)

// VerifyCache remembers recently verified credentials so hot paths (WebDAV
// sends Basic auth on every request) skip the expensive Argon2id derivation.
// Keys should bind the credential to its target, e.g. editID+":"+sha256(pw).
type VerifyCache struct {
	Now func() time.Time

	ttl time.Duration
	mu  sync.Mutex
	m   map[string]time.Time // key -> expiry
}

func NewVerifyCache(ttl time.Duration) *VerifyCache {
	return &VerifyCache{Now: time.Now, ttl: ttl, m: make(map[string]time.Time)}
}

func (c *VerifyCache) Check(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.m[key]
	if !ok {
		return false
	}
	if c.Now().After(exp) {
		delete(c.m, key)
		return false
	}
	return true
}

func (c *VerifyCache) Put(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if len(c.m) > 4096 { // bound memory; prune expired entries
		for k, exp := range c.m {
			if now.After(exp) {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = now.Add(c.ttl)
}

// Drop forgets every cached credential for keys with the given prefix. Used
// when a site's password changes or the site is deleted.
func (c *VerifyCache) Drop(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.m, k)
		}
	}
}
