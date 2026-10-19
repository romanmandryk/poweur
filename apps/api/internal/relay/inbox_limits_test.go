package relay

import (
	"fmt"
	"strings"
	"testing"
)

func TestEmailAndMessagesQueueApart(t *testing.T) {
	s := newIdentityPageServer(t)
	s.cfg.EmailBridgeID = "email.poweur.net"
	s.cfg.MaxInboxPerIdentity, s.cfg.MaxInboxBytesPerIdentity = 3, 1<<20
	s.cfg.MaxEmailInboxPerIdentity, s.cfg.MaxEmailInboxBytesPerIdentity = 2, 1<<20
	s.cfg.MaxInboxBytesTotal = 1 << 30
	msg := func(from string, i int) Message {
		return Message{ID: fmt.Sprintf("%s-%d", from, i), Sender: from, Recipient: pageIdentity, Payload: strings.Repeat("x", 100)}
	}

	// The email queue fills up...
	for i := 0; i < 2; i++ {
		if !s.enqueue(msg("email.poweur.net", i)) {
			t.Fatalf("mail %d refused under its limit", i)
		}
	}
	if s.enqueue(msg("EMAIL.poweur.net", 9)) {
		t.Fatal("mail accepted over its limit (the sender check must ignore case)")
	}
	// ...and people still get through, up to their own limit.
	for i := 0; i < 3; i++ {
		if !s.enqueue(msg("bob.poweur.net", i)) {
			t.Fatalf("message %d refused: email crowded it out", i)
		}
	}
	if s.enqueue(msg("carol.poweur.net", 0)) {
		t.Fatal("message accepted over its limit")
	}

	// The relay-wide byte limit refuses everyone once reached.
	s2 := newIdentityPageServer(t)
	s2.cfg.EmailBridgeID = "email.poweur.net"
	s2.cfg.MaxInboxBytesTotal = 600
	if !s2.enqueue(msg("bob.poweur.net", 0)) {
		t.Fatal("first message refused")
	}
	if s2.enqueue(msg("email.poweur.net", 1)) {
		t.Fatal("relay-wide byte limit not enforced")
	}
}
