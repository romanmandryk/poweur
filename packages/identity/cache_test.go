package identity

import (
	"fmt"
	"testing"
	"time"
)

func TestCacheTTL(t *testing.T) {
	c := NewCache()
	r := Result{Source: SourceWeb, Document: IdentityDocument{Identity: "a.poweur.net"}}
	c.Put("a.poweur.net", r, 50*time.Millisecond)
	if got, ok := c.Get("a.poweur.net"); !ok || got.Document.Identity != "a.poweur.net" {
		t.Fatalf("expected cache hit, got ok=%v %#v", ok, got)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("a.poweur.net"); ok {
		t.Fatal("expected cache miss after TTL")
	}
}

func TestCache_StaysBounded(t *testing.T) {
	c := NewCache()
	for i := 0; i < maxCacheEntries+500; i++ {
		c.Put(fmt.Sprintf("n%d.example.org", i), Result{}, time.Minute)
	}
	c.mu.RLock()
	size := len(c.items)
	c.mu.RUnlock()
	if size > maxCacheEntries {
		t.Fatalf("cache holds %d entries, cap is %d", size, maxCacheEntries)
	}
	// The newest entry is always kept.
	if _, ok := c.Get(fmt.Sprintf("n%d.example.org", maxCacheEntries+499)); !ok {
		t.Fatal("entry just stored is missing")
	}
}
