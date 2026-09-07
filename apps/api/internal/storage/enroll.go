package storage

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Enrollment rendezvous (EPIC-011 E11-T3).
//
// A blind letterbox for moving a seed from a device that has it to one that
// does not. The relay holds an ephemeral public key and, later, a sealed blob
// it cannot open: the payload is encrypted to the new device's ephemeral
// X25519 key, which never leaves that device.
//
// Entries are short-lived and single-use. A completed or expired transfer is
// unrecoverable by design — that is what stops a captured rendezvous id from
// being replayed later.

// ErrRendezvousNotFound covers unknown, expired and already-claimed entries
// alike: the caller learns only that this rendezvous is not usable.
var ErrRendezvousNotFound = errors.New("rendezvous not found")

// Rendezvous is one in-flight enrollment offer.
type Rendezvous struct {
	ID string
	// Identity the new device is asking to join.
	Identity string
	// EphemeralPublicKey is the new device's X25519 key (base64url).
	EphemeralPublicKey string
	// Label describes the requesting device, shown to the approving user.
	Label string
	// Sealed is the seed encrypted to that key. Empty until delivery.
	Sealed    string
	Delivered bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

// RendezvousStore keeps offers in memory: they live for minutes, and losing
// them on restart is a retry, not a data loss.
type RendezvousStore struct {
	mu      sync.Mutex
	entries map[string]Rendezvous
}

func NewRendezvousStore() *RendezvousStore {
	return &RendezvousStore{entries: make(map[string]Rendezvous)}
}

// MaxOpenPerIdentity caps concurrent offers so a stranger cannot flood an
// identity's rendezvous space (the identity name is public).
const MaxOpenPerIdentity = 5

// ErrTooManyOffers is returned when an identity already has the maximum
// number of open offers.
var ErrTooManyOffers = errors.New("too many open enrollment offers")

// Offer records a new device's ephemeral key and returns the entry.
func (s *RendezvousStore) Offer(r Rendezvous) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	identity := strings.ToLower(r.Identity)
	open := 0
	for _, existing := range s.entries {
		if strings.ToLower(existing.Identity) == identity {
			open++
		}
	}
	if open >= MaxOpenPerIdentity {
		return ErrTooManyOffers
	}
	r.Identity = identity
	s.entries[r.ID] = r
	return nil
}

// Get returns a live rendezvous for an identity.
func (s *RendezvousStore) Get(identity, id string) (Rendezvous, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.entries[id]
	if !ok || !strings.EqualFold(entry.Identity, identity) {
		return Rendezvous{}, ErrRendezvousNotFound
	}
	return entry, nil
}

// Deliver attaches the sealed payload. Refuses to overwrite an existing one so
// a race cannot swap what the new device is about to unwrap.
func (s *RendezvousStore) Deliver(identity, id, sealed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.entries[id]
	if !ok || !strings.EqualFold(entry.Identity, identity) {
		return ErrRendezvousNotFound
	}
	if entry.Delivered {
		return errors.New("rendezvous already has a payload")
	}
	entry.Sealed = sealed
	entry.Delivered = true
	s.entries[id] = entry
	return nil
}

// Claim returns the sealed payload once and deletes the entry.
func (s *RendezvousStore) Claim(identity, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.entries[id]
	if !ok || !strings.EqualFold(entry.Identity, identity) {
		return "", ErrRendezvousNotFound
	}
	if !entry.Delivered {
		return "", nil // still waiting for the other device
	}
	delete(s.entries, id)
	return entry.Sealed, nil
}

// Cancel removes an entry, e.g. when the user aborts on either screen.
func (s *RendezvousStore) Cancel(identity, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[id]; ok && strings.EqualFold(entry.Identity, identity) {
		delete(s.entries, id)
	}
}

func (s *RendezvousStore) pruneLocked() {
	now := time.Now().UTC()
	for id, entry := range s.entries {
		if entry.ExpiresAt.Before(now) {
			delete(s.entries, id)
		}
	}
}
