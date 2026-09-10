package signin

import (
	"context"
	"sync"
	"time"
)

// NonceCache is the only state a verifier keeps. Sign-In is otherwise
// stateless: no sessions, no tokens, no registration. A verifier must
// remember each accepted nonce until the response it came in would have
// expired anyway — that is at most SignInMaxTTL (5 minutes), which is why
// the TTL cap exists.
//
// Implementations must be safe for concurrent use and must be atomic:
// two concurrent Use calls for the same key must not both return true, or
// the replay guard has a race a determined attacker can drive.
type NonceCache interface {
	// Use claims key until expiresAt. It returns false when the key was
	// already claimed (a replay). An error means the cache could not
	// answer, and the verifier fails closed.
	Use(ctx context.Context, key string, expiresAt time.Time) (bool, error)
}

// MemoryNonceCache is the default single-process cache. A relying party
// running more than one process needs a shared implementation (Redis, a
// unique index in Postgres): a per-process cache lets a response be replayed
// once per process.
type MemoryNonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	// Now is injectable for tests.
	Now func() time.Time
}

// NewMemoryNonceCache returns an empty in-process cache.
func NewMemoryNonceCache() *MemoryNonceCache {
	return &MemoryNonceCache{seen: make(map[string]time.Time)}
}

func (c *MemoryNonceCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// Use implements NonceCache.
func (c *MemoryNonceCache) Use(_ context.Context, key string, expiresAt time.Time) (bool, error) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = make(map[string]time.Time)
	}
	// Opportunistic pruning keeps the map bounded by the request rate over
	// one expiry window without a background goroutine.
	for k, exp := range c.seen {
		if now.After(exp) {
			delete(c.seen, k)
		}
	}
	if exp, ok := c.seen[key]; ok && !now.After(exp) {
		return false, nil
	}
	c.seen[key] = expiresAt
	return true, nil
}

// Len reports how many live entries the cache holds (tests, metrics).
func (c *MemoryNonceCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}
