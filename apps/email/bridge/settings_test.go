package bridge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
)

// Email is on by default: without email.json the bridge takes mail for an
// identity whose home relay names it and reports email_receive "on".
func TestDefaultOnWithoutOptIn(t *testing.T) {
	b := newTestBridge(t)
	john := b.net.add("john.poweur.test", "")
	b.net.byDefault(john, b.self.name, "on")
	off := b.net.add("off.poweur.test", "")
	b.net.byDefault(off, b.self.name, "off")
	other := b.net.add("other.poweur.test", "")
	b.net.byDefault(other, "other-bridge.poweur.test", "on")
	b.net.add("bare.poweur.test", "") // no capabilities at all
	bearer := "Bearer hook-secret-0123456789"

	for to, want := range map[string]string{
		"john@poweur.test": "accept", "john+x@alias.test": "accept",
		"off@poweur.test": "reject", "other@poweur.test": "reject", "bare@poweur.test": "reject",
	} {
		if _, out := postHook(t, b, bearer, "rcpt", to); out.Action != want {
			t.Errorf("hook %s = %s, want %s", to, out.Action, want)
		}
	}

	addr := startLMTP(t, b)
	if res := deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john@poweur.test"}, sampleMail); res.dataErr != nil {
		t.Fatalf("deliver: %v", res.dataErr)
	}
	if n := len(b.net.delivered(john, identity.MsgTypeEmailMessage)); n != 1 {
		t.Fatalf("delivered %d", n)
	}

	// Sending works too, from the address the name gives.
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, identity.EmailSend{Version: 1, SendID: "D1",
		To: []identity.EmailAddress{{Address: "ann@example.com"}}, Subject: "Hi", Text: "hi"})
	runOutbound(t, b)
	if sent := readSent(t, b.logDir); len(sent) != 1 || !strings.Contains(sent[0], "From: <john@poweur.test>") && !strings.Contains(sent[0], "From: john@poweur.test") {
		t.Fatalf("sent: %v", sent)
	}

	// Opting out takes effect once the cached answer expires.
	b.net.byDefault(john, b.self.name, "off")
	b.Dir.Forget(john.name)
	if _, out := postHook(t, b, bearer, "rcpt", "john@poweur.test"); out.Action != "reject" {
		t.Fatal("opted-out identity still accepted")
	}
}

func mailFrom(from, msgID, inReplyTo string) string {
	extra := ""
	if inReplyTo != "" {
		extra = "In-Reply-To: <" + inReplyTo + ">\n"
	}
	return fmt.Sprintf("Authentication-Results: mail.poweur.test; dmarc=pass header.from=x\nFrom: %s\nTo: john@poweur.test\nSubject: s\nMessage-ID: <%s>\n%sDate: Tue, 13 Oct 2026 10:00:00 +0000\n\nbody\n", from, msgID, extra)
}

func TestReceiveFilters(t *testing.T) {
	b := newTestBridge(t)
	john := b.net.add("john.poweur.test", b.self.name)
	addr := startLMTP(t, b)
	n := 0
	deliver := func(from, inReplyTo string) bool {
		t.Helper()
		n++
		before := len(b.net.delivered(john, identity.MsgTypeEmailMessage))
		res := deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john@poweur.test"}, mailFrom(from, fmt.Sprintf("m%d@x", n), inReplyTo))
		if res.dataErr != nil {
			t.Fatalf("filtered mail must still be accepted: %v", res.dataErr)
		}
		return len(b.net.delivered(john, identity.MsgTypeEmailMessage)) > before
	}
	settings := func(s identity.EmailSettings) {
		s.Version = 1
		if s.UpdatedAt == "" {
			s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSettings, s)
		runOutbound(t, b)
	}

	if !deliver("stranger@example.com", "") {
		t.Fatal("no settings: everyone should get through")
	}

	settings(identity.EmailSettings{Block: []string{"pest@example.com", "@spam.test"}, UpdatedAt: "2026-10-14T10:00:00Z"})
	if deliver("pest@example.com", "") || deliver("x@mx.spam.test", "") {
		t.Fatal("blocked sender delivered")
	}
	if !deliver("stranger@example.com", "") {
		t.Fatal("unblocked sender dropped")
	}

	// Older settings arriving late do not undo newer ones.
	settings(identity.EmailSettings{UpdatedAt: "2026-10-14T09:00:00Z"})
	if deliver("pest@example.com", "") {
		t.Fatal("stale settings applied")
	}

	settings(identity.EmailSettings{Receive: identity.EmailReceiveContacts, Allow: []string{"@friends.test"}, Block: []string{"enemy@friends.test"}, UpdatedAt: "2026-10-14T11:00:00Z"})
	if deliver("stranger@example.com", "") {
		t.Fatal("contacts only: stranger delivered")
	}
	if !deliver("pal@friends.test", "") || deliver("enemy@friends.test", "") {
		t.Fatal("allow by domain / block wins")
	}
	// Someone John emails becomes a contact, and replies to his mail get through.
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, identity.EmailSend{Version: 1, SendID: "F1",
		To: []identity.EmailAddress{{Address: "ann@example.com"}}, Subject: "Hi", Text: "hi"})
	runOutbound(t, b)
	if !deliver("ann@example.com", "") {
		t.Fatal("someone John emailed was dropped")
	}
	rec, ok, _ := b.Store.ClaimSend(t.Context(), "john.poweur.test", "F1")
	if ok || rec.MessageID == "" {
		t.Fatalf("send record: %+v %v", rec, ok)
	}
	if !deliver("ann.other@example.org", strings.Trim(rec.MessageID, "<>")) {
		t.Fatal("reply to John's mail from a new address dropped")
	}
	// Stores no addresses in the clear.
	f, _, _ := b.Store.Filters(t.Context(), "john.poweur.test")
	for k := range f.Allow {
		if strings.Contains(k, "friends") {
			t.Fatal("filter stored in clear")
		}
	}
}

func TestSettingsNeedAMailbox(t *testing.T) {
	b := newTestBridge(t)
	quiet := b.net.add("quiet.poweur.test", "")
	b.net.sendToBridge(quiet, b.self, identity.MsgTypeEmailSettings, identity.EmailSettings{Version: 1, Receive: "contacts", UpdatedAt: "2026-10-14T10:00:00Z"})
	runOutbound(t, b)
	if _, ok, _ := b.Store.Filters(t.Context(), "quiet.poweur.test"); ok {
		t.Fatal("settings stored for an identity without email")
	}
	_ = http.StatusOK
}
