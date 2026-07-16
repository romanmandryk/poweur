package identity

import (
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
