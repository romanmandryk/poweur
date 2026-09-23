package storage

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
)

// Enrollment rendezvous (EPIC-011 E11-T8, pairing v2).
//
// A blind letterbox for moving a seed from a device that has it to one that
// does not. The relay holds, in order: the new device's commitment to its
// ephemeral key, the approving device's nonce, the revealed key, and finally
// the seed sealed to that key. It can open nothing, and the order is what
// stops it substituting a key of its own (packages/identity/pairing.go).
//
// Entries are short-lived and single-use. A completed or expired transfer is
// unrecoverable by design.

// ErrRendezvousNotFound covers unknown, expired and already-claimed entries
// alike: the caller learns only that this rendezvous is not usable.
var ErrRendezvousNotFound = errors.New("rendezvous not found")

// ErrRendezvousState is a step out of order (a reveal before the approver's
// nonce, a delivery before the reveal, a second nonce or payload).
var ErrRendezvousState = errors.New("rendezvous is not at that step")

// ErrClaimToken is a new-device call without the token its offer returned.
var ErrClaimToken = errors.New("claim token does not match")

// Rendezvous is one in-flight pairing.
type Rendezvous struct {
	ID string
	// Identity the new device is asking to join.
	Identity string
	// Commitment is SHA-256(key ‖ nonce), base64url, sent before anything else.
	Commitment string
	// ClaimTokenHash authenticates the new device's own calls.
	ClaimTokenHash [32]byte
	// Label describes the requesting device, shown to the approving user.
	Label string
	// ApproverNonce is the approving device's contribution; Mode says whether
	// it scanned the commitment ("scan") or will compare digits ("compare").
	ApproverNonce string
	Mode          string
	// EphemeralPublicKey and CommitNonce open the commitment.
	EphemeralPublicKey string
	CommitNonce        string
	// Sealed is the seed encrypted to the key. Empty until delivery.
	Sealed    string
	Delivered bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

// State names where a pairing is, for both devices' polls.
func (r Rendezvous) State() string {
	switch {
	case r.Delivered:
		return "delivered"
	case r.EphemeralPublicKey != "":
		return "revealed"
	case r.ApproverNonce != "":
		return "nonce"
	}
	return "offered"
}

// RendezvousStore keeps pairings in memory: they live for minutes, and losing
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

// ErrCodeTaken means the generated code is already open for this identity.
var ErrCodeTaken = errors.New("rendezvous code already in use")

func key(identity, id string) string { return strings.ToLower(identity) + "/" + id }

// HashClaimToken is how a claim token is kept.
func HashClaimToken(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// Offer records a new pairing.
func (s *RendezvousStore) Offer(r Rendezvous) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	r.Identity = strings.ToLower(r.Identity)
	open := 0
	for _, existing := range s.entries {
		if existing.Identity == r.Identity {
			open++
		}
	}
	if open >= MaxOpenPerIdentity {
		return ErrTooManyOffers
	}
	if _, taken := s.entries[key(r.Identity, r.ID)]; taken {
		return ErrCodeTaken
	}
	s.entries[key(r.Identity, r.ID)] = r
	return nil
}

// Get returns a live rendezvous for an identity.
func (s *RendezvousStore) Get(identity, id string) (Rendezvous, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.entries[key(identity, id)]
	if !ok {
		return Rendezvous{}, ErrRendezvousNotFound
	}
	return entry, nil
}

// Approve records the approving device's nonce, once. Fetching again with
// the same nonce is idempotent; a different one is refused, so a replayed or
// racing approver cannot change the digits under the user.
func (s *RendezvousStore) Approve(identity, id, nonce, mode string) (Rendezvous, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	k := key(identity, id)
	entry, ok := s.entries[k]
	if !ok {
		return Rendezvous{}, ErrRendezvousNotFound
	}
	switch {
	case entry.ApproverNonce == "":
		entry.ApproverNonce, entry.Mode = nonce, mode
		s.entries[k] = entry
	case entry.ApproverNonce != nonce:
		return Rendezvous{}, ErrRendezvousState
	}
	return entry, nil
}

// Reveal stores the new device's key and nonce — only after the approver's
// nonce exists, and only from the holder of the claim token.
func (s *RendezvousStore) Reveal(identity, id, token, publicKey, commitNonce string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	k := key(identity, id)
	entry, ok := s.entries[k]
	if !ok {
		return ErrRendezvousNotFound
	}
	if !tokenMatches(entry, token) {
		return ErrClaimToken
	}
	if entry.ApproverNonce == "" || entry.EphemeralPublicKey != "" {
		return ErrRendezvousState
	}
	entry.EphemeralPublicKey, entry.CommitNonce = publicKey, commitNonce
	s.entries[k] = entry
	return nil
}

// Deliver attaches the sealed payload, once, after the reveal.
func (s *RendezvousStore) Deliver(identity, id, sealed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	k := key(identity, id)
	entry, ok := s.entries[k]
	if !ok {
		return ErrRendezvousNotFound
	}
	if entry.EphemeralPublicKey == "" || entry.Delivered {
		return ErrRendezvousState
	}
	entry.Sealed = sealed
	entry.Delivered = true
	s.entries[k] = entry
	return nil
}

// Poll returns the entry to its new device; once the payload is there it is
// handed over and the entry deleted.
func (s *RendezvousStore) Poll(identity, id, token string) (Rendezvous, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	k := key(identity, id)
	entry, ok := s.entries[k]
	if !ok {
		return Rendezvous{}, ErrRendezvousNotFound
	}
	if !tokenMatches(entry, token) {
		return Rendezvous{}, ErrClaimToken
	}
	if entry.Delivered {
		delete(s.entries, k)
	}
	return entry, nil
}

// Cancel removes an entry: the new device (token) or the approver (signed)
// backing out.
func (s *RendezvousStore) Cancel(identity, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key(identity, id))
}

func tokenMatches(entry Rendezvous, token string) bool {
	h := HashClaimToken(token)
	return token != "" && subtle.ConstantTimeCompare(h[:], entry.ClaimTokenHash[:]) == 1
}

func (s *RendezvousStore) pruneLocked() {
	now := time.Now().UTC()
	for k, entry := range s.entries {
		if entry.ExpiresAt.Before(now) {
			delete(s.entries, k)
		}
	}
}
