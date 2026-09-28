package storage

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/provider"
)

// The durable spool behind the inbox and the ack queue (EPIC-009 E09-T1).
//
// Undelivered mail used to live in a map and died with the process: a relay
// restart lost every message nobody had polled for yet. It now lands on disk
// as one file per entry, ordered by a per-identity sequence number that also
// serves as the pickup cursor.
//
// Each entry is one object at `relay/spool/<queue>/<identity>/<seq>.json` in
// the relay's provider (disk or bucket), outside every drive: undelivered
// mail belongs to the relay until the recipient takes it.
//
// Entries stay until the recipient says they have them. That is the whole
// point of the change: a drain-on-read inbox loses a message to a dropped
// connection just as surely as to a restart.

type spoolEntry[T any] struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Item T         `json:"item"`
}

type spool[T any] struct {
	mu      sync.Mutex
	objects Objects // nil = memory only
	prefix  string  // "relay/spool/<queue>/"
	entries map[string][]spoolEntry[T]
	nextSeq map[string]uint64
}

func newSpool[T any](objects Objects, queue string) (*spool[T], error) {
	s := &spool[T]{
		objects: objects,
		prefix:  "relay/spool/" + queue + "/",
		entries: make(map[string][]spoolEntry[T]),
		nextSeq: make(map[string]uint64),
	}
	if objects == nil {
		return s, nil
	}
	return s, s.load()
}

// cursorOf renders a sequence number as the opaque cursor clients echo back.
// Zero-padded so the keys sort in sequence order.
func cursorOf(seq uint64) string { return fmt.Sprintf("%020d", seq) }

func parseCursor(cursor string) uint64 {
	value, err := strconv.ParseUint(strings.TrimSpace(cursor), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func (s *spool[T]) load() error {
	return listAll(s.objects, s.prefix, func(info provider.Info) error {
		rest := strings.TrimPrefix(info.Key, s.prefix)
		dir, file, ok := strings.Cut(rest, "/")
		if !ok || !strings.HasSuffix(file, ".json") {
			return nil
		}
		raw, err := getObject(s.objects, info.Key)
		if err != nil {
			return nil
		}
		var entry spoolEntry[T]
		if err := json.Unmarshal(raw, &entry); err != nil {
			// A corrupt entry must not stop the relay from serving the
			// rest of someone's mail.
			return nil
		}
		identity := identityFromKey(dir)
		// Keys list in sequence order within an identity.
		s.entries[identity] = append(s.entries[identity], entry)
		if entry.Seq >= s.nextSeq[identity] {
			s.nextSeq[identity] = entry.Seq + 1
		}
		return nil
	})
}

func (s *spool[T]) entryKey(identity string, seq uint64) (string, error) {
	dir, err := identityKey(identity)
	if err != nil {
		return "", err
	}
	return s.prefix + dir + "/" + cursorOf(seq) + ".json", nil
}

// add appends an item. `max` caps the queue (0 = unbounded); `dropOldest`
// makes room instead of refusing, which is what the ack queue wants — a recent
// receipt is worth more than an old one, while a *message* must never be
// silently dropped.
func (s *spool[T]) add(identity string, item T, max int, dropOldest bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if max > 0 && len(s.entries[identity]) >= max {
		if !dropOldest {
			return false
		}
		oldest := s.entries[identity][0]
		s.entries[identity] = s.entries[identity][1:]
		s.removeFile(identity, oldest.Seq)
	}

	seq := s.nextSeq[identity]
	if seq == 0 {
		seq = 1
	}
	s.nextSeq[identity] = seq + 1
	entry := spoolEntry[T]{Seq: seq, At: time.Now().UTC(), Item: item}
	s.entries[identity] = append(s.entries[identity], entry)
	s.writeFile(identity, entry)
	return true
}

func (s *spool[T]) writeFile(identity string, entry spoolEntry[T]) {
	if s.objects == nil {
		return
	}
	key, err := s.entryKey(identity, entry.Seq)
	if err != nil {
		return
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = putObject(s.objects, key, raw)
}

func (s *spool[T]) removeFile(identity string, seq uint64) {
	if s.objects == nil {
		return
	}
	if key, err := s.entryKey(identity, seq); err == nil {
		_ = deleteObject(s.objects, key)
	}
}

// drain returns everything and deletes it — the pre-EPIC-009 pickup, kept for
// clients that have not moved to cursors.
func (s *spool[T]) drain(identity string) []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.entries[identity]
	if len(entries) == 0 {
		return nil
	}
	items := make([]T, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.Item)
		s.removeFile(identity, entry.Seq)
	}
	delete(s.entries, identity)
	return items
}

// since returns everything after `cursor` **without deleting**, plus the
// cursor to acknowledge once the client has the messages safely.
func (s *spool[T]) since(identity, cursor string) ([]T, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := parseCursor(cursor)
	items := []T{}
	highest := from
	for _, entry := range s.entries[identity] {
		if entry.Seq <= from {
			continue
		}
		items = append(items, entry.Item)
		if entry.Seq > highest {
			highest = entry.Seq
		}
	}
	return items, cursorOf(highest)
}

// consume drops everything up to and including `cursor`. This is the client
// saying "I have these", which is the only safe moment to forget them.
func (s *spool[T]) consume(identity, cursor string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	through := parseCursor(cursor)
	if through == 0 {
		return 0
	}
	kept := s.entries[identity][:0]
	removed := 0
	for _, entry := range s.entries[identity] {
		if entry.Seq <= through {
			s.removeFile(identity, entry.Seq)
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		delete(s.entries, identity)
	} else {
		s.entries[identity] = kept
	}
	return removed
}

// ExpiredEntry is one item the retention sweep gave up on.
type ExpiredEntry[T any] struct {
	Identity string
	Item     T
}

// expire drops entries older than `before` and reports them, so the caller can
// tell the sender their message was never collected.
func (s *spool[T]) expire(before time.Time) []ExpiredEntry[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expired []ExpiredEntry[T]
	for identity, entries := range s.entries {
		kept := entries[:0]
		for _, entry := range entries {
			if entry.At.Before(before) {
				expired = append(expired, ExpiredEntry[T]{Identity: identity, Item: entry.Item})
				s.removeFile(identity, entry.Seq)
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(s.entries, identity)
		} else {
			s.entries[identity] = kept
		}
	}
	return expired
}

func (s *spool[T]) count(identity string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries[identity])
}
