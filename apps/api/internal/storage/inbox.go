package storage

import (
	"path/filepath"
	"strings"
	"time"
)

type StoredMessage struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
	Type      string `json:"type,omitempty"`
	// ThreadID, ExpiresAt and Metadata are part of what the sender signed
	// (EPIC-009 E09-T3), so the spool has to hand them back verbatim: a
	// recipient that recomputes the canonical string without them would
	// reject a message the relay already verified.
	ThreadID   string                `json:"thread_id,omitempty"`
	ExpiresAt  string                `json:"expires_at,omitempty"`
	Metadata   map[string]string     `json:"metadata,omitempty"`
	SessionID  string                `json:"session_id,omitempty"`
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

// InboxStore holds undelivered messages. Durable when opened with a data dir
// (EPIC-009 E09-T1): a relay restart used to lose every message nobody had
// polled for yet.
type InboxStore struct {
	spool *spool[StoredMessage]
}

// NewInboxStore returns a memory-only inbox — restart loses undelivered mail.
// Used by tests and by relays configured without POWEUR_DATA.
func NewInboxStore() *InboxStore {
	s, _ := newSpool[StoredMessage]("")
	return &InboxStore{spool: s}
}

// OpenInboxStore spools under dataDir/spool/messages, loading whatever a
// previous process left behind.
func OpenInboxStore(dataDir string) (*InboxStore, error) {
	dir := ""
	if strings.TrimSpace(dataDir) != "" {
		dir = filepath.Join(dataDir, "spool", "messages")
	}
	s, err := newSpool[StoredMessage](dir)
	if err != nil {
		return nil, err
	}
	return &InboxStore{spool: s}, nil
}

// Add appends msg to the identity's inbox. Returns false without storing when
// the inbox is at capacity (max > 0), so the caller can return a 503 and let
// the sender retry later rather than silently losing the message.
func (s *InboxStore) Add(identity string, msg StoredMessage, max int) bool {
	return s.spool.add(identity, msg, max, false)
}

// Drain returns everything and forgets it — the pre-cursor pickup, kept for
// clients that have not moved to `?since=`.
func (s *InboxStore) Drain(identity string) []StoredMessage {
	return s.spool.drain(identity)
}

// Since returns messages after `cursor` and leaves them in place; the returned
// cursor is what the client acknowledges with Consume once it has them.
func (s *InboxStore) Since(identity, cursor string) ([]StoredMessage, string) {
	return s.spool.since(identity, cursor)
}

// Consume forgets everything through `cursor`. Splitting this from the read is
// the point of the change: a connection that drops after the relay has already
// deleted the messages loses them.
func (s *InboxStore) Consume(identity, cursor string) int {
	return s.spool.consume(identity, cursor)
}

// Expire drops messages older than `before`, reporting them so the caller can
// tell each sender their message was never collected.
func (s *InboxStore) Expire(before time.Time) []ExpiredEntry[StoredMessage] {
	return s.spool.expire(before)
}

// Pending is how many messages are waiting for an identity.
func (s *InboxStore) Pending(identity string) int { return s.spool.count(identity) }

// Depth is the aggregate queue size; it reveals no identity names.
func (s *InboxStore) Depth() int64 {
	s.spool.mu.Lock()
	defer s.spool.mu.Unlock()
	var n int64
	for _, entries := range s.spool.entries {
		n += int64(len(entries))
	}
	return n
}
