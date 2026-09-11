package storage

import (
	"crypto/ed25519"
	"strings"
	"sync"
	"time"
)

type Session struct {
	ID                string
	Identity          string
	PublicKey         string
	PublicKeyBytes    ed25519.PublicKey
	IssuedAt          time.Time
	ExpiresAt         time.Time
	DeviceFingerprint string

	// Original fields from the session-registration request, kept verbatim so
	// the relay can reconstruct a SessionProof when forwarding session-signed
	// messages to a peer relay.
	IssuedAtRaw       string
	ExpiresAtRaw      string
	Nonce             string
	IdentitySignature string
}

type SessionStore struct {
	mu         sync.RWMutex
	sessions   map[string]Session
	byIdentity map[string]map[string]struct{}
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions:   make(map[string]Session),
		byIdentity: make(map[string]map[string]struct{}),
	}
}

func (s *SessionStore) Put(session Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	if _, ok := s.byIdentity[session.Identity]; !ok {
		s.byIdentity[session.Identity] = make(map[string]struct{})
	}
	s.byIdentity[session.Identity][session.ID] = struct{}{}
}

func (s *SessionStore) Get(id string) (Session, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return Session{}, false
	}
	if time.Now().After(session.ExpiresAt) {
		s.Delete(id)
		return Session{}, false
	}
	return session, true
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return
	}
	delete(s.sessions, id)
	if set, ok := s.byIdentity[session.Identity]; ok {
		delete(set, id)
		if len(set) == 0 {
			delete(s.byIdentity, session.Identity)
		}
	}
}

// DeleteMatching removes every live session of an identity that pred
// accepts, returning how many went. Device revocation (EPIC-004 E04-T6)
// needs it: sessions are keyed by id, but a revocation names a device, and
// the identity match has to be case-insensitive because the session carries
// whatever spelling its registration request used.
func (s *SessionStore) DeleteMatching(identity string, pred func(Session) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, session := range s.sessions {
		if !strings.EqualFold(session.Identity, identity) || !pred(session) {
			continue
		}
		delete(s.sessions, id)
		if set, ok := s.byIdentity[session.Identity]; ok {
			delete(set, id)
			if len(set) == 0 {
				delete(s.byIdentity, session.Identity)
			}
		}
		removed++
	}
	return removed
}

func (s *SessionStore) ListForIdentity(identity string) []Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Session
	now := time.Now()
	for id := range s.byIdentity[identity] {
		session, ok := s.sessions[id]
		if !ok {
			continue
		}
		if now.After(session.ExpiresAt) {
			continue
		}
		result = append(result, session)
	}
	return result
}

func (s *SessionStore) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, id)
			if set, ok := s.byIdentity[session.Identity]; ok {
				delete(set, id)
				if len(set) == 0 {
					delete(s.byIdentity, session.Identity)
				}
			}
		}
	}
}
