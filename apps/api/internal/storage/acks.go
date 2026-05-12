package storage

import "sync"

// StoredAck mirrors the on-wire Ack envelope. Acks are keyed in the
// AckStore by the ack's `recipient` (== the original message sender),
// because that is the identity the ack is "delivered to" for inbox-poll
// surfacing.
type StoredAck struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	State     string `json:"state"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Signature string `json:"signature"`
	SessionID string `json:"session_id,omitempty"`
}

// AckStore is the in-memory queue of pending acks per identity. It mirrors
// InboxStore exactly: acks are appended on receipt and drained when the
// addressed identity polls. Like the inbox, acks are ephemeral and lost on
// relay restart — clients are expected to fall back to message-level retry
// or operator-driven re-sync if state is critical.
type AckStore struct {
	mu   sync.Mutex
	acks map[string][]StoredAck
}

func NewAckStore() *AckStore {
	return &AckStore{acks: make(map[string][]StoredAck)}
}

// Add appends ack to the identity's queue. When max > 0 and the queue is
// already at capacity the oldest ack is dropped to make room — acks are
// delivery receipts and it is better to surface recent ones than old ones.
func (s *AckStore) Add(identity string, ack StoredAck, max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if max > 0 && len(s.acks[identity]) >= max {
		s.acks[identity] = s.acks[identity][1:]
	}
	s.acks[identity] = append(s.acks[identity], ack)
}

func (s *AckStore) Drain(identity string) []StoredAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	acks := s.acks[identity]
	if len(acks) > 0 {
		s.acks[identity] = nil
	}
	return acks
}
