package storage

import (
	"testing"
	"time"
)

// The constraint this replaced: one outstanding challenge per identity meant a
// client with a push stream and a poll in flight at once invalidated its own
// authentication at random (EPIC-009 E09-T2).
func TestConcurrentChallengesEachSpendTheirOwn(t *testing.T) {
	store := NewChallengeStore()
	expiry := time.Now().Add(time.Minute)
	store.Issue("bob.poweur.net", "one", expiry)
	store.Issue("bob.poweur.net", "two", expiry)

	first, ok := store.ConsumeValue("bob.poweur.net", "one")
	if !ok || first.Value != "one" {
		t.Fatalf("first challenge not honoured: %+v ok=%v", first, ok)
	}
	second, ok := store.ConsumeValue("bob.poweur.net", "two")
	if !ok || second.Value != "two" {
		t.Fatal("issuing a second challenge invalidated the first")
	}
}

func TestChallengeIsSingleUse(t *testing.T) {
	store := NewChallengeStore()
	store.Issue("bob.poweur.net", "once", time.Now().Add(time.Minute))
	if _, ok := store.ConsumeValue("bob.poweur.net", "once"); !ok {
		t.Fatal("first use refused")
	}
	if _, ok := store.ConsumeValue("bob.poweur.net", "once"); ok {
		t.Fatal("a spent challenge was accepted again")
	}
}

func TestUnknownAndExpiredChallengesAreRefused(t *testing.T) {
	store := NewChallengeStore()
	store.Issue("bob.poweur.net", "live", time.Now().Add(time.Minute))
	if _, ok := store.ConsumeValue("bob.poweur.net", "never-issued"); ok {
		t.Fatal("a challenge nobody issued was accepted")
	}
	// …and the real one still works: a wrong guess must not spend it.
	if _, ok := store.ConsumeValue("bob.poweur.net", "live"); !ok {
		t.Fatal("a wrong guess invalidated the outstanding challenge")
	}

	store.Issue("bob.poweur.net", "stale", time.Now().Add(-time.Minute))
	if _, ok := store.ConsumeValue("bob.poweur.net", "stale"); ok {
		t.Fatal("an expired challenge was accepted")
	}
}

// Callers that cannot echo a value (a WebAuthn assertion carries it inside
// signed client data) still get the newest one.
func TestConsumeWithoutValueTakesTheNewest(t *testing.T) {
	store := NewChallengeStore()
	store.Issue("bob.poweur.net", "old", time.Now().Add(time.Minute))
	store.Issue("bob.poweur.net", "new", time.Now().Add(time.Minute))
	got, ok := store.Consume("bob.poweur.net")
	if !ok || got.Value != "new" {
		t.Fatalf("Consume returned %+v", got)
	}
}

func TestChallengeQueueIsBounded(t *testing.T) {
	store := NewChallengeStore()
	expiry := time.Now().Add(time.Minute)
	for i := 0; i < maxChallengesPerIdentity*2; i++ {
		store.Issue("bob.poweur.net", string(rune('a'+i%26))+string(rune('0'+i/26)), expiry)
	}
	// An identity that requests challenges in a loop must not grow memory
	// without bound; the oldest fall off.
	if got := len(store.challenges["bob.poweur.net"]); got > maxChallengesPerIdentity {
		t.Fatalf("queue grew to %d", got)
	}
}
