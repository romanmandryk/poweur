package storage

import (
	"crypto/ed25519"
	"sync"
	"time"
)

type Identity struct {
	Identity      string
	PublicKey     string
	PublicKeyBytes ed25519.PublicKey
	CreatedAt     time.Time
}

type IdentityStore struct {
	mu         sync.RWMutex
	identities map[string]Identity
}

func NewIdentityStore() *IdentityStore {
	return &IdentityStore{
		identities: make(map[string]Identity),
	}
}

func (s *IdentityStore) Add(identity Identity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.identities[identity.Identity]; exists {
		return false
	}
	s.identities[identity.Identity] = identity
	return true
}

func (s *IdentityStore) Get(name string) (Identity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.identities[name]
	return identity, ok
}

func (s *IdentityStore) Exists(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.identities[name]
	return ok
}
