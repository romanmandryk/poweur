package storage

import "sync"

type StoredMessage struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type InboxStore struct {
	mu       sync.Mutex
	messages map[string][]StoredMessage
}

func NewInboxStore() *InboxStore {
	return &InboxStore{
		messages: make(map[string][]StoredMessage),
	}
}

func (s *InboxStore) Add(identity string, msg StoredMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[identity] = append(s.messages[identity], msg)
}

func (s *InboxStore) Drain(identity string) []StoredMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.messages[identity]
	if len(msgs) > 0 {
		s.messages[identity] = nil
	}
	return msgs
}
