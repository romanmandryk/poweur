package identity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Client-side message history (EPIC-009 E09-T1, the client half).
//
// The relay's inbox is a *spool*, not an archive: `GET /messages` drains, and
// what a client has been handed is gone from the relay. Everything a person
// expects a messaging app to remember therefore has to be written down by the
// client, and this file is the format it is written in.
//
// It lives in the owner-only, end-to-end encrypted zone `.poweur/private/`.
// The record format below is kept; the storage v1 layout (one file per
// message in month shards, HistoryPath) is replaced by one append file per
// conversation in storage v2 (EPIC-020 E20-T11).
//
// Three properties the format was chosen for:
//
//  1. **Sealed.** `.poweur/private` is "the relay stores but cannot read";
//     sealing the bytes to the owner's own X25519 key
//     makes that a fact rather than a promise. Every identity has that key —
//     it is the one messages are already encrypted to — so history needs no
//     new key custody, and any enrolled device can read it.
//  2. **Write-once.** One file per message, named by a key that sorts in
//     arrival order. Two devices that pick up the same message write the same
//     bytes to the same path, so there is no lost update to resolve and no
//     merge to get wrong. Read state is the one mutable document, and it is
//     last-write-wins on purpose — the cost of losing that race is an unread
//     mark, not a message.
//  3. **Sharded by month.** A flat directory is a PROPFIND that grows without
//     bound; the shard keeps a listing proportional to recent traffic.
const (
	HistoryDir           = ".poweur/private/messages"
	HistoryReadStatePath = ".poweur/private/read-state.json"

	// HistoryVersion is the schema version of both documents below.
	HistoryVersion = 1

	// MaxHistoryBody caps a stored body so one enormous message cannot make
	// the tree unreadable. Messages themselves are capped at 512 KB by the
	// relay; history keeps the same ceiling.
	MaxHistoryBody = 512 * 1024
)

// Queues a record can have come from. The queue is part of the record because
// it is not recoverable from the envelope: an anonymous message and a signed
// one differ in whether `sender` is trustworthy, not in whether it is set.
const (
	HistoryQueueInbox     = "inbox"
	HistoryQueueAnonymous = "anonymous"
	HistoryQueueRequests  = "requests"
	// HistoryQueueSent marks the owner's own outbound copy — the relay never
	// hands a sender their own message back, so this is the only record of it.
	HistoryQueueSent = "sent"
)

// HistoryRecord is the plaintext of one archived message. It is sealed before
// it touches the tree; `SealedDocument` is what is actually written.
type HistoryRecord struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Sender    string `json:"sender,omitempty"` // empty for anonymous
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"` // RFC3339
	Type      string `json:"type,omitempty"`
	// ThreadID is carried so a client that redraws from the archive after a
	// reload can group the conversation the same way the live inbox did
	// (EPIC-009 E09-T3). `omitempty` keeps every record written before this
	// field existed byte-identical.
	ThreadID  string            `json:"thread_id,omitempty"`
	ExpiresAt string            `json:"expires_at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Queue     string            `json:"queue"`
	// Subject is the message's subject line (E09-T13), parsed out of a
	// `headers-v1` plaintext; Body then holds only the text. `omitempty`
	// keeps a record without one byte-identical to the earlier format.
	Subject string `json:"subject,omitempty"`
	// Body is the *decrypted* message text. History is the plaintext archive;
	// keeping ciphertext would mean re-deriving a shared secret with an
	// ephemeral key nobody kept.
	Body string `json:"body"`
}

// ThreadKey is the record's grouping key inside its conversation
// (ConversationKey).
func (r HistoryRecord) ThreadKey() string { return ConversationKey(r.ThreadID, r.Subject) }

// Peer is the other party in the conversation this record belongs to, from
// the owner's point of view. Anonymous senders have no identity to group by,
// so they all share one pseudo-peer.
func (r HistoryRecord) Peer(owner string) string {
	if r.Queue == HistoryQueueAnonymous || r.Sender == "" {
		return AnonymousPeer
	}
	if strings.EqualFold(r.Sender, owner) {
		return strings.ToLower(r.Recipient)
	}
	return strings.ToLower(r.Sender)
}

// AnonymousPeer groups every unsigned message into one conversation. It is
// not a resolvable identity and is deliberately not one: an anonymous sender
// has no name to thread by, and pretending otherwise would let two unrelated
// strangers appear as one correspondent.
const AnonymousPeer = "anonymous"

// Validate checks a record before it is sealed.
func (r HistoryRecord) Validate() error {
	if r.Version != 0 && r.Version != HistoryVersion {
		return fmt.Errorf("unsupported history record version %d", r.Version)
	}
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("history record: id is required")
	}
	if strings.TrimSpace(r.Recipient) == "" {
		return fmt.Errorf("history record: recipient is required")
	}
	if _, err := time.Parse(time.RFC3339, r.Timestamp); err != nil {
		return fmt.Errorf("history record: timestamp must be RFC3339: %w", err)
	}
	if err := ValidateEnvelopeExtensions(r.Type, r.ThreadID, r.ExpiresAt, r.Metadata); err != nil {
		return fmt.Errorf("history record: %w", err)
	}
	switch r.Queue {
	case HistoryQueueInbox, HistoryQueueAnonymous, HistoryQueueRequests, HistoryQueueSent:
	default:
		return fmt.Errorf("history record: invalid queue %q", r.Queue)
	}
	if err := ValidateSubject(r.Subject); err != nil {
		return fmt.Errorf("history record: %w", err)
	}
	if len(r.Body) > MaxHistoryBody {
		return fmt.Errorf("history record: body exceeds %d bytes", MaxHistoryBody)
	}
	return nil
}

// SealedDocument is the on-tree form of anything in the owner-only zone: the
// same X25519 + HKDF + ChaCha20-Poly1305 envelope messages already use, with
// the owner as their own recipient.
type SealedDocument struct {
	Version            int    `json:"version"`
	Alg                string `json:"alg"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
}

// Validate checks a sealed envelope has everything needed to open it.
func (d SealedDocument) Validate() error {
	if d.Version != 0 && d.Version != HistoryVersion {
		return fmt.Errorf("unsupported sealed document version %d", d.Version)
	}
	if d.Alg == "" || d.EphemeralPublicKey == "" || d.Nonce == "" || d.Ciphertext == "" {
		return fmt.Errorf("sealed document: alg, ephemeral_public_key, nonce and ciphertext are required")
	}
	return nil
}

// ParseSealedDocument decodes and validates a sealed envelope.
func ParseSealedDocument(raw []byte) (SealedDocument, error) {
	var d SealedDocument
	if err := json.Unmarshal(raw, &d); err != nil {
		return SealedDocument{}, fmt.Errorf("invalid sealed document: %w", err)
	}
	if err := d.Validate(); err != nil {
		return SealedDocument{}, err
	}
	return d, nil
}

// HistoryShard is the month directory a timestamp belongs to. An unparseable
// timestamp lands in "unknown" rather than failing: a record that cannot be
// filed is still a record worth keeping.
func HistoryShard(timestamp string) string {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return "unknown"
	}
	return t.UTC().Format("2006-01")
}

// HistoryFileName is the write-once name for one record.
//
// The sort key comes first so a plain lexical listing is chronological, and
// the id follows so two messages in the same second cannot collide.
//
// The id is chosen by the *sender*, which makes it untrusted input on its way
// to becoming a path segment. Anything outside the id alphabet is not escaped
// but replaced wholesale by a hash of the original: escaping invites an
// argument about which escape is correct, while a hash is unarguably a single
// safe segment and is still deterministic, so the write stays idempotent.
func HistoryFileName(timestamp, id string) string {
	key := "00000000T000000Z"
	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		key = t.UTC().Format("20060102T150405Z")
	}
	return key + "-" + safeIDSegment(id) + ".json"
}

// HistoryPeerHash names a conversation without revealing the peer. Group
// conversations use the group identity as the peer. Anonymous mail shares
// AnonymousPeer.
func HistoryPeerHash(peer string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(peer))))
	return hex.EncodeToString(sum[:])
}

// HistoryLogName is the append file for one conversation.
func HistoryLogName(peer string) string { return HistoryPeerHash(peer) + ".jsonl" }

// HistoryPath is the full tree path for one record.
func HistoryPath(timestamp, id string) string {
	return HistoryDir + "/" + HistoryShard(timestamp) + "/" + HistoryFileName(timestamp, id)
}

// maxIDSegment bounds the id half of a filename well below the 255-byte
// segment limit, leaving room for the sort key and suffix.
const maxIDSegment = 96

func safeIDSegment(id string) string {
	safe := id != "" && len(id) <= maxIDSegment
	if safe {
		for _, r := range id {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
				continue
			}
			safe = false
			break
		}
	}
	if safe {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "h-" + base64.RawURLEncoding.EncodeToString(sum[:12])
}

// ReadMark is how far the owner has read in one conversation: a *position*
// in the archive's total order, not a moment in time.
//
// A timestamp alone is not enough. Message timestamps are RFC3339 to the
// second, so two messages a moment apart routinely share one, and a mark that
// says "read through 09:30:00" silently swallows the one that arrived at
// 09:30:00 after you looked. Carrying the id makes the mark comparable with
// exactly the ordering `SortHistory` imposes, so "everything up to here" means
// the same thing to the sort and to the count.
type ReadMark struct {
	Timestamp string `json:"timestamp"`
	ID        string `json:"id,omitempty"`
}

// After reports whether m is at or past the position of the given record.
func (m ReadMark) After(timestamp, id string) bool {
	if m.Timestamp != timestamp {
		return newerTimestamp(m.Timestamp, timestamp)
	}
	return m.ID >= id
}

// Before is After's complement for advancing a mark.
func (m ReadMark) Before(timestamp, id string) bool {
	return !m.After(timestamp, id)
}

// ReadState records how far the owner has read in each conversation — what
// turns "messages held" into "messages unread", a count that can reach zero.
type ReadState struct {
	Version int `json:"version"`
	// Conversations maps a lowercased peer identity to a read position.
	Conversations map[string]ReadMark `json:"conversations"`
	// Threads holds per-thread marks (E09-T13): peer → thread key
	// (ConversationKey) → position. A record is read when either its peer
	// mark or its thread mark covers it, so the view that advances the peer
	// mark (By ID) and the one that advances thread marks (By subject) never
	// disagree. A writer that drops this field only makes messages more
	// unread, never hides one.
	Threads map[string]map[string]ReadMark `json:"threads,omitempty"`
}

// MaxThreadMarks caps the thread marks kept per peer.
const MaxThreadMarks = 500

// Validate checks the read-state document.
func (s ReadState) Validate() error {
	if s.Version != 0 && s.Version != HistoryVersion {
		return fmt.Errorf("unsupported read-state version %d", s.Version)
	}
	for peer, mark := range s.Conversations {
		if strings.TrimSpace(peer) == "" {
			return fmt.Errorf("read-state: empty peer")
		}
		if _, err := time.Parse(time.RFC3339, mark.Timestamp); err != nil {
			return fmt.Errorf("read-state: %s: timestamp must be RFC3339: %w", peer, err)
		}
	}
	for peer, marks := range s.Threads {
		if strings.TrimSpace(peer) == "" {
			return fmt.Errorf("read-state: empty peer in threads")
		}
		if len(marks) > MaxThreadMarks {
			return fmt.Errorf("read-state: %s has too many thread marks (max %d)", peer, MaxThreadMarks)
		}
		for key, mark := range marks {
			if _, err := time.Parse(time.RFC3339, mark.Timestamp); err != nil {
				return fmt.Errorf("read-state: %s/%s: timestamp must be RFC3339: %w", peer, key, err)
			}
		}
	}
	return nil
}

func (s ReadState) clone() ReadState {
	out := ReadState{Version: HistoryVersion, Conversations: map[string]ReadMark{}}
	for k, v := range s.Conversations {
		out.Conversations[k] = v
	}
	if len(s.Threads) > 0 {
		out.Threads = map[string]map[string]ReadMark{}
		for peer, marks := range s.Threads {
			copied := make(map[string]ReadMark, len(marks))
			for k, v := range marks {
				copied[k] = v
			}
			out.Threads[peer] = copied
		}
	}
	return out
}

// Covers reports whether the read state marks a record of peer as read: the
// peer mark or the record's thread mark is at or past it.
func (s ReadState) Covers(peer, threadKey, timestamp, id string) bool {
	peer = strings.ToLower(strings.TrimSpace(peer))
	if mark, ok := s.Conversations[peer]; ok && mark.After(timestamp, id) {
		return true
	}
	if mark, ok := s.Threads[peer][threadKey]; ok && mark.After(timestamp, id) {
		return true
	}
	return false
}

// MarkThreadRead advances one thread's mark inside a peer's conversation,
// never rewinds it. A thread mark the peer mark already covers is not stored.
func (s ReadState) MarkThreadRead(peer, threadKey, timestamp, id string) ReadState {
	key := strings.ToLower(strings.TrimSpace(peer))
	out := s.clone()
	if key == "" || timestamp == "" {
		return out
	}
	if mark, ok := out.Conversations[key]; ok && mark.After(timestamp, id) {
		return out
	}
	if out.Threads == nil {
		out.Threads = map[string]map[string]ReadMark{}
	}
	marks := out.Threads[key]
	if marks == nil {
		marks = map[string]ReadMark{}
		out.Threads[key] = marks
	}
	if existing, ok := marks[threadKey]; !ok || existing.Before(timestamp, id) {
		marks[threadKey] = ReadMark{Timestamp: timestamp, ID: id}
	}
	// Over the cap, the oldest marks go: forgetting one makes an old thread
	// unread again, which is the safe way to be wrong.
	for len(marks) > MaxThreadMarks {
		oldest := ""
		for k, m := range marks {
			if oldest == "" || m.Before(marks[oldest].Timestamp, marks[oldest].ID) {
				oldest = k
			}
		}
		delete(marks, oldest)
	}
	return out
}

// ParseReadState decodes and validates read-state.json.
func ParseReadState(raw []byte) (ReadState, error) {
	var s ReadState
	if err := json.Unmarshal(raw, &s); err != nil {
		return ReadState{}, fmt.Errorf("invalid read-state: %w", err)
	}
	if err := s.Validate(); err != nil {
		return ReadState{}, err
	}
	if s.Conversations == nil {
		s.Conversations = map[string]ReadMark{}
	}
	return s, nil
}

// MarkRead advances a peer's read mark, never rewinds it. Two devices reading
// the same conversation race on this document, and the safe direction to lose
// that race in is "already read".
func (s ReadState) MarkRead(peer, timestamp, id string) ReadState {
	key := strings.ToLower(strings.TrimSpace(peer))
	if key == "" || timestamp == "" {
		return s
	}
	out := s.clone()
	if existing, ok := out.Conversations[key]; !ok || existing.Before(timestamp, id) {
		out.Conversations[key] = ReadMark{Timestamp: timestamp, ID: id}
	}
	// Thread marks the new peer mark covers say nothing any more.
	mark := out.Conversations[key]
	for threadKey, threadMark := range out.Threads[key] {
		if mark.After(threadMark.Timestamp, threadMark.ID) {
			delete(out.Threads[key], threadKey)
		}
	}
	if len(out.Threads[key]) == 0 {
		delete(out.Threads, key)
	}
	if len(out.Threads) == 0 {
		out.Threads = nil
	}
	return out
}

// Unread counts records past each peer's read mark. The owner's own sent
// copies never count: you have read what you wrote.
func (s ReadState) Unread(owner string, records []HistoryRecord) map[string]int {
	counts := map[string]int{}
	for _, r := range records {
		if r.Queue == HistoryQueueSent || strings.EqualFold(r.Sender, owner) {
			continue
		}
		peer := r.Peer(owner)
		if s.Covers(peer, r.ThreadKey(), r.Timestamp, r.ID) {
			continue
		}
		counts[peer]++
	}
	return counts
}

// newerTimestamp reports whether a is strictly after b. Unparseable values
// compare lexically, which is right for the RFC3339 subset in use and
// harmless for anything else.
func newerTimestamp(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return a > b
	}
	return ta.After(tb)
}

// SortHistory orders records oldest-first, breaking ties on id so the order
// is total — two clients rendering the same archive must agree on it.
func SortHistory(records []HistoryRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Timestamp == records[j].Timestamp {
			return records[i].ID < records[j].ID
		}
		return newerTimestamp(records[j].Timestamp, records[i].Timestamp)
	})
}
