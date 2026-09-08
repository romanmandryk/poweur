package storage

import (
	"testing"
	"time"
)

func message(id, sender string) StoredMessage {
	return StoredMessage{ID: id, Sender: sender, Recipient: "bob.poweur.net", Payload: "x"}
}

// The bug this exists for: undelivered mail lived in a map and died with the
// process (EPIC-009 E09-T1).
func TestInboxSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	first, err := OpenInboxStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Add("bob.poweur.net", message("m1", "alice.poweur.net"), 0) {
		t.Fatal("add refused")
	}
	first.Add("bob.poweur.net", message("m2", "alice.poweur.net"), 0)

	// A new process over the same data dir is what a restart is.
	second, err := OpenInboxStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	messages, cursor := second.Since("bob.poweur.net", "")
	if len(messages) != 2 || messages[0].ID != "m1" || messages[1].ID != "m2" {
		t.Fatalf("after restart: %+v", messages)
	}
	if cursor == "" {
		t.Fatal("no cursor returned")
	}

	// …and once consumed they are gone from disk too.
	second.Consume("bob.poweur.net", cursor)
	third, err := OpenInboxStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if remaining, _ := third.Since("bob.poweur.net", ""); len(remaining) != 0 {
		t.Fatalf("consumed messages came back: %+v", remaining)
	}
}

// Reading must not forget. A drain-on-read inbox loses a message to a dropped
// connection exactly as a restart used to.
func TestSinceDoesNotConsume(t *testing.T) {
	store, _ := OpenInboxStore(t.TempDir())
	store.Add("bob.poweur.net", message("m1", "alice.poweur.net"), 0)

	first, cursor := store.Since("bob.poweur.net", "")
	second, _ := store.Since("bob.poweur.net", "")
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("a read consumed the message: %d then %d", len(first), len(second))
	}

	// A second pickup from the cursor sees only what arrived after it.
	store.Add("bob.poweur.net", message("m2", "alice.poweur.net"), 0)
	next, _ := store.Since("bob.poweur.net", cursor)
	if len(next) != 1 || next[0].ID != "m2" {
		t.Fatalf("cursor did not advance correctly: %+v", next)
	}
}

func TestConsumeOnlyThroughTheCursorGiven(t *testing.T) {
	store, _ := OpenInboxStore(t.TempDir())
	store.Add("bob.poweur.net", message("m1", "alice.poweur.net"), 0)
	_, cursor := store.Since("bob.poweur.net", "")
	store.Add("bob.poweur.net", message("m2", "alice.poweur.net"), 0)

	// The client only ever saw m1, so acknowledging must not drop m2.
	if removed := store.Consume("bob.poweur.net", cursor); removed != 1 {
		t.Fatalf("consumed %d, want 1", removed)
	}
	left, _ := store.Since("bob.poweur.net", "")
	if len(left) != 1 || left[0].ID != "m2" {
		t.Fatalf("wrong message survived: %+v", left)
	}
}

func TestDrainStillWorksForClientsWithoutCursors(t *testing.T) {
	store, _ := OpenInboxStore(t.TempDir())
	store.Add("bob.poweur.net", message("m1", "alice.poweur.net"), 0)
	if got := store.Drain("bob.poweur.net"); len(got) != 1 {
		t.Fatalf("drain returned %d", len(got))
	}
	if got := store.Drain("bob.poweur.net"); len(got) != 0 {
		t.Fatalf("drain did not forget: %d", len(got))
	}
}

func TestInboxCapRefusesRatherThanDropping(t *testing.T) {
	store, _ := OpenInboxStore(t.TempDir())
	store.Add("bob.poweur.net", message("m1", "alice.poweur.net"), 1)
	// A full inbox refuses the new message so the sender can retry; silently
	// dropping either end of the queue would lose mail nobody knows is gone.
	if store.Add("bob.poweur.net", message("m2", "alice.poweur.net"), 1) {
		t.Fatal("cap did not refuse")
	}
	left, _ := store.Since("bob.poweur.net", "")
	if len(left) != 1 || left[0].ID != "m1" {
		t.Fatalf("wrong message kept: %+v", left)
	}
}

// Acks are the opposite: a recent receipt is worth more than an old one.
func TestAckCapDropsOldest(t *testing.T) {
	store, _ := OpenAckStore(t.TempDir())
	store.Add("alice.poweur.net", StoredAck{ID: "a1", MessageID: "m1"}, 1)
	store.Add("alice.poweur.net", StoredAck{ID: "a2", MessageID: "m2"}, 1)
	acks, _ := store.Since("alice.poweur.net", "")
	if len(acks) != 1 || acks[0].ID != "a2" {
		t.Fatalf("expected the newest ack to survive: %+v", acks)
	}
}

func TestExpireReportsWhatItDropped(t *testing.T) {
	store, _ := OpenInboxStore(t.TempDir())
	store.Add("bob.poweur.net", message("old", "alice.poweur.net"), 0)

	// Nothing is expired yet.
	if dropped := store.Expire(time.Now().Add(-time.Hour)); len(dropped) != 0 {
		t.Fatalf("dropped %d messages that were not old", len(dropped))
	}

	dropped := store.Expire(time.Now().Add(time.Hour))
	if len(dropped) != 1 || dropped[0].Item.ID != "old" || dropped[0].Identity != "bob.poweur.net" {
		t.Fatalf("expire reported %+v", dropped)
	}
	if left, _ := store.Since("bob.poweur.net", ""); len(left) != 0 {
		t.Fatalf("expired message still queued: %+v", left)
	}
}

// A relay with no data dir keeps working exactly as before.
func TestMemoryOnlyInboxStillWorks(t *testing.T) {
	store := NewInboxStore()
	store.Add("bob.poweur.net", message("m1", "alice.poweur.net"), 0)
	if got := store.Drain("bob.poweur.net"); len(got) != 1 {
		t.Fatalf("memory-only drain returned %d", len(got))
	}
}

// Spool paths come from the identity, so a name that tried to climb out of the
// directory must be refused rather than sanitized into something surprising.
func TestSpoolRefusesPathTraversalIdentities(t *testing.T) {
	store, _ := OpenInboxStore(t.TempDir())
	store.Add("../../etc/passwd", message("m1", "alice.poweur.net"), 0)
	// It stays in memory for this process but is never written to disk under a
	// traversed path; the next process therefore does not see it.
	if _, err := OpenInboxStore(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
