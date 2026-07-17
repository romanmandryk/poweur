package storage

import "sync"

type StoredMessage struct {
	ID         string               `json:"id"`
	Sender     string               `json:"sender"`
	Recipient  string               `json:"recipient"`
	Timestamp  string               `json:"timestamp"`
	Payload    string               `json:"payload"`
	Signature  string               `json:"signature"`
	Type       string               `json:"type,omitempty"`
	SessionID  string               `json:"session_id,omitempty"`
	Encryption *StoredEncryptionMeta `json:"encryption,omitempty"`
}

// StoredEncryptionMeta mirrors the encryption envelope carried on the wire.
// Preserving these fields is what lets the recipient decrypt the payload
// later when draining the inbox.
type StoredEncryptionMeta struct {
	Alg                string `json:"alg"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
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

// Add appends msg to the identity's inbox. Returns false without storing when
// the inbox is at capacity (max > 0), so the caller can return a 503 and let
// the sender retry later rather than silently losing the message.
func (s *InboxStore) Add(identity string, msg StoredMessage, max int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if max > 0 && len(s.messages[identity]) >= max {
		return false
	}
	s.messages[identity] = append(s.messages[identity], msg)
	return true
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
