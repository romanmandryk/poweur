package storage

import (
	"sync"
	"time"
)

// Challenge nonces for signed reads.
//
// The store used to keep exactly **one** outstanding challenge per identity,
// so two authenticated reads in flight at once invalidated each other — a
// client with a push stream reconnecting in the background and a poll running
// in the foreground would see random "challenge missing or expired" failures.
// Several may now be outstanding; each is still single-use and short-lived,
// and a caller that echoes the value it was given spends exactly that one.
const maxChallengesPerIdentity = 32

type Challenge struct {
	Value     string
	ExpiresAt time.Time
	Used      bool
}

type ChallengeStore struct {
	mu         sync.Mutex
	challenges map[string][]Challenge
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{
		challenges: make(map[string][]Challenge),
	}
}

func (s *ChallengeStore) Issue(identity, value string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveLocked(identity)
	if len(live) >= maxChallengesPerIdentity {
		live = live[len(live)-maxChallengesPerIdentity+1:]
	}
	s.challenges[identity] = append(live, Challenge{Value: value, ExpiresAt: expiresAt})
}

// liveLocked drops expired and already-used entries.
func (s *ChallengeStore) liveLocked(identity string) []Challenge {
	now := time.Now()
	kept := make([]Challenge, 0, len(s.challenges[identity]))
	for _, challenge := range s.challenges[identity] {
		if challenge.Used || now.After(challenge.ExpiresAt) {
			continue
		}
		kept = append(kept, challenge)
	}
	return kept
}

// Consume spends the most recent outstanding challenge. Kept for callers that
// cannot echo a value back — a WebAuthn assertion carries the challenge inside
// its signed client data rather than in a header.
func (s *ChallengeStore) Consume(identity string) (Challenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveLocked(identity)
	if len(live) == 0 {
		delete(s.challenges, identity)
		return Challenge{}, false
	}
	challenge := live[len(live)-1]
	s.challenges[identity] = live[:len(live)-1]
	challenge.Used = true
	return challenge, true
}

// ConsumeValue spends the specific challenge a caller was issued, which is what
// makes concurrent authenticated reads safe: two callers each spend their own.
func (s *ChallengeStore) ConsumeValue(identity, value string) (Challenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveLocked(identity)
	for i, challenge := range live {
		if challenge.Value != value {
			continue
		}
		s.challenges[identity] = append(live[:i], live[i+1:]...)
		challenge.Used = true
		return challenge, true
	}
	s.challenges[identity] = live
	return Challenge{}, false
}
