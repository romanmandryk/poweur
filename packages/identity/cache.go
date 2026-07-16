package identity

import (
	"sync"
	"time"
)

// Cache is a simple in-memory TTL cache for Resolve results (honors a caller-
// supplied TTL, typically derived from Cache-Control max-age).
type Cache struct {
	mu    sync.RWMutex
	items map[string]cacheEntry
}

type cacheEntry struct {
	result    Result
	expiresAt time.Time
}

// NewCache returns an empty resolver cache.
func NewCache() *Cache {
	return &Cache{items: make(map[string]cacheEntry)}
}

// Get returns a cached result if present and not expired.
func (c *Cache) Get(identity string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[identity]
	if !ok || time.Now().After(e.expiresAt) {
		return Result{}, false
	}
	return e.result, true
}

// Put stores a result until now+ttl. Zero/negative ttl skips caching.
func (c *Cache) Put(identity string, result Result, ttl time.Duration) {
	if c == nil || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[identity] = cacheEntry{result: result, expiresAt: time.Now().Add(ttl)}
}

// Invalidate drops a cached identity.
func (c *Cache) Invalidate(identity string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, identity)
}
