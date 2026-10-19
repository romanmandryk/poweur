package storage

import (
	"strings"
	"testing"
	"time"
)

func msgFrom(sender string, payloadBytes int) StoredMessage {
	return StoredMessage{ID: sender + "-" + time.Now().String(), Sender: sender, Recipient: "alice.poweur.net", Payload: strings.Repeat("x", payloadBytes)}
}

func TestInboxLimitsCountEachClassApart(t *testing.T) {
	s := NewInboxStore()
	mail := func(m StoredMessage) bool { return m.Sender == "email.poweur.net" }
	chat := func(m StoredMessage) bool { return !mail(m) }

	for i := 0; i < 3; i++ {
		if !s.AddLimited("alice.poweur.net", msgFrom("email.poweur.net", 10), SpoolLimits{Count: 3}, mail) {
			t.Fatalf("mail %d refused under the limit", i)
		}
	}
	if s.AddLimited("alice.poweur.net", msgFrom("email.poweur.net", 10), SpoolLimits{Count: 3}, mail) {
		t.Fatal("a fourth mail was accepted over a limit of 3")
	}
	// Three queued mails do not count against messages from people.
	for i := 0; i < 2; i++ {
		if !s.AddLimited("alice.poweur.net", msgFrom("bob.poweur.net", 10), SpoolLimits{Count: 2}, chat) {
			t.Fatalf("chat %d refused: mail crowded it out", i)
		}
	}
	if s.AddLimited("alice.poweur.net", msgFrom("bob.poweur.net", 10), SpoolLimits{Count: 2}, chat) {
		t.Fatal("a third chat was accepted over a limit of 2")
	}
}

func TestInboxByteLimitsAndTheRelayWideTotal(t *testing.T) {
	s := NewInboxStore()
	all := func(StoredMessage) bool { return true }
	if !s.AddLimited("a.poweur.net", msgFrom("x.poweur.net", 4000), SpoolLimits{Bytes: 10000}, all) {
		t.Fatal("first message refused")
	}
	if s.AddLimited("a.poweur.net", msgFrom("x.poweur.net", 7000), SpoolLimits{Bytes: 10000}, all) {
		t.Fatal("per-identity byte limit not enforced")
	}
	used := s.Bytes()
	if used < 4000 {
		t.Fatalf("total %d does not count the queued message", used)
	}
	// Another identity is refused once the whole spool would pass the total.
	if s.AddLimited("b.poweur.net", msgFrom("x.poweur.net", 5000), SpoolLimits{Total: used + 1000}, all) {
		t.Fatal("relay-wide total not enforced")
	}
	// Taking messages off the relay gives the room back, on every path.
	s.Drain("a.poweur.net")
	if s.Bytes() != 0 {
		t.Fatalf("drain left %d bytes counted", s.Bytes())
	}
	s.AddLimited("b.poweur.net", msgFrom("x.poweur.net", 100), SpoolLimits{}, all)
	_, cursor := s.Since("b.poweur.net", "")
	s.Consume("b.poweur.net", cursor)
	s.AddLimited("c.poweur.net", msgFrom("x.poweur.net", 100), SpoolLimits{}, all)
	s.Expire(time.Now().Add(time.Hour))
	if s.Bytes() != 0 {
		t.Fatalf("consume/expire left %d bytes counted", s.Bytes())
	}
}

func TestInboxTotalSurvivesARestart(t *testing.T) {
	dir := objectsAt(t)
	first, err := OpenInboxStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.AddLimited("a.poweur.net", msgFrom("x.poweur.net", 3000), SpoolLimits{}, func(StoredMessage) bool { return true })
	want := first.Bytes()
	second, err := OpenInboxStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Bytes(); got != want || got == 0 {
		t.Fatalf("after a restart the total is %d, want %d", got, want)
	}
}
