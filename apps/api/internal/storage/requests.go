package storage

import (
	"strings"
	"sync"
	"time"
)

// RequestStore holds pending consent gestures (contact requests/accepts and
// share offers). Contact handshakes retain one slot per sender; share offers
// get one slot per signed share_id so one sender can offer independent grants
// without creating a message stream. Memory-only like the inbox (durability
// arrives with EPIC-009).
type RequestStore struct {
	mu      sync.Mutex
	pending map[string]map[string]StoredMessage // recipient -> request slot -> message
	// last records when a sender's request was queued, surviving Drain, so
	// re-requests respect the cooldown even after the recipient saw (and
	// ignored) the first one.
	last map[string]time.Time // recipient+"\n"+sender -> queued at
}

// Request outcomes for Add.
const (
	RequestQueued   = "queued"
	RequestDup      = "request_pending"
	RequestCooldown = "request_cooldown"
)

func NewRequestStore() *RequestStore {
	return &RequestStore{
		pending: make(map[string]map[string]StoredMessage),
		last:    make(map[string]time.Time),
	}
}

func requestKey(recipient, sender string) string {
	return strings.ToLower(recipient) + "\n" + strings.ToLower(sender)
}

func requestSlot(sender string, msg StoredMessage) string {
	slot := strings.ToLower(sender)
	if strings.EqualFold(strings.TrimSpace(msg.Type), "sys.share.offer") {
		if shareID := strings.TrimSpace(msg.Metadata["share_id"]); shareID != "" {
			return slot + "\nshare:" + shareID
		}
	}
	return slot
}

// Add queues a contact request. A sender holds at most one pending slot per
// recipient, and after the slot is drained a re-request is only accepted
// once cooldown has passed.
func (s *RequestStore) Add(recipient, sender string, msg StoredMessage, cooldown time.Duration) string {
	rk := strings.ToLower(recipient)
	sk := requestSlot(sender, msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[rk][sk]; ok {
		return RequestDup
	}
	lastKey := strings.ToLower(recipient) + "\n" + sk
	if at, ok := s.last[lastKey]; ok && cooldown > 0 && time.Since(at) < cooldown {
		return RequestCooldown
	}
	if s.pending[rk] == nil {
		s.pending[rk] = make(map[string]StoredMessage)
	}
	s.pending[rk][sk] = msg
	s.last[lastKey] = time.Now()
	return RequestQueued
}

// Drain returns and clears the recipient's pending requests (cooldown
// bookkeeping is retained).
func (s *RequestStore) Drain(recipient string) []StoredMessage {
	rk := strings.ToLower(recipient)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.pending[rk]
	if len(m) == 0 {
		return nil
	}
	out := make([]StoredMessage, 0, len(m))
	for _, msg := range m {
		out = append(out, msg)
	}
	delete(s.pending, rk)
	return out
}

// ClearCooldown forgets the cooldown for (recipient, sender) — used when a
// request is accepted so future flows are unimpeded.
func (s *RequestStore) ClearCooldown(recipient, sender string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.last, requestKey(recipient, sender))
	if m := s.pending[strings.ToLower(recipient)]; m != nil {
		delete(m, strings.ToLower(sender))
	}
}
