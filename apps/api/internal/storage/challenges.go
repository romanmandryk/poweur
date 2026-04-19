package storage

import (
	"sync"
	"time"
)

type Challenge struct {
	Value     string
	ExpiresAt time.Time
	Used      bool
}

type ChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]Challenge
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{
		challenges: make(map[string]Challenge),
	}
}

func (s *ChallengeStore) Issue(identity, value string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges[identity] = Challenge{
		Value:     value,
		ExpiresAt: expiresAt,
		Used:      false,
	}
}

func (s *ChallengeStore) Consume(identity string) (Challenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.challenges[identity]
	if !ok {
		return Challenge{}, false
	}
	if challenge.Used || time.Now().After(challenge.ExpiresAt) {
		delete(s.challenges, identity)
		return Challenge{}, false
	}
	challenge.Used = true
	s.challenges[identity] = challenge
	return challenge, true
}
