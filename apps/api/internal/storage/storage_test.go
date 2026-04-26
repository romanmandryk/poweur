package storage

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestInboxAddDrain(t *testing.T) {
	s := NewInboxStore()
	s.Add("a", StoredMessage{ID: "1", Payload: "p"})
	if x := s.Drain("a"); len(x) != 1 {
		t.Fatalf("drain1: %d", len(x))
	}
	if x := s.Drain("a"); len(x) != 0 {
		t.Fatalf("drain2 should empty: %d", len(x))
	}
}

func TestAckAddDrain(t *testing.T) {
	s := NewAckStore()
	s.Add("u", StoredAck{ID: "a1", MessageID: "m1"})
	got := s.Drain("u")
	if len(got) != 1 {
		t.Fatal(len(got))
	}
	if len(s.Drain("u")) != 0 {
		t.Fatal("second drain")
	}
}

func TestChallengeStore(t *testing.T) {
	s := NewChallengeStore()
	s.Issue("id", "tok", time.Now().Add(time.Minute))
	c, ok := s.Consume("id")
	if !ok || c.Value != "tok" {
		t.Fatal(ok)
	}
	if _, ok := s.Consume("id"); ok {
		t.Fatal("second consume should fail")
	}
}

func TestChallengeStoreExpired(t *testing.T) {
	s := NewChallengeStore()
	s.Issue("id", "tok", time.Now().Add(-time.Second))
	if _, ok := s.Consume("id"); ok {
		t.Fatal("expired should not consume")
	}
}

func TestSessionStoreGetDeleteListPrune(t *testing.T) {
	s := NewSessionStore()
	pub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now().UTC()
	s.Put(Session{
		ID:         "s1",
		Identity:   "a.test",
		PublicKey:  "k",
		PublicKeyBytes: pub,
		IssuedAt:   now,
		ExpiresAt:  now.Add(time.Hour),
	})
	if s2, ok := s.Get("s1"); !ok || s2.ID != "s1" {
		t.Fatal("get")
	}
	if lst := s.ListForIdentity("a.test"); len(lst) != 1 {
		t.Fatal("list", len(lst))
	}
	s.Delete("s1")
	if _, ok := s.Get("s1"); ok {
		t.Fatal("delete")
	}
}

func TestSessionStoreExpiredGet(t *testing.T) {
	s := NewSessionStore()
	pub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now().UTC()
	s.Put(Session{
		ID:            "s2",
		Identity:      "a.test",
		PublicKey:     "k",
		PublicKeyBytes: pub,
		IssuedAt:      now,
		ExpiresAt:     now.Add(-time.Second),
	})
	if _, ok := s.Get("s2"); ok {
		t.Fatal("expired session should be gone")
	}
}

func TestSessionStorePrune(t *testing.T) {
	s := NewSessionStore()
	pub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now().UTC()
	s.Put(Session{
		ID:            "sx",
		Identity:      "a.test",
		PublicKey:     "k",
		PublicKeyBytes: pub,
		IssuedAt:      now,
		ExpiresAt:     now.Add(-time.Hour),
	})
	s.Prune()
	if len(s.sessions) != 0 {
		t.Fatal("prune", len(s.sessions))
	}
}

func TestIdentityStore(t *testing.T) {
	s := NewIdentityStore()
	pub, _, _ := ed25519.GenerateKey(nil)
	if !s.Add(Identity{Identity: "a", PublicKey: "p", PublicKeyBytes: pub, CreatedAt: time.Now()}) {
		t.Fatal("first add")
	}
	if s.Add(Identity{Identity: "a", PublicKey: "p2", PublicKeyBytes: pub, CreatedAt: time.Now()}) {
		t.Fatal("duplicate add should fail")
	}
	if !s.Exists("a") {
		t.Fatal("exists")
	}
	if _, ok := s.Get("x"); ok {
		t.Fatal("get missing")
	}
}
