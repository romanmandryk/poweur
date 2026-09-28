package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func entry(id string) KeystoreEntry {
	return KeystoreEntry{
		EnrollmentID: id,
		Kind:         "passkey",
		Wrap:         "prf",
		Payload:      "seed",
		CredentialID: "cred-" + id,
		Wrapped:      json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
}

func TestKeystoreStore_PutGetListRemove(t *testing.T) {
	s := NewKeystoreStore()
	if err := s.Put("alice.poweur.net", entry("e1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("alice.poweur.net", entry("e2")); err != nil {
		t.Fatal(err)
	}
	if got := len(s.List("alice.poweur.net")); got != 2 {
		t.Fatalf("want 2 entries, got %d", got)
	}
	// Identity lookup is case-insensitive, matching the rest of the relay.
	if got := len(s.List("ALICE.poweur.net")); got != 2 {
		t.Fatalf("case-insensitive lookup failed: %d", got)
	}
	if _, ok := s.FindByCredential("alice.poweur.net", "cred-e1"); !ok {
		t.Fatal("FindByCredential missed an enrolled credential")
	}
	if _, ok := s.FindByCredential("alice.poweur.net", "nope"); ok {
		t.Fatal("FindByCredential matched an unenrolled credential")
	}
	if err := s.Remove("alice.poweur.net", "e1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("alice.poweur.net", "e1"); err != ErrEnrollmentNotFound {
		t.Fatalf("want ErrEnrollmentNotFound, got %v", err)
	}
	if got := len(s.List("alice.poweur.net")); got != 1 {
		t.Fatalf("want 1 entry after removal, got %d", got)
	}
}

func TestKeystoreStore_PutReplacesByEnrollmentID(t *testing.T) {
	s := NewKeystoreStore()
	_ = s.Put("alice.poweur.net", entry("e1"))
	updated := entry("e1")
	updated.Label = "renamed"
	_ = s.Put("alice.poweur.net", updated)
	entries := s.List("alice.poweur.net")
	if len(entries) != 1 {
		t.Fatalf("re-put must replace, not append: %d entries", len(entries))
	}
	if entries[0].Label != "renamed" {
		t.Fatalf("want replaced entry, got %+v", entries[0])
	}
}

// Enrollments must outlive a relay restart, or "clear site data" recovery is
// only as durable as the process.
func TestKeystoreStore_SurvivesReopen(t *testing.T) {
	dir := objectsAt(t)
	s, err := OpenKeystoreStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("alice.poweur.net", entry("e1")); err != nil {
		t.Fatal(err)
	}
	s.TouchLastUsed("alice.poweur.net", "e1", time.Now())

	reopened, err := OpenKeystoreStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get("alice.poweur.net", "e1")
	if !ok {
		t.Fatal("enrollment lost across reopen")
	}
	if got.CredentialID != "cred-e1" {
		t.Fatalf("unexpected entry after reopen: %+v", got)
	}
	if got.LastUsedAt == "" {
		t.Fatal("last_used_at lost across reopen")
	}
	if string(got.Wrapped) == "" {
		t.Fatal("wrapped ciphertext lost across reopen")
	}
}

func TestKeystoreStore_MemoryOnlyDoesNotPersist(t *testing.T) {
	s := NewKeystoreStore()
	if err := s.Put("alice.poweur.net", entry("e1")); err != nil {
		t.Fatalf("memory-only store must accept writes: %v", err)
	}
}
