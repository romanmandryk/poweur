package storage

import (
	"path/filepath"
	"strings"
	"time"
)

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

// AckStore queues pending acks per identity. It mirrors InboxStore exactly,
// including durability (EPIC-009 E09-T1) — a receipt that vanishes on restart
// leaves a sender staring at one tick for a message that did arrive.
type AckStore struct {
	spool *spool[StoredAck]
}

// NewAckStore returns a memory-only queue (tests, relays without POWEUR_DATA).
func NewAckStore() *AckStore {
	s, _ := newSpool[StoredAck]("")
	return &AckStore{spool: s}
}

// OpenAckStore spools under dataDir/spool/acks.
func OpenAckStore(dataDir string) (*AckStore, error) {
	dir := ""
	if strings.TrimSpace(dataDir) != "" {
		dir = filepath.Join(dataDir, "spool", "acks")
	}
	s, err := newSpool[StoredAck](dir)
	if err != nil {
		return nil, err
	}
	return &AckStore{spool: s}, nil
}

// Add appends ack to the identity's queue. When max > 0 and the queue is
// already at capacity the oldest ack is dropped to make room — acks are
// delivery receipts and it is better to surface recent ones than old ones.
func (s *AckStore) Add(identity string, ack StoredAck, max int) {
	s.spool.add(identity, ack, max, true)
}

func (s *AckStore) Drain(identity string) []StoredAck {
	return s.spool.drain(identity)
}

// Since / Consume mirror the inbox: read without forgetting, forget on the
// client's word.
func (s *AckStore) Since(identity, cursor string) ([]StoredAck, string) {
	return s.spool.since(identity, cursor)
}

func (s *AckStore) Consume(identity, cursor string) int {
	return s.spool.consume(identity, cursor)
}

func (s *AckStore) Expire(before time.Time) []ExpiredEntry[StoredAck] {
	return s.spool.expire(before)
}
