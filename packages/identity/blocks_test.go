package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func signedList(t *testing.T, publisher string, entries []BlockEntry) (Blocklist, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	list := NewBlocklist(publisher, "community list", entries)
	if err := list.Sign(priv); err != nil {
		t.Fatal(err)
	}
	return list, pub
}

func TestBlocklistValidate(t *testing.T) {
	base := func() Blocklist {
		return NewBlocklist("alice.poweur.net", "list", []BlockEntry{{Identity: "spam.poweur.net"}})
	}
	cases := []struct {
		name   string
		mutate func(*Blocklist)
		wantOK bool
	}{
		{"valid", func(*Blocklist) {}, true},
		{"future version", func(b *Blocklist) { b.Version = 7 }, false},
		{"no publisher", func(b *Blocklist) { b.Publisher = "" }, false},
		{"empty list is legal", func(b *Blocklist) { b.Entries = nil }, true},
		{"blank identity", func(b *Blocklist) { b.Entries = []BlockEntry{{Identity: " "}} }, false},
		{"publisher blocks themselves", func(b *Blocklist) {
			b.Entries = []BlockEntry{{Identity: "alice.poweur.net"}}
		}, false},
		{"duplicate entries", func(b *Blocklist) {
			b.Entries = []BlockEntry{{Identity: "x.poweur.net"}, {Identity: "x.poweur.net"}}
		}, false},
		{"name too long", func(b *Blocklist) { b.Name = strings.Repeat("n", MaxBlocklistName+1) }, false},
		{"bad added_at", func(b *Blocklist) {
			b.Entries = []BlockEntry{{Identity: "x.poweur.net", AddedAt: "soon"}}
		}, false},
		{"no updated_at", func(b *Blocklist) { b.UpdatedAt = "" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := base()
			tc.mutate(&list)
			err := list.Validate()
			if tc.wantOK != (err == nil) {
				t.Fatalf("Validate = %v, wantOK %v", err, tc.wantOK)
			}
		})
	}
}

func TestNewBlocklistNormalisesAndDedupes(t *testing.T) {
	list := NewBlocklist("ALICE.poweur.net", " list ", []BlockEntry{
		{Identity: "Zed.poweur.net"},
		{Identity: "abe.poweur.net", Reason: AbuseReasonSpam},
		{Identity: "ZED.poweur.net", Reason: AbuseReasonPhishing},
		{Identity: "  "},
	})
	if list.Publisher != "alice.poweur.net" || list.Name != "list" {
		t.Fatalf("header not normalised: %+v", list)
	}
	if len(list.Entries) != 2 {
		t.Fatalf("entries = %+v", list.Entries)
	}
	if list.Entries[0].Identity != "abe.poweur.net" || list.Entries[1].Identity != "zed.poweur.net" {
		t.Fatalf("entries not sorted/normalised: %+v", list.Entries)
	}
	if list.Entries[1].Reason != AbuseReasonPhishing {
		t.Fatalf("last entry for an identity should win: %+v", list.Entries[1])
	}
}

func TestBlocklistSignAndVerify(t *testing.T) {
	list, pub := signedList(t, "alice.poweur.net", []BlockEntry{
		{Identity: "spam.poweur.net", Reason: AbuseReasonSpam},
		{Identity: "phish.poweur.net", Reason: AbuseReasonPhishing},
	})
	if err := list.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}

	// Appending a name in transit is the whole attack this signature exists
	// to stop.
	appended := list
	appended.Entries = append(append([]BlockEntry(nil), list.Entries...), BlockEntry{Identity: "innocent.poweur.net"})
	if err := appended.VerifySignature(pub); err == nil {
		t.Fatal("an appended entry must break the signature")
	}

	relabelled := list
	relabelled.Entries = append([]BlockEntry(nil), list.Entries...)
	relabelled.Entries[0].Reason = AbuseReasonMalware
	if err := relabelled.VerifySignature(pub); err == nil {
		t.Fatal("re-labelling an entry must break the signature")
	}

	renamed := list
	renamed.Publisher = "mallory.poweur.net"
	if err := renamed.VerifySignature(pub); err == nil {
		t.Fatal("changing the publisher must break the signature")
	}
}

func TestParseBlocklistRefusesUnsigned(t *testing.T) {
	list := NewBlocklist("alice.poweur.net", "l", []BlockEntry{{Identity: "x.poweur.net"}})
	raw, _ := json.Marshal(list)
	if _, err := ParseBlocklist(raw); err == nil {
		t.Fatal("an unsigned blocklist must be refused")
	}
}

func TestBlocklistFromContacts(t *testing.T) {
	contacts := ContactsFile{Version: 1, Contacts: []Contact{
		{Identity: "friend.poweur.net", State: ContactAccepted},
		{Identity: "pending.poweur.net", State: ContactRequested},
		{Identity: "spam.poweur.net", State: ContactBlocked, AddedAt: "2026-01-01T00:00:00Z"},
	}}
	list := BlocklistFromContacts("alice.poweur.net", "mine", contacts)
	if len(list.Entries) != 1 || list.Entries[0].Identity != "spam.poweur.net" {
		t.Fatalf("only blocked contacts export: %+v", list.Entries)
	}
	if list.Entries[0].AddedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("added_at should carry over: %+v", list.Entries[0])
	}
}

func TestApplyBlocklist(t *testing.T) {
	contacts := ContactsFile{Version: 1, Contacts: []Contact{
		{Identity: "friend.poweur.net", State: ContactAccepted, Petname: "Bestie", PinnedKey: ""},
		{Identity: "known.poweur.net", State: ContactBlocked},
		{Identity: "asked.poweur.net", State: ContactRequested},
	}}
	list := NewBlocklist("carol.poweur.net", "list", []BlockEntry{
		{Identity: "stranger.poweur.net"},
		{Identity: "friend.poweur.net"},
		{Identity: "known.poweur.net"},
		{Identity: "asked.poweur.net"},
		{Identity: "me.poweur.net"},
	})

	updated, merge := ApplyBlocklist(contacts, "me.poweur.net", list, false)
	if len(merge.Blocked) != 2 {
		t.Fatalf("blocked = %v", merge.Blocked)
	}
	if len(merge.AlreadyBlocked) != 1 || merge.AlreadyBlocked[0] != "known.poweur.net" {
		t.Fatalf("already = %v", merge.AlreadyBlocked)
	}
	if len(merge.SkippedAccepted) != 1 || merge.SkippedAccepted[0] != "friend.poweur.net" {
		t.Fatalf("an adopted list must never silently cut off a chosen contact: %v", merge.SkippedAccepted)
	}
	if !merge.SkippedSelf {
		t.Fatal("a list naming the importer must not block the importer")
	}
	if c, _ := updated.Find("friend.poweur.net"); c.State != ContactAccepted {
		t.Fatalf("friend state = %q", c.State)
	}
	if c, _ := updated.Find("stranger.poweur.net"); c.Source != "blocklist:carol.poweur.net" {
		t.Fatalf("provenance missing: %+v", c)
	}
	if _, found := updated.Find("me.poweur.net"); found {
		t.Fatal("the importer must not be written into their own contacts")
	}

	// force is the explicit override, and it keeps what the importer knew.
	forced, merge := ApplyBlocklist(contacts, "me.poweur.net", list, true)
	if len(merge.SkippedAccepted) != 0 {
		t.Fatalf("force should adopt accepted contacts: %v", merge.SkippedAccepted)
	}
	c, _ := forced.Find("friend.poweur.net")
	if c.State != ContactBlocked || c.Petname != "Bestie" {
		t.Fatalf("forced entry = %+v", c)
	}
}
