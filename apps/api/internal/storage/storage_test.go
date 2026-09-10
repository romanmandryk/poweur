package storage

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestInboxAddDrain(t *testing.T) {
	s := NewInboxStore()
	s.Add("a", StoredMessage{ID: "1", Payload: "p"}, 50)
	if x := s.Drain("a"); len(x) != 1 {
		t.Fatalf("drain1: %d", len(x))
	}
	if x := s.Drain("a"); len(x) != 0 {
		t.Fatalf("drain2 should empty: %d", len(x))
	}
}

func TestAckAddDrain(t *testing.T) {
	s := NewAckStore()
	s.Add("u", StoredAck{ID: "a1", MessageID: "m1"}, 50)
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
		ID:             "s1",
		Identity:       "a.test",
		PublicKey:      "k",
		PublicKeyBytes: pub,
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Hour),
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
		ID:             "s2",
		Identity:       "a.test",
		PublicKey:      "k",
		PublicKeyBytes: pub,
		IssuedAt:       now,
		ExpiresAt:      now.Add(-time.Second),
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
		ID:             "sx",
		Identity:       "a.test",
		PublicKey:      "k",
		PublicKeyBytes: pub,
		IssuedAt:       now,
		ExpiresAt:      now.Add(-time.Hour),
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

// DeleteMatching backs device revocation (EPIC-004 E04-T6): a revocation
// names a device, but sessions are keyed by id, so the store has to be able
// to sweep an identity's sessions by predicate.
func TestSessionStoreDeleteMatching(t *testing.T) {
	s := NewSessionStore()
	now := time.Now().UTC()
	put := func(id, identity, fingerprint string) {
		s.Put(Session{
			ID: id, Identity: identity, DeviceFingerprint: fingerprint,
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		})
	}
	put("s1", "alice.example.org", "laptop")
	put("s2", "alice.example.org", "laptop")
	put("s3", "alice.example.org", "phone")
	// Same identity, different casing — a session carries whatever spelling
	// its registration used, so the match has to fold case or a revocation
	// silently misses half the sessions.
	put("s4", "ALICE.example.org", "laptop")
	put("s5", "bob.example.org", "laptop")

	removed := s.DeleteMatching("alice.example.org", func(sess Session) bool {
		return sess.DeviceFingerprint == "laptop"
	})
	if removed != 3 {
		t.Fatalf("removed %d, want 3", removed)
	}
	for _, gone := range []string{"s1", "s2", "s4"} {
		if _, ok := s.Get(gone); ok {
			t.Fatalf("%s survived", gone)
		}
	}
	for _, kept := range []string{"s3", "s5"} {
		if _, ok := s.Get(kept); !ok {
			t.Fatalf("%s was taken", kept)
		}
	}
	// The per-identity index has to shrink with it, or ListForIdentity
	// keeps naming sessions that no longer exist.
	if got := s.ListForIdentity("alice.example.org"); len(got) != 1 || got[0].ID != "s3" {
		t.Fatalf("ListForIdentity after sweep: %+v", got)
	}
	if got := s.ListForIdentity("bob.example.org"); len(got) != 1 {
		t.Fatalf("bob lost a session: %+v", got)
	}
	// A second sweep is a no-op, and an unknown identity removes nothing.
	if n := s.DeleteMatching("alice.example.org", func(Session) bool { return false }); n != 0 {
		t.Fatalf("predicate that matches nothing removed %d", n)
	}
	if n := s.DeleteMatching("nobody.example.org", func(Session) bool { return true }); n != 0 {
		t.Fatalf("unknown identity removed %d", n)
	}
}
