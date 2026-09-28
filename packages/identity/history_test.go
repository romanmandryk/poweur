package identity

import (
	"strings"
	"testing"
)

func TestHistoryPeerHashHidesTheName(t *testing.T) {
	if HistoryPeerHash("Bob.Poweur.net") != HistoryPeerHash("  bob.poweur.net ") {
		t.Fatal("peer hash is not case-insensitive")
	}
	got := HistoryLogName("alice.poweur.net")
	if strings.Contains(got, "alice") || len(got) != 64+len(".jsonl") {
		t.Fatalf("log name %s", got)
	}
	if HistoryPeerHash("alice.poweur.net") == HistoryPeerHash("bob.poweur.net") {
		t.Fatal("distinct peers hashed equal")
	}
}

func TestHistoryPathIsSortableAndSharded(t *testing.T) {
	early := HistoryPath("2026-09-09T08:15:00Z", "msg_1_aaa")
	later := HistoryPath("2026-09-09T09:15:00Z", "msg_2_bbb")
	nextMonth := HistoryPath("2026-10-01T00:00:00Z", "msg_3_ccc")

	if !strings.HasPrefix(early, HistoryDir+"/2026-09/") {
		t.Fatalf("shard: %s", early)
	}
	if !strings.HasPrefix(nextMonth, HistoryDir+"/2026-10/") {
		t.Fatalf("shard rolls with the month: %s", nextMonth)
	}
	// Within a shard, lexical order is chronological — that is the whole
	// reason the sort key leads the filename.
	if !(early < later) {
		t.Fatalf("%s should sort before %s", early, later)
	}
	// Idempotent: the same message always lands on the same path, so two
	// devices picking it up write the same bytes rather than two copies.
	if HistoryPath("2026-09-09T08:15:00Z", "msg_1_aaa") != early {
		t.Fatal("history path must be deterministic")
	}
}

// The id in a filename comes off the wire, chosen by the sender. It must
// never become a path of the sender's choosing inside the recipient's tree.
func TestHistoryFileNameNeutralizesHostileIDs(t *testing.T) {
	hostile := []string{
		"../../../poweur-sys/relay/contacts",
		"a/b",
		"..",
		"",
		strings.Repeat("x", 300),
		"msg with spaces",
		".poweur-web-public",
	}
	for _, id := range hostile {
		name := HistoryFileName("2026-09-09T08:15:00Z", id)
		if strings.ContainsAny(name, "/\\") {
			t.Fatalf("id %q produced a path separator: %s", id, name)
		}
		if strings.Contains(name, "..") {
			t.Fatalf("id %q produced a traversal segment: %s", id, name)
		}
		if strings.HasPrefix(name, ".") {
			t.Fatalf("id %q produced a dotfile: %s", id, name)
		}
		if len(name) > 255 {
			t.Fatalf("id %q produced an over-long segment (%d bytes)", id, len(name))
		}
		// Still deterministic, or the write stops being idempotent.
		if HistoryFileName("2026-09-09T08:15:00Z", id) != name {
			t.Fatalf("id %q hashed differently on the second call", id)
		}
	}
	// Two different hostile ids must not collide onto one file.
	a := HistoryFileName("2026-09-09T08:15:00Z", "a/b")
	b := HistoryFileName("2026-09-09T08:15:00Z", "a/c")
	if a == b {
		t.Fatal("distinct ids collided after sanitizing")
	}
	// An ordinary id is left alone — the hash is a fallback, not the norm.
	plain := HistoryFileName("2026-09-09T08:15:00Z", "msg_1757404500000_AbC-_9")
	if !strings.Contains(plain, "msg_1757404500000_AbC-_9") {
		t.Fatalf("safe id was rewritten: %s", plain)
	}
}

func TestHistoryRecordValidate(t *testing.T) {
	ok := HistoryRecord{
		Version: HistoryVersion, ID: "msg_1", Sender: "bob.poweur.net",
		Recipient: "alice.poweur.net", Timestamp: "2026-09-09T08:15:00Z",
		Queue: HistoryQueueInbox, Body: "hi",
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		mut  func(*HistoryRecord)
		want string
	}{
		{"no id", func(r *HistoryRecord) { r.ID = "" }, "id is required"},
		{"no recipient", func(r *HistoryRecord) { r.Recipient = "" }, "recipient is required"},
		{"bad time", func(r *HistoryRecord) { r.Timestamp = "yesterday" }, "RFC3339"},
		{"bad queue", func(r *HistoryRecord) { r.Queue = "drafts" }, "invalid queue"},
		{"huge body", func(r *HistoryRecord) { r.Body = strings.Repeat("x", MaxHistoryBody+1) }, "exceeds"},
		{"bad version", func(r *HistoryRecord) { r.Version = 9 }, "unsupported"},
	}
	for _, tc := range cases {
		r := ok
		tc.mut(&r)
		err := r.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestHistoryRecordPeer(t *testing.T) {
	const owner = "alice.poweur.net"
	inbound := HistoryRecord{Sender: "Bob.Poweur.net", Recipient: owner, Queue: HistoryQueueInbox}
	if got := inbound.Peer(owner); got != "bob.poweur.net" {
		t.Fatalf("inbound peer = %q", got)
	}
	outbound := HistoryRecord{Sender: owner, Recipient: "Bob.Poweur.net", Queue: HistoryQueueSent}
	if got := outbound.Peer(owner); got != "bob.poweur.net" {
		t.Fatalf("outbound peer = %q — both directions must thread together", got)
	}
	anon := HistoryRecord{Recipient: owner, Queue: HistoryQueueAnonymous}
	if got := anon.Peer(owner); got != AnonymousPeer {
		t.Fatalf("anonymous peer = %q", got)
	}
}

func TestReadStateMarkAndCount(t *testing.T) {
	const owner = "alice.poweur.net"
	records := []HistoryRecord{
		{ID: "1", Sender: "bob.poweur.net", Recipient: owner, Timestamp: "2026-09-09T08:00:00Z", Queue: HistoryQueueInbox},
		{ID: "2", Sender: "bob.poweur.net", Recipient: owner, Timestamp: "2026-09-09T09:00:00Z", Queue: HistoryQueueInbox},
		{ID: "3", Sender: owner, Recipient: "bob.poweur.net", Timestamp: "2026-09-09T10:00:00Z", Queue: HistoryQueueSent},
		{ID: "4", Recipient: owner, Timestamp: "2026-09-09T11:00:00Z", Queue: HistoryQueueAnonymous},
	}

	var s ReadState
	unread := s.Unread(owner, records)
	if unread["bob.poweur.net"] != 2 || unread[AnonymousPeer] != 1 {
		t.Fatalf("fresh read state: %v", unread)
	}
	// Our own sent message is never unread.
	if len(unread) != 2 {
		t.Fatalf("sent messages must not count as unread: %v", unread)
	}

	s = s.MarkRead("Bob.Poweur.net", "2026-09-09T08:00:00Z", "1")
	if got := s.Unread(owner, records)["bob.poweur.net"]; got != 1 {
		t.Fatalf("after reading the first message, unread = %d", got)
	}

	s = s.MarkRead("bob.poweur.net", "2026-09-09T09:00:00Z", "2")
	if got := s.Unread(owner, records)["bob.poweur.net"]; got != 0 {
		t.Fatalf("a fully read conversation must reach zero, got %d", got)
	}

	// A stale mark from a slower device must not un-read the conversation.
	s = s.MarkRead("bob.poweur.net", "2026-09-09T08:00:00Z", "1")
	if got := s.Unread(owner, records)["bob.poweur.net"]; got != 0 {
		t.Fatalf("read marks must never rewind, got %d unread", got)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Two messages in the same second are the case a timestamp-only mark gets
// wrong: reading the first must not silently mark the second read.
func TestReadStateDistinguishesSameSecondMessages(t *testing.T) {
	const owner = "alice.poweur.net"
	records := []HistoryRecord{
		{ID: "m1", Sender: "bob.poweur.net", Recipient: owner, Timestamp: "2026-09-09T08:00:00Z", Queue: HistoryQueueInbox},
		{ID: "m2", Sender: "bob.poweur.net", Recipient: owner, Timestamp: "2026-09-09T08:00:00Z", Queue: HistoryQueueInbox},
	}
	var s ReadState
	s = s.MarkRead("bob.poweur.net", "2026-09-09T08:00:00Z", "m1")
	if got := s.Unread(owner, records)["bob.poweur.net"]; got != 1 {
		t.Fatalf("reading m1 left %d unread, want 1 (m2 shares its second)", got)
	}
	s = s.MarkRead("bob.poweur.net", "2026-09-09T08:00:00Z", "m2")
	if got := s.Unread(owner, records)["bob.poweur.net"]; got != 0 {
		t.Fatalf("after reading both, %d unread", got)
	}
}

func TestParseReadStateRejectsJunk(t *testing.T) {
	if _, err := ParseReadState([]byte(`{"version":1,"conversations":{"bob":{"timestamp":"nope"}}}`)); err == nil {
		t.Fatal("want a timestamp error")
	}
	s, err := ParseReadState([]byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Conversations == nil {
		t.Fatal("conversations must be usable after parsing an empty document")
	}
}

func TestParseSealedDocument(t *testing.T) {
	good := `{"version":1,"alg":"x25519-chacha20-poly1305","ephemeral_public_key":"e","nonce":"n","ciphertext":"c"}`
	if _, err := ParseSealedDocument([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"version":2,"alg":"a","ephemeral_public_key":"e","nonce":"n","ciphertext":"c"}`,
		`{"version":1,"alg":"a","nonce":"n","ciphertext":"c"}`,
		`not json`,
	} {
		if _, err := ParseSealedDocument([]byte(bad)); err == nil {
			t.Fatalf("want an error for %s", bad)
		}
	}
}

func TestSortHistoryIsTotal(t *testing.T) {
	records := []HistoryRecord{
		{ID: "b", Timestamp: "2026-09-09T09:00:00Z"},
		{ID: "a", Timestamp: "2026-09-09T09:00:00Z"},
		{ID: "c", Timestamp: "2026-09-09T08:00:00Z"},
	}
	SortHistory(records)
	got := records[0].ID + records[1].ID + records[2].ID
	if got != "cab" {
		t.Fatalf("sorted order = %s, want cab (oldest first, id breaking ties)", got)
	}
}
